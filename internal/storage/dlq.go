package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type DLQRepo struct{ db *sql.DB }

// Insert writes a DLQ entry. tenantID is required — DLQ entries always
// originate from a tenant-owned pipeline (or carry the default tenant
// when the pipeline_id is empty, e.g. for bridge endpoint failures).
func (r *DLQRepo) Insert(ctx context.Context, tenantID string, e *DLQEntry) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	e.TenantID = tenantID
	e.CreatedAt = time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO dlq (id, tenant_id, pipeline_id, source_queue, original_msg, error_reason,
		                 retry_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, tenantID, nullable(e.PipelineID), e.SourceQueue, e.OriginalMsg, e.ErrorReason,
		e.RetryCount, e.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert dlq: %w", err)
	}
	return nil
}

func (r *DLQRepo) Get(ctx context.Context, tenantID, id string) (*DLQEntry, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	row := r.db.QueryRowContext(ctx, dlqSelect+` WHERE id=? AND tenant_id=?`, id, tenantID)
	return scanDLQ(row)
}

// GetUnsafe — internal callers (DLQ retry executor) that already have a
// tenant-scoped id.
func (r *DLQRepo) GetUnsafe(ctx context.Context, id string) (*DLQEntry, error) {
	row := r.db.QueryRowContext(ctx, dlqSelect+` WHERE id=?`, id)
	return scanDLQ(row)
}

// DLQFilter narrows a List query. Zero-valued fields mean "any" (within
// the tenant the caller is asking about).
//   - PipelineID: exact match.
//   - Error: case-insensitive substring match against error_reason.
//   - Since / Until: half-open time window over created_at.
type DLQFilter struct {
	PipelineID string
	Error      string
	Since      *time.Time
	Until      *time.Time
}

// List returns DLQ entries for the named tenant, newest-first.
func (r *DLQRepo) List(ctx context.Context, tenantID string, page, perPage int) ([]*DLQEntry, int, error) {
	return r.ListFiltered(ctx, tenantID, DLQFilter{}, page, perPage)
}

func (r *DLQRepo) ListFiltered(ctx context.Context, tenantID string, f DLQFilter, page, perPage int) ([]*DLQEntry, int, error) {
	if tenantID == "" {
		return nil, 0, ErrTenantRequired
	}
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 200 {
		perPage = 20
	}

	where := "tenant_id = ?"
	args := []any{tenantID}
	if f.PipelineID != "" {
		where += " AND pipeline_id = ?"
		args = append(args, f.PipelineID)
	}
	if f.Error != "" {
		where += " AND LOWER(error_reason) LIKE LOWER(?)"
		args = append(args, "%"+f.Error+"%")
	}
	if f.Since != nil {
		where += " AND created_at >= ?"
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		where += " AND created_at < ?"
		args = append(args, *f.Until)
	}

	var total int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dlq WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count dlq: %w", err)
	}

	offset := (page - 1) * perPage
	rows, err := r.db.QueryContext(ctx,
		dlqSelect+` WHERE `+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		append(args, perPage, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list dlq: %w", err)
	}
	defer rows.Close()

	var out []*DLQEntry
	for rows.Next() {
		e, err := scanDLQ(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

func (r *DLQRepo) Delete(ctx context.Context, tenantID, id string) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM dlq WHERE id=? AND tenant_id=?`, id, tenantID)
	if err != nil {
		return fmt.Errorf("delete dlq: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneOlderThan deletes DLQ rows older than cutoff. Returns the count
// removed. Runs across every tenant — retention is a system-level job
// configured globally, not per-tenant.
func (r *DLQRepo) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, nil
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM dlq WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune by age: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (r *DLQRepo) PruneToMaxRows(ctx context.Context, maxRows int) (int64, error) {
	if maxRows <= 0 {
		return 0, nil
	}
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM dlq
		WHERE id IN (
			SELECT id FROM dlq
			ORDER BY created_at DESC
			LIMIT -1 OFFSET ?
		)`, maxRows)
	if err != nil {
		return 0, fmt.Errorf("prune by count: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (r *DLQRepo) IncrementRetry(ctx context.Context, tenantID, id string) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx,
		`UPDATE dlq SET retry_count = retry_count + 1, last_retry_at = ? WHERE id=? AND tenant_id=?`,
		now, id, tenantID)
	if err != nil {
		return fmt.Errorf("increment retry: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const dlqSelect = `
SELECT id, tenant_id, COALESCE(pipeline_id, ''), source_queue, original_msg, error_reason,
       retry_count, last_retry_at, created_at
FROM dlq`

func scanDLQ(s scanner) (*DLQEntry, error) {
	e := &DLQEntry{}
	var lastRetry sql.NullTime
	err := s.Scan(&e.ID, &e.TenantID, &e.PipelineID, &e.SourceQueue, &e.OriginalMsg,
		&e.ErrorReason, &e.RetryCount, &lastRetry, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if lastRetry.Valid {
		t := lastRetry.Time
		e.LastRetryAt = &t
	}
	return e, nil
}
