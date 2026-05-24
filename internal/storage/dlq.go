package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type DLQRepo struct{ db *dbWrap }

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
	var nextRetry any
	if e.NextRetryAt != nil {
		nextRetry = e.NextRetryAt.UTC()
	}
	// RawMsg is left as nil when no redaction was applied — the column
	// has a NOT NULL constraint and DEFAULT '' so the driver coerces
	// nil into an empty BLOB / bytea cleanly.
	rawMsg := e.RawMsg
	if rawMsg == nil {
		rawMsg = []byte{}
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO dlq (id, tenant_id, pipeline_id, source_queue, original_msg, raw_msg, redacted,
		                 error_reason, retry_count, created_at, next_retry_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, tenantID, nullable(e.PipelineID), e.SourceQueue, e.OriginalMsg, rawMsg, e.Redacted,
		e.ErrorReason, e.RetryCount, e.CreatedAt, nextRetry)
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

// DLQStat is one row of the per-pipeline DLQ aggregate. OldestAt is
// the timestamp of the longest-resident message for that pipeline —
// alerting on its age catches a stuck retry loop or a broken
// destination broker that the size metric alone would miss.
type DLQStat struct {
	PipelineID string
	Count      int64
	OldestAt   time.Time
}

// Stats returns one DLQStat per pipeline that currently has any DLQ
// rows. Empty pipelines are omitted — the metrics renderer surfaces
// only what's actually backed up. Cost is one indexed scan; the
// query groups in-place with no application-side aggregation.
func (r *DLQRepo) Stats(ctx context.Context) ([]DLQStat, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT pipeline_id, COUNT(1), MIN(created_at)
		FROM dlq
		GROUP BY pipeline_id`)
	if err != nil {
		return nil, fmt.Errorf("dlq stats: %w", err)
	}
	defer rows.Close()
	var out []DLQStat
	for rows.Next() {
		var s DLQStat
		if err := rows.Scan(&s.PipelineID, &s.Count, &s.OldestAt); err != nil {
			return nil, fmt.Errorf("dlq stats scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
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

// ListDue returns DLQ entries whose next_retry_at <= now, capped at
// `limit`. The reaper walks this list, attempts to re-publish, and on
// retry-cap exhaustion clears next_retry_at (so the row sits awaiting
// manual triage). Bypasses tenant scoping — the reaper is a global
// service.
func (r *DLQRepo) ListDue(ctx context.Context, now time.Time, limit int) ([]*DLQEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx,
		dlqSelect+` WHERE next_retry_at IS NOT NULL AND next_retry_at <= ?
		            ORDER BY next_retry_at ASC LIMIT ?`,
		now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list due dlq: %w", err)
	}
	defer rows.Close()
	var out []*DLQEntry
	for rows.Next() {
		e, err := scanDLQ(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ScheduleRetry sets next_retry_at on a DLQ row. Nil clears the
// schedule — used when the row exhausts its retry budget or a manual
// retry succeeds.
func (r *DLQRepo) ScheduleRetry(ctx context.Context, tenantID, id string, next *time.Time) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	var arg any
	if next != nil {
		arg = next.UTC()
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE dlq SET next_retry_at=? WHERE id=? AND tenant_id=?`,
		arg, id, tenantID)
	if err != nil {
		return fmt.Errorf("schedule retry: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const dlqSelect = `
SELECT id, tenant_id, COALESCE(pipeline_id, ''), source_queue, original_msg, raw_msg, redacted,
       error_reason, retry_count, last_retry_at, next_retry_at, created_at
FROM dlq`

func scanDLQ(s scanner) (*DLQEntry, error) {
	e := &DLQEntry{}
	var lastRetry, nextRetry sql.NullTime
	err := s.Scan(&e.ID, &e.TenantID, &e.PipelineID, &e.SourceQueue, &e.OriginalMsg,
		&e.RawMsg, &e.Redacted,
		&e.ErrorReason, &e.RetryCount, &lastRetry, &nextRetry, &e.CreatedAt)
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
	if nextRetry.Valid {
		t := nextRetry.Time
		e.NextRetryAt = &t
	}
	return e, nil
}
