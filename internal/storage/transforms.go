package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type TransformRepo struct{ db *sql.DB }

func (r *TransformRepo) ListByPipeline(ctx context.Context, tenantID, pipelineID string) ([]*Transform, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	return r.listByPipeline(ctx, pipelineID, &tenantID)
}

// ListByPipelineUnsafe — internal only (pipeline manager boot).
func (r *TransformRepo) ListByPipelineUnsafe(ctx context.Context, pipelineID string) ([]*Transform, error) {
	return r.listByPipeline(ctx, pipelineID, nil)
}

func (r *TransformRepo) listByPipeline(ctx context.Context, pipelineID string, tenantFilter *string) ([]*Transform, error) {
	q := `
		SELECT id, tenant_id, pipeline_id, transform_type, source_path, target_path,
		       mask_pattern, mask_replace, set_value, ord
		FROM transforms WHERE pipeline_id=?`
	args := []any{pipelineID}
	if tenantFilter != nil {
		q += ` AND tenant_id=?`
		args = append(args, *tenantFilter)
	}
	q += ` ORDER BY ord ASC`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list transforms: %w", err)
	}
	defer rows.Close()
	var out []*Transform
	for rows.Next() {
		t := &Transform{}
		if err := rows.Scan(&t.ID, &t.TenantID, &t.PipelineID, &t.TransformType,
			&t.SourcePath, &t.TargetPath, &t.MaskPattern, &t.MaskReplace,
			&t.SetValue, &t.Order); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *TransformRepo) ReplaceForPipeline(ctx context.Context, tenantID, pipelineID string, rules []*Transform) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM transforms WHERE pipeline_id=? AND tenant_id=?`, pipelineID, tenantID); err != nil {
		return fmt.Errorf("clear transforms: %w", err)
	}
	for _, t := range rules {
		if t.ID == "" {
			t.ID = uuid.NewString()
		}
		t.PipelineID = pipelineID
		t.TenantID = tenantID
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transforms (id, tenant_id, pipeline_id, transform_type, source_path, target_path,
			                        mask_pattern, mask_replace, set_value, ord)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, tenantID, t.PipelineID, t.TransformType, t.SourcePath, t.TargetPath,
			t.MaskPattern, t.MaskReplace, t.SetValue, t.Order); err != nil {
			return fmt.Errorf("insert transform: %w", err)
		}
	}
	return tx.Commit()
}
