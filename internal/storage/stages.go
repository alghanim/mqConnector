package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type StageRepo struct{ db *sql.DB }

func (r *StageRepo) ListByPipeline(ctx context.Context, pipelineID string) ([]*Stage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, pipeline_id, stage_order, stage_type, stage_config, enabled
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

func (r *StageRepo) Create(ctx context.Context, s *Stage) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.StageConfig == "" {
		s.StageConfig = "{}"
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO stages (id, pipeline_id, stage_order, stage_type, stage_config, enabled)
		VALUES (?, ?, ?, ?, ?, ?)`,
		s.ID, s.PipelineID, s.StageOrder, s.StageType, s.StageConfig, s.Enabled)
	if err != nil {
		return fmt.Errorf("insert stage: %w", err)
	}
	return nil
}

func (r *StageRepo) ReplaceForPipeline(ctx context.Context, pipelineID string, stages []*Stage) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM stages WHERE pipeline_id=?`, pipelineID); err != nil {
		return fmt.Errorf("clear stages: %w", err)
	}
	for _, s := range stages {
		if s.ID == "" {
			s.ID = uuid.NewString()
		}
		s.PipelineID = pipelineID
		if s.StageConfig == "" {
			s.StageConfig = "{}"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO stages (id, pipeline_id, stage_order, stage_type, stage_config, enabled)
			VALUES (?, ?, ?, ?, ?, ?)`,
			s.ID, s.PipelineID, s.StageOrder, s.StageType, s.StageConfig, s.Enabled); err != nil {
			return fmt.Errorf("insert stage: %w", err)
		}
	}
	return tx.Commit()
}

func scanStage(rows *sql.Rows) (*Stage, error) {
	s := &Stage{}
	err := rows.Scan(&s.ID, &s.PipelineID, &s.StageOrder, &s.StageType, &s.StageConfig, &s.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}
