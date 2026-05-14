package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type TransformRepo struct{ db *sql.DB }

func (r *TransformRepo) ListByPipeline(ctx context.Context, pipelineID string) ([]*Transform, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, pipeline_id, transform_type, source_path, target_path,
		       mask_pattern, mask_replace, set_value, ord
		FROM transforms WHERE pipeline_id=? ORDER BY ord ASC`, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("list transforms: %w", err)
	}
	defer rows.Close()
	var out []*Transform
	for rows.Next() {
		t := &Transform{}
		if err := rows.Scan(&t.ID, &t.PipelineID, &t.TransformType,
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

func (r *TransformRepo) ReplaceForPipeline(ctx context.Context, pipelineID string, rules []*Transform) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM transforms WHERE pipeline_id=?`, pipelineID); err != nil {
		return fmt.Errorf("clear transforms: %w", err)
	}
	for _, t := range rules {
		if t.ID == "" {
			t.ID = uuid.NewString()
		}
		t.PipelineID = pipelineID
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transforms (id, pipeline_id, transform_type, source_path, target_path,
			                        mask_pattern, mask_replace, set_value, ord)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.PipelineID, t.TransformType, t.SourcePath, t.TargetPath,
			t.MaskPattern, t.MaskReplace, t.SetValue, t.Order); err != nil {
			return fmt.Errorf("insert transform: %w", err)
		}
	}
	return tx.Commit()
}
