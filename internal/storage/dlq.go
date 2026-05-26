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
		                 error_reason, retry_count, created_at, next_retry_at,
		                 error_fingerprint, error_template, failing_stage_name, failing_stage_index)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, tenantID, nullable(e.PipelineID), e.SourceQueue, e.OriginalMsg, rawMsg, e.Redacted,
		e.ErrorReason, e.RetryCount, e.CreatedAt, nextRetry,
		e.ErrorFingerprint, e.ErrorTemplate, e.FailingStageName, e.FailingStageIndex)
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

// DeleteByFilter removes every DLQ row matching the same DLQFilter
// shape the List endpoints accept. Returns the number of rows
// removed. Tenant-scoped — a missing tenantID is rejected. Callers
// (the bulk-triage handler) supply the cap explicitly via maxRows;
// SQLite supports DELETE ... LIMIT only with the right build flags
// so we apply the limit via a SELECT-then-DELETE-by-ID pattern.
func (r *DLQRepo) DeleteByFilter(ctx context.Context, tenantID string, f DLQFilter, maxRows int) (int64, error) {
	if tenantID == "" {
		return 0, ErrTenantRequired
	}
	if maxRows <= 0 {
		maxRows = 1000
	}
	ids, err := r.IDsByFilter(ctx, tenantID, f, maxRows)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return r.deleteByIDs(ctx, tenantID, ids)
}

