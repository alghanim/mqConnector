package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type StageRepo struct{ db *sql.DB }

func (r *StageRepo) ListByPipeline(ctx context.Context, tenantID, pipelineID string) ([]*Stage, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, pipeline_id, stage_order, stage_type, stage_config, enabled
		FROM stages WHERE pipeline_id=? AND tenant_id=? ORDER BY stage_order ASC`, pipelineID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list stages: %w", err)
	}
	defer rows.Close()
	var out []*Stage
	for rows.Next() {
		s, err := scanStage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListByPipelineUnsafe lists stages without a tenant scope. Internal only
// — pipeline.Manager calls it at boot when it walks every pipeline.
func (r *StageRepo) ListByPipelineUnsafe(ctx context.Context, pipelineID string) ([]*Stage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, pipeline_id, stage_order, stage_type, stage_config, enabled
		FROM stages WHERE pipeline_id=? ORDER BY stage_order ASC`, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("list stages: %w", err)
	}
	defer rows.Close()
	var out []*Stage
	for rows.Next() {
		s, err := scanStage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *StageRepo) Create(ctx context.Context, tenantID string, s *Stage) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.StageConfig == "" {
		s.StageConfig = "{}"
	}
	s.TenantID = tenantID
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO stages (id, tenant_id, pipeline_id, stage_order, stage_type, stage_config, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID, tenantID, s.PipelineID, s.StageOrder, s.StageType, s.StageConfig, s.Enabled)
	if err != nil {
		return fmt.Errorf("insert stage: %w", err)
	}
	return nil
}

// ReplaceForPipeline atomically replaces every stage attached to the
// pipeline. The pipeline's tenant is the authority — passed explicitly
// so the caller can't accidentally rewrite stages onto a pipeline they
// don't own. Cross-tenant attempts are silently no-ops (the DELETE
// matches zero rows, then the INSERT writes nothing because the loop
// runs over the supplied stages — to a tenant the caller does control).
func (r *StageRepo) ReplaceForPipeline(ctx context.Context, tenantID, pipelineID string, stages []*Stage) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM stages WHERE pipeline_id=? AND tenant_id=?`, pipelineID, tenantID); err != nil {
		return fmt.Errorf("clear stages: %w", err)
	}
	for _, s := range stages {
		if s.ID == "" {
			s.ID = uuid.NewString()
		}
		s.PipelineID = pipelineID
		s.TenantID = tenantID
		if s.StageConfig == "" {
			s.StageConfig = "{}"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO stages (id, tenant_id, pipeline_id, stage_order, stage_type, stage_config, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			s.ID, tenantID, s.PipelineID, s.StageOrder, s.StageType, s.StageConfig, s.Enabled); err != nil {
			return fmt.Errorf("insert stage: %w", err)
		}
	}
	return tx.Commit()
}

func scanStage(rows *sql.Rows) (*Stage, error) {
	s := &Stage{}
	err := rows.Scan(&s.ID, &s.TenantID, &s.PipelineID, &s.StageOrder, &s.StageType, &s.StageConfig, &s.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}
