package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type PipelineRepo struct{ db *sql.DB }

func (r *PipelineRepo) Create(ctx context.Context, p *Pipeline) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.OutputFormat == "" {
		p.OutputFormat = "same"
	}
	if p.FilterPaths == nil {
		p.FilterPaths = []string{}
	}
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	pathsJSON, _ := json.Marshal(p.FilterPaths)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO pipelines (id, name, source_id, destination_id, output_format,
		                       schema_id, filter_paths, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.SourceID, p.DestinationID, p.OutputFormat,
		nullable(p.SchemaID), string(pathsJSON), p.Enabled, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert pipeline: %w", err)
	}
	return nil
}

func (r *PipelineRepo) Update(ctx context.Context, p *Pipeline) error {
	p.UpdatedAt = time.Now().UTC()
	if p.FilterPaths == nil {
		p.FilterPaths = []string{}
	}
	pathsJSON, _ := json.Marshal(p.FilterPaths)
	res, err := r.db.ExecContext(ctx, `
		UPDATE pipelines SET name=?, source_id=?, destination_id=?, output_format=?,
		                     schema_id=?, filter_paths=?, enabled=?, updated_at=?
		WHERE id=?`,
		p.Name, p.SourceID, p.DestinationID, p.OutputFormat,
		nullable(p.SchemaID), string(pathsJSON), p.Enabled, p.UpdatedAt, p.ID)
	if err != nil {
		return fmt.Errorf("update pipeline: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PipelineRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM pipelines WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete pipeline: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PipelineRepo) Get(ctx context.Context, id string) (*Pipeline, error) {
	row := r.db.QueryRowContext(ctx, pipelineSelect+` WHERE id=?`, id)
	return scanPipeline(row)
}

func (r *PipelineRepo) List(ctx context.Context) ([]*Pipeline, error) {
	rows, err := r.db.QueryContext(ctx, pipelineSelect+` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list pipelines: %w", err)
	}
	defer rows.Close()
	var out []*Pipeline
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const pipelineSelect = `
SELECT id, name, source_id, destination_id, output_format, COALESCE(schema_id,''),
       filter_paths, enabled, created_at, updated_at
FROM pipelines`

func scanPipeline(s scanner) (*Pipeline, error) {
	p := &Pipeline{}
	var pathsJSON string
	err := s.Scan(&p.ID, &p.Name, &p.SourceID, &p.DestinationID, &p.OutputFormat,
		&p.SchemaID, &pathsJSON, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if pathsJSON != "" {
		if err := json.Unmarshal([]byte(pathsJSON), &p.FilterPaths); err != nil {
			return nil, fmt.Errorf("decode filter_paths: %w", err)
		}
	}
	if p.FilterPaths == nil {
		p.FilterPaths = []string{}
	}
	return p, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