// IDsByFilter returns up to maxRows DLQ ids matching f. Used by the
// bulk-action handlers to materialise the affected set before
// applying retry/delete row-by-row.
func (r *DLQRepo) IDsByFilter(ctx context.Context, tenantID string, f DLQFilter, maxRows int) ([]string, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	if maxRows <= 0 {
		maxRows = 1000
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
	rows, err := r.db.QueryContext(ctx,
		`SELECT id FROM dlq WHERE `+where+` ORDER BY created_at DESC LIMIT ?`,
		append(args, maxRows)...)
	if err != nil {
		return nil, fmt.Errorf("list dlq ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *DLQRepo) deleteByIDs(ctx context.Context, tenantID string, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	// Build a parametrised IN clause. SQLite handles up to 999 params
	// per statement comfortably; the bulk-cap (maxRows) is enforced
	// by the caller well below that ceiling.
	placeholders := make([]byte, 0, len(ids)*2)
	args := make([]any, 0, len(ids)+1)
	args = append(args, tenantID)
	for i, id := range ids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, id)
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM dlq WHERE tenant_id = ? AND id IN (`+string(placeholders)+`)`,
		args...)
	if err != nil {
		return 0, fmt.Errorf("bulk delete: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ErrorGroup is one bucket of the DLQ-by-error-reason aggregation.
// Pattern is the first dlqErrorPatternHead characters of the
// error_reason (sufficient to identify the failure family;
// distinguishes "validate: required field x missing" from
// "send: dial tcp 10.0.0.1:5672: connect: refused" even though both
// share a prefix), Count is the number of rows in that bucket, and
// OldestAt is the earliest created_at among them.
type ErrorGroup struct {
	Pattern  string
	Count    int64
	OldestAt time.Time
}

// dlqErrorPatternHead is how many chars of error_reason we group on.
// Long enough to keep distinct failure modes separate, short enough
// to collapse "field 'x'" vs "field 'y'" into one bucket.
const dlqErrorPatternHead = 80

// GroupByError aggregates DLQ rows by error_reason prefix for the
// triage UI. Returns the top `limit` buckets, sorted by count desc.
// Tenant-scoped.
func (r *DLQRepo) GroupByError(ctx context.Context, tenantID string, limit int) ([]ErrorGroup, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT SUBSTR(error_reason, 1, ?) AS pattern,
		       COUNT(1) AS cnt,
		       MIN(created_at) AS oldest
		FROM dlq
		WHERE tenant_id = ?
		GROUP BY pattern
		ORDER BY cnt DESC
		LIMIT ?`, dlqErrorPatternHead, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("dlq group by error: %w", err)
	}
	defer rows.Close()
	var out []ErrorGroup
	for rows.Next() {
		var g ErrorGroup
		// SQLite returns aggregate timestamps as TEXT; pgx returns
		// time.Time. Scan via sql.NullString to accept both, then
		// parse on the SQLite path.
		var oldestRaw sql.NullString
		var oldestT sql.NullTime
		if err := rows.Scan(&g.Pattern, &g.Count, &oldestRaw); err == nil {
			if oldestRaw.Valid {
				if t, perr := ParseSQLiteTimestamp(oldestRaw.String); perr == nil {
					g.OldestAt = t
				}
			}
		} else if err2 := rows.Scan(&g.Pattern, &g.Count, &oldestT); err2 != nil {
			return nil, err
		} else if oldestT.Valid {
			g.OldestAt = oldestT.Time
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ParseSQLiteTimestamp accepts the handful of formats SQLite emits
// when an aggregate (MIN, MAX) is applied to a DATETIME column.
// pgx returns time.Time directly so this is only invoked on the
// SQLite path. Exported so other packages (e.g. internal/server's
// DLQ-cluster scan) can reuse the same tolerant parser.
func ParseSQLiteTimestamp(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05Z07:00",
		// SQLite via modernc.org/sqlite emits the Go time.String() form
		// on aggregates: "2026-05-25 21:11:08.271009 +0000 UTC". The
		// numeric offset is what we actually want; the trailing zone
		// name is a Go-stringer artefact.
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("dlq: unparseable timestamp %q", s)
}

// DLQClusterFilter narrows ListClusters. Empty zero values mean "any
// (within the tenant)". The PipelineID exact-match and Since lower-
// bound mirror the DLQFilter shape used by the row-level list.
type DLQClusterFilter struct {
	PipelineID string
	Since      *time.Time
	MinCount   int
	Limit      int
}

// DLQClusterRow is one fingerprint-bucket row from ListClusters. The
// follow-up details (pipelines affected, failing stages, recent ids)
// are queried per-cluster via separate small queries — see the HTTP
// handler that consumes this for the rationale.
type DLQClusterRow struct {
	Fingerprint string
	Template    string
	Count       int
	FirstSeen   time.Time
	LastSeen    time.Time
}

// ListClusters groups DLQ rows by error_fingerprint + error_template
// and returns the top-N clusters (sorted by count desc) for the named
// tenant. Empty-fingerprint rows (legacy data + send-side attribution
// gaps) are excluded — the HTTP layer surfaces them via the regular
// list endpoint.
func (r *DLQRepo) ListClusters(ctx context.Context, tenantID string, f DLQClusterFilter) ([]DLQClusterRow, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	if f.MinCount < 1 {
		f.MinCount = 1
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	where := "tenant_id = ? AND error_fingerprint != ''"
	args := []any{tenantID}
	if f.PipelineID != "" {
		where += " AND pipeline_id = ?"
		args = append(args, f.PipelineID)
	}
	if f.Since != nil {
		where += " AND created_at >= ?"
		args = append(args, *f.Since)
	}
	args = append(args, f.MinCount, f.Limit)
	rows, err := r.db.QueryContext(ctx, `
		SELECT error_fingerprint, error_template,
		       COUNT(*) AS cnt,
		       MIN(created_at) AS first_seen,
		       MAX(created_at) AS last_seen
		FROM dlq
		WHERE `+where+`
		GROUP BY error_fingerprint, error_template
		HAVING COUNT(*) >= ?
		ORDER BY cnt DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list dlq clusters: %w", err)
	}
	defer rows.Close()
	out := make([]DLQClusterRow, 0, 8)
	for rows.Next() {
		c, err := scanDLQClusterRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// scanDLQClusterRow reads one GROUP BY row. SQLite returns aggregate
// timestamps as TEXT and rejects scans into sql.NullTime; pgx returns
// timestamps as time.Time and rejects scans into sql.NullString. We
// use a tolerant interface{} scan and post-process: if the value is
// already a time.Time (pgx), use it; if it's a string (SQLite),
// parse via ParseSQLiteTimestamp.
func scanDLQClusterRow(rows *sql.Rows) (DLQClusterRow, error) {
	var c DLQClusterRow
	var firstRaw, lastRaw any
	if err := rows.Scan(&c.Fingerprint, &c.Template, &c.Count, &firstRaw, &lastRaw); err != nil {
		return c, fmt.Errorf("scan dlq cluster: %w", err)
	}
	c.FirstSeen = coerceTimestamp(firstRaw)
	c.LastSeen = coerceTimestamp(lastRaw)
	return c, nil
}

// coerceTimestamp accepts whatever the driver hands back for an
// aggregate timestamp column (string from SQLite, time.Time from pgx,
// []byte from some mariadb-flavoured drivers) and returns a UTC
// time.Time. Returns the zero value on nil or unparseable input —
// the caller treats zero-time as "no rows yet" rather than failing
// the whole rollup.
func coerceTimestamp(v any) time.Time {
	switch x := v.(type) {
	case nil:
		return time.Time{}
	case time.Time:
		return x.UTC()
	case string:
		if t, err := ParseSQLiteTimestamp(x); err == nil {
			return t
		}
	case []byte:
		if t, err := ParseSQLiteTimestamp(string(x)); err == nil {
			return t
		}
	}
	return time.Time{}
}

// RecentForPipeline returns up to `limit` DLQ rows whose
// pipeline_id matches, newest-first. When `since` is non-zero
// it acts as a lower bound on created_at. Tenant-scoped — a
// missing tenantID is rejected with ErrTenantRequired.
//
// Used by the internal/explain package's CircuitExplainer +
// DriftExplainer + DLQRootCauseExplainer to sample representative
// failure rows alongside the rollup data. Kept as its own helper
// (rather than re-using ListFiltered) so the narrow read path
// doesn't pull the page-count aggregate and so the SQL stays
// fingerprint-friendly via the dlq_pipeline_id_created_at
// composite index.
func (r *DLQRepo) RecentForPipeline(ctx context.Context, tenantID, pipelineID string, limit int, since time.Time) ([]*DLQEntry, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	if pipelineID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 25
	}
	where := "tenant_id = ? AND pipeline_id = ?"
	args := []any{tenantID, pipelineID}
	if !since.IsZero() {
		where += " AND created_at >= ?"
		args = append(args, since.UTC())
	}
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx,
		dlqSelect+` WHERE `+where+` ORDER BY created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("dlq recent for pipeline: %w", err)
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

// ClusterByFingerprint returns the cluster rollup for a single
// fingerprint within the tenant. ErrNotFound when no rows share
// the fingerprint. Counterpart to ListClusters that targets one
// known fingerprint — used by the explain package's
// DLQRootCauseExplainer for the "look up the cluster for this
// entry" path.
func (r *DLQRepo) ClusterByFingerprint(ctx context.Context, tenantID, fingerprint string) (DLQClusterRow, error) {
	if tenantID == "" {
		return DLQClusterRow{}, ErrTenantRequired
	}
	if fingerprint == "" {
		return DLQClusterRow{}, ErrNotFound
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT error_fingerprint, error_template,
		       COUNT(*) AS cnt,
		       MIN(created_at) AS first_seen,
		       MAX(created_at) AS last_seen
		FROM dlq
		WHERE tenant_id = ? AND error_fingerprint = ?
		GROUP BY error_fingerprint, error_template`, tenantID, fingerprint)
	var c DLQClusterRow
	var firstRaw, lastRaw any
	if err := row.Scan(&c.Fingerprint, &c.Template, &c.Count, &firstRaw, &lastRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DLQClusterRow{}, ErrNotFound
		}
		return DLQClusterRow{}, fmt.Errorf("cluster by fingerprint: %w", err)
	}
	c.FirstSeen = coerceTimestamp(firstRaw)
	c.LastSeen = coerceTimestamp(lastRaw)
	return c, nil
}

// ClusterPipelines returns the distinct, alpha-sorted, non-empty
// pipeline_ids for one fingerprint bucket. Used by the cluster detail
// fan-out — empty pipeline_id (bridge endpoint failures) is dropped.
func (r *DLQRepo) ClusterPipelines(ctx context.Context, tenantID, fingerprint string) ([]string, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	return r.queryStrings(ctx,
		`SELECT DISTINCT COALESCE(pipeline_id, '') FROM dlq
		 WHERE tenant_id = ? AND error_fingerprint = ?
		 ORDER BY pipeline_id`,
		tenantID, fingerprint)
}

// ClusterFailingStages returns the distinct, alpha-sorted failing
// stage names for one fingerprint bucket. Empty stage names (send-
// side failures with no stage attribution) are excluded.
func (r *DLQRepo) ClusterFailingStages(ctx context.Context, tenantID, fingerprint string) ([]string, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	return r.queryStrings(ctx,
		`SELECT DISTINCT failing_stage_name FROM dlq
		 WHERE tenant_id = ? AND error_fingerprint = ? AND failing_stage_name != ''
		 ORDER BY failing_stage_name`,
		tenantID, fingerprint)
}

// ClusterRecentIDs returns the N most-recent entry ids in a cluster,
// newest-first. Used by the detail panel's "show the latest examples"
// affordance.
func (r *DLQRepo) ClusterRecentIDs(ctx context.Context, tenantID, fingerprint string, limit int) ([]string, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	if limit <= 0 {
		limit = 5
	}
	return r.queryStrings(ctx,
		`SELECT id FROM dlq
		 WHERE tenant_id = ? AND error_fingerprint = ?
		 ORDER BY created_at DESC LIMIT ?`,
		tenantID, fingerprint, limit)
}

// ClusterRepresentative returns the id of the oldest entry in a
// cluster — operators usually want the original symptom for triage.
// Returns ErrNotFound when no row matches (defensive; the caller
// usually only invokes this after ListClusters returned the cluster).
func (r *DLQRepo) ClusterRepresentative(ctx context.Context, tenantID, fingerprint string) (string, error) {
	if tenantID == "" {
		return "", ErrTenantRequired
	}
	row := r.db.QueryRowContext(ctx,
		`SELECT id FROM dlq
		 WHERE tenant_id = ? AND error_fingerprint = ?
		 ORDER BY created_at ASC LIMIT 1`,
		tenantID, fingerprint)
	var id string
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return id, nil
}

// queryStrings runs a one-column-string query and collects the
// results. Used by the cluster-detail helpers — keeping it in one
// place avoids three near-identical 20-line stanzas.
func (r *DLQRepo) queryStrings(ctx context.Context, sqlStr string, args ...any) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("dlq cluster detail query: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0, 4)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
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

// CountByPipeline returns one (pipelineID → row count) entry per
// pipeline in the named tenant that currently has any DLQ rows.
// Pipelines with zero rows are omitted; callers expecting full
// coverage default missing keys to 0. Tenant-scoped — used by the
// /api/v1/topology aggregator to attach a depth column to every
// pipeline in the live snapshot. One grouped scan, no application-
// side aggregation.
func (r *DLQRepo) CountByPipeline(ctx context.Context, tenantID string) (map[string]int64, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT COALESCE(pipeline_id, ''), COUNT(1)
		FROM dlq
		WHERE tenant_id = ?
		GROUP BY pipeline_id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("dlq count by pipeline: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var pid string
		var n int64
		if err := rows.Scan(&pid, &n); err != nil {
			return nil, fmt.Errorf("dlq count scan: %w", err)
		}
		if pid == "" {
			continue
		}
		out[pid] = n
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
       error_reason, retry_count, last_retry_at, next_retry_at, created_at,
       error_fingerprint, error_template, failing_stage_name, failing_stage_index
FROM dlq`

func scanDLQ(s scanner) (*DLQEntry, error) {
	e := &DLQEntry{}
	var lastRetry, nextRetry sql.NullTime
	err := s.Scan(&e.ID, &e.TenantID, &e.PipelineID, &e.SourceQueue, &e.OriginalMsg,
		&e.RawMsg, &e.Redacted,
		&e.ErrorReason, &e.RetryCount, &lastRetry, &nextRetry, &e.CreatedAt,
		&e.ErrorFingerprint, &e.ErrorTemplate, &e.FailingStageName, &e.FailingStageIndex)
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
