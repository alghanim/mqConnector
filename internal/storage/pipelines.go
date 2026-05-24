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

type PipelineRepo struct{ db *dbWrap }

func (r *PipelineRepo) Create(ctx context.Context, tenantID string, p *Pipeline) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.OutputFormat == "" {
		p.OutputFormat = "same"
	}
	if p.FilterPaths == nil {
		p.FilterPaths = []string{}
	}
	p.TenantID = tenantID
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	pathsJSON, _ := json.Marshal(p.FilterPaths)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO pipelines (id, tenant_id, name, source_id, destination_id, output_format,
		                       schema_id, filter_paths, enabled, workers, retry_max,
		                       retry_backoff_ms, max_msgs_per_minute, dedup_window_seconds,
		                       shadow_destination_id, shadow_percent, requires_approval,
		                       created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, tenantID, p.Name, p.SourceID, p.DestinationID, p.OutputFormat,
		nullable(p.SchemaID), string(pathsJSON), p.Enabled,
		p.Workers, p.RetryMax, p.RetryBackoffMs, p.MaxMsgsPerMinute, p.DedupWindowSeconds,
		nullable(p.ShadowDestinationID), p.ShadowPercent, p.RequiresApproval,
		p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert pipeline: %w", err)
	}
	return nil
}

func (r *PipelineRepo) Update(ctx context.Context, tenantID string, p *Pipeline) error {
	return r.updateExec(ctx, r.db, tenantID, p)
}

// UpdateTx is the tx-aware variant of Update. Mirrors the contract of
// Update exactly — the only difference is the executor. Used by the
// server-layer applyRevisionLive helper so the pipeline write and the
// child-row replacements all commit (or roll back) together.
func (r *PipelineRepo) UpdateTx(ctx context.Context, tx *txWrap, tenantID string, p *Pipeline) error {
	return r.updateExec(ctx, tx, tenantID, p)
}

// pipelineExec is the narrow set of database/sql verbs the Update path
// needs. Both *dbWrap and *txWrap satisfy it, so updateExec can run
// against either without code duplication.
type pipelineExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (r *PipelineRepo) updateExec(ctx context.Context, exec pipelineExec, tenantID string, p *Pipeline) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	p.UpdatedAt = time.Now().UTC()
	if p.FilterPaths == nil {
		p.FilterPaths = []string{}
	}
	pathsJSON, _ := json.Marshal(p.FilterPaths)
	res, err := exec.ExecContext(ctx, `
		UPDATE pipelines SET name=?, source_id=?, destination_id=?, output_format=?,
		                     schema_id=?, filter_paths=?, enabled=?,
		                     workers=?, retry_max=?, retry_backoff_ms=?,
		                     max_msgs_per_minute=?, dedup_window_seconds=?,
		                     shadow_destination_id=?, shadow_percent=?,
		                     requires_approval=?, updated_at=?
		WHERE id=? AND tenant_id=?`,
		p.Name, p.SourceID, p.DestinationID, p.OutputFormat,
		nullable(p.SchemaID), string(pathsJSON), p.Enabled,
		p.Workers, p.RetryMax, p.RetryBackoffMs, p.MaxMsgsPerMinute, p.DedupWindowSeconds,
		nullable(p.ShadowDestinationID), p.ShadowPercent,
		p.RequiresApproval, p.UpdatedAt, p.ID, tenantID)
	if err != nil {
		return fmt.Errorf("update pipeline: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PipelineRepo) Delete(ctx context.Context, tenantID, id string) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM pipelines WHERE id=? AND tenant_id=?`, id, tenantID)
	if err != nil {
		return fmt.Errorf("delete pipeline: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PipelineRepo) Get(ctx context.Context, tenantID, id string) (*Pipeline, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	row := r.db.QueryRowContext(ctx, pipelineSelect+` WHERE id=? AND tenant_id=?`, id, tenantID)
	return scanPipeline(row)
}

// GetUnsafe reads a pipeline by id only, bypassing tenant scoping.
// Internal subsystems (pipeline.Manager, DLQ retry) only — never HTTP.
func (r *PipelineRepo) GetUnsafe(ctx context.Context, id string) (*Pipeline, error) {
	row := r.db.QueryRowContext(ctx, pipelineSelect+` WHERE id=?`, id)
	return scanPipeline(row)
}

func (r *PipelineRepo) List(ctx context.Context, tenantID string) ([]*Pipeline, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	rows, err := r.db.QueryContext(ctx, pipelineSelect+` WHERE tenant_id=? ORDER BY name`, tenantID)
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

// ListAll lists every pipeline across all tenants. Used by the pipeline
// manager at boot to start workers — the manager is system-level, not
// user-facing. NOT exposed via HTTP.
func (r *PipelineRepo) ListAll(ctx context.Context) ([]*Pipeline, error) {
	rows, err := r.db.QueryContext(ctx, pipelineSelect+` ORDER BY tenant_id, name`)
	if err != nil {
		return nil, fmt.Errorf("list all pipelines: %w", err)
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
SELECT id, tenant_id, name, source_id, destination_id, output_format, COALESCE(schema_id,''),
       filter_paths, enabled, workers, retry_max, retry_backoff_ms, max_msgs_per_minute,
       dedup_window_seconds, COALESCE(shadow_destination_id,''), shadow_percent,
       requires_approval, created_at, updated_at
FROM pipelines`

func scanPipeline(s scanner) (*Pipeline, error) {
	p := &Pipeline{}
	var pathsJSON string
	err := s.Scan(&p.ID, &p.TenantID, &p.Name, &p.SourceID, &p.DestinationID, &p.OutputFormat,
		&p.SchemaID, &pathsJSON, &p.Enabled,
		&p.Workers, &p.RetryMax, &p.RetryBackoffMs, &p.MaxMsgsPerMinute, &p.DedupWindowSeconds,
		&p.ShadowDestinationID, &p.ShadowPercent,
		&p.RequiresApproval, &p.CreatedAt, &p.UpdatedAt)
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
