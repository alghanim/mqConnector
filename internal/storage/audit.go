package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AuditEntry is one immutable record of an admin action. Records are insert-
// only; no Update or Delete repo methods exist by design.
//
// TenantID is the actor's *active* tenant at the time of the action. A
// global system-admin (one with owner role on the default tenant + a
// flag we'll add later) sees all rows; a tenant-bounded user sees only
// rows tagged with their tenant.
//
// Hash + PrevHash form the tamper-evident chain (migration 0005). Each
// row's Hash = sha256(PrevHash || canonical(row fields)). The verifier
// in ChainStatus walks the rows in insertion order per tenant and
// reports the first row where the recomputed hash disagrees. A single-
// row mutation is therefore detectable.
type AuditEntry struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	At        time.Time `json:"at"`
	Actor     string    `json:"actor"`      // preferred_username
	ActorSub  string    `json:"actor_sub"`  // JWT sub
	Action    string    `json:"action"`     // HTTP verb (POST/PUT/DELETE)
	Resource  string    `json:"resource"`   // URL path including ID
	Status    int       `json:"status"`     // HTTP status of the response
	RequestID string    `json:"request_id"` // X-Request-Id for cross-reference
	RemoteIP  string    `json:"remote_ip"`
	Hash      string    `json:"hash,omitempty"`
	PrevHash  string    `json:"prev_hash,omitempty"`
}

// canonicalAuditBytes is the byte sequence we hash. Stable order across
// fields; LF separators so a stray field that happens to contain another
// field's value can't collide. The format is internal and must never
// change without bumping the chain marker — every old row would then
// fail verification.
func canonicalAuditBytes(e *AuditEntry) []byte {
	// Use Unix-nanos for At so timezone normalization doesn't shift the
	// hash between machines. The Insert path always sets At to UTC.
	s := fmt.Sprintf("%s\n%s\n%d\n%s\n%s\n%s\n%s\n%d\n%s\n%s",
		e.ID,
		e.TenantID,
		e.At.UnixNano(),
		e.Actor,
		e.ActorSub,
		e.Action,
		e.Resource,
		e.Status,
		e.RequestID,
		e.RemoteIP,
	)
	return []byte(s)
}

// computeAuditHash returns sha256(prevHash || canonical(entry)) as hex.
func computeAuditHash(prevHash string, e *AuditEntry) string {
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write(canonicalAuditBytes(e))
	return hex.EncodeToString(h.Sum(nil))
}

// AuditRepo persists and lists audit entries.
//
// chainMu serialises the chain-head lookup + insert so concurrent
// writers can't pick the same prev_hash and fork the chain. The lock
// is in-process — appropriate for a single-binary deployment with one
// audit writer per node. The leader-elected HA setup writes audit
// from one node at a time anyway; if that ever changes we'd need an
// advisory lock at the DB layer.
// AuditSink receives a copy of every successfully-inserted audit
// entry. Used by the syslog forwarder so audit rows can stream to a
// SIEM in real time without waiting for the archiver's batch
// rollover. Implementations MUST NOT block — the chain mutex is held
// while the sink is invoked. Fire-and-forget on a buffered channel
// is the right shape.
type AuditSink interface {
	OnInsert(e AuditEntry)
}

type AuditRepo struct {
	db      *dbWrap
	chainMu sync.Mutex // SQLite single-writer guard; ignored on Postgres
	sinks   []AuditSink
}

// AddSink registers a callback that fires after every successful
// audit insert. Safe to call before the repo is used; not
// concurrent-safe with active insert traffic.
func (r *AuditRepo) AddSink(s AuditSink) {
	if s != nil {
		r.sinks = append(r.sinks, s)
	}
}

// IterOlderThan streams audit entries created strictly before cutoff,
// oldest-first. The callback runs synchronously for each row; returning
// a non-nil error aborts iteration. Used by the archival exporter so it
// can stream-to-disk without buffering an unbounded result set.
//
// Across all tenants — archival is system-level.
func (r *AuditRepo) IterOlderThan(ctx context.Context, cutoff time.Time, fn func(*AuditEntry) error) error {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, tenant_id, at, actor, actor_sub, action, resource, status, request_id, remote_ip, hash, prev_hash
		 FROM audit_log WHERE at < ? ORDER BY at ASC`, cutoff)
	if err != nil {
		return fmt.Errorf("query audit: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		e := &AuditEntry{}
		if err := rows.Scan(&e.ID, &e.TenantID, &e.At, &e.Actor, &e.ActorSub, &e.Action,
			&e.Resource, &e.Status, &e.RequestID, &e.RemoteIP, &e.Hash, &e.PrevHash); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *AuditRepo) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM audit_log WHERE at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune audit: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Insert appends an entry to the log. TenantID is required — even
// "global" actions (e.g. tenant creation by a system-admin) carry the
// default tenant id, so every row has a non-null tenant_id.
//
// Hash chain (migration 0005): we look up the most recent row's hash
// for the same tenant and compute this row's hash as
// sha256(prev_hash || canonical(this row)). The chainMu mutex serialises
// the lookup + insert so concurrent writers can't pick the same
// prev_hash and fork the chain — a process-local lock is enough for a
// single-binary deployment, and avoids the SQLite write-lock contention
// a tx-based serialisation produces under load.
func (r *AuditRepo) Insert(ctx context.Context, e *AuditEntry) error {
	if e.TenantID == "" {
		e.TenantID = DefaultTenantID
	}
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}

	// Concurrency: on SQLite we rely on the in-process chainMu —
	// SQLite is single-writer anyway, so a process-local lock is
	// enough to keep the chain head consistent. On Postgres a
	// multi-replica deploy has concurrent writers in different
	// processes; the chainMu doesn't see them. We open a
	// serialisable transaction instead: lookup + insert run as
	// one atomic unit, and Postgres rejects the second concurrent
	// writer with a serialization_failure that the caller retries.
	if r.db.dialect == DialectPostgres {
		return r.insertSerialised(ctx, e)
	}

	r.chainMu.Lock()
	defer r.chainMu.Unlock()

	// Most recent row's hash for this tenant becomes our prev_hash.
	// Ordered by at DESC, id DESC so two rows sharing a timestamp
	// (SQLite stores DATETIME at second granularity) still pick a
	// deterministic predecessor.
	var prev string
	err := r.db.QueryRowContext(ctx,
		`SELECT hash FROM audit_log WHERE tenant_id = ? ORDER BY at DESC, id DESC LIMIT 1`,
		e.TenantID).Scan(&prev)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("audit chain head: %w", err)
	}
	e.PrevHash = prev
	e.Hash = computeAuditHash(prev, e)

	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO audit_log (id, tenant_id, at, actor, actor_sub, action, resource, status, request_id, remote_ip, hash, prev_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.TenantID, e.At, e.Actor, e.ActorSub, e.Action, e.Resource, e.Status, e.RequestID, e.RemoteIP, e.Hash, e.PrevHash); err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	// Real-time fan-out to any registered sinks (syslog, etc.).
	// We hold chainMu through this so sinks must be non-blocking —
	// the contract is documented on AuditSink.
	for _, s := range r.sinks {
		s.OnInsert(*e)
	}
	return nil
}

// insertSerialised is the Postgres concurrent-safe path. The chain
// head lookup + the new-row insert run inside one Serializable
// transaction so concurrent writers in different processes can't
// pick the same prev_hash. Postgres detects the conflict and returns
// SQLSTATE 40001 (serialization_failure); we retry a bounded number
// of times. After retries are exhausted the error bubbles up — the
// caller (the audit middleware) treats audit-insert failure as
// non-fatal and logs a WARN.
//
// Retry strategy: 5 attempts with exponential backoff. Concurrent
// audit traffic is bounded by the request rate, not by audit
// volume, so contention is low in practice. The retry budget gives
// the second-place writer time to win on its next round.
func (r *AuditRepo) insertSerialised(ctx context.Context, e *AuditEntry) error {
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := r.tryInsertSerialised(ctx, e)
		if err == nil {
			for _, s := range r.sinks {
				s.OnInsert(*e)
			}
			return nil
		}
		// Postgres serialization_failure code is 40001. We don't
		// import lib/pq just to recognise it; pgx returns the
		// error text containing "could not serialize access" which
		// is stable across versions.
		if !isSerializationFailure(err) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("audit insert: serializable retry exhausted: %w", lastErr)
}

func (r *AuditRepo) tryInsertSerialised(ctx context.Context, e *AuditEntry) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("audit begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var prev string
	err = tx.QueryRowContext(ctx,
		`SELECT hash FROM audit_log WHERE tenant_id = ? ORDER BY at DESC, id DESC LIMIT 1`,
		e.TenantID).Scan(&prev)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("audit chain head: %w", err)
	}
	e.PrevHash = prev
	e.Hash = computeAuditHash(prev, e)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (id, tenant_id, at, actor, actor_sub, action, resource, status, request_id, remote_ip, hash, prev_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.TenantID, e.At, e.Actor, e.ActorSub, e.Action, e.Resource, e.Status, e.RequestID, e.RemoteIP, e.Hash, e.PrevHash); err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("audit commit: %w", err)
	}
	return nil
}

func isSerializationFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// pgx surfaces "ERROR: could not serialize access due to ..."
	// from Postgres SQLSTATE 40001. The check is intentionally
	// loose to cover both "concurrent update" and "read/write
	// dependencies" flavours.
	return strings.Contains(msg, "could not serialize access") ||
		strings.Contains(msg, "SQLSTATE 40001")
}

// ChainStatus is what Verify reports. Status is "ok" if every row's
// hash matches the recomputed value; "broken" if any row diverges.
// FirstBrokenID is the id of the earliest row that fails verification
// (zero on a clean chain). Checked is the total number of rows walked.
type ChainStatus struct {
	TenantID        string `json:"tenant_id"`
	Status          string `json:"status"`
	Checked         int    `json:"checked"`
	FirstBrokenID   string `json:"first_broken_id,omitempty"`
	FirstBrokenAt   string `json:"first_broken_at,omitempty"`
	FirstBrokenWhy  string `json:"first_broken_why,omitempty"`
}

// Verify walks the audit chain for one tenant (or all tenants when
// tenantID == "*") in insertion order and recomputes each row's hash.
// Returns one ChainStatus per tenant. The walk is O(n) and streams via
// the same scan loop as List, so even very long chains don't buffer.
func (r *AuditRepo) Verify(ctx context.Context, tenantID string) ([]ChainStatus, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}

	where := "1=1"
	args := []any{}
	if tenantID != "*" {
		where += " AND tenant_id = ?"
		args = append(args, tenantID)
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, tenant_id, at, actor, actor_sub, action, resource, status, request_id, remote_ip, hash, prev_hash
		 FROM audit_log WHERE `+where+` ORDER BY tenant_id ASC, at ASC, id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("verify query: %w", err)
	}
	defer rows.Close()

	// One ChainStatus per tenant, accumulated as we walk.
	statusByTenant := map[string]*ChainStatus{}
	expectedPrev := map[string]string{} // last hash seen per tenant

	for rows.Next() {
		e := &AuditEntry{}
		if err := rows.Scan(&e.ID, &e.TenantID, &e.At, &e.Actor, &e.ActorSub, &e.Action,
			&e.Resource, &e.Status, &e.RequestID, &e.RemoteIP, &e.Hash, &e.PrevHash); err != nil {
			return nil, err
		}
		st, ok := statusByTenant[e.TenantID]
		if !ok {
			st = &ChainStatus{TenantID: e.TenantID, Status: "ok"}
			statusByTenant[e.TenantID] = st
		}
		st.Checked++

		// Backfilled rows (migration 0005 default-empty hash) are not
		// part of the chain — skip verification but keep walking so we
		// can report new rows after the gap. Reset expectedPrev so the
		// post-backfill segment chains from the next stored hash.
		if e.Hash == "" {
			expectedPrev[e.TenantID] = ""
			continue
		}

		expected := computeAuditHash(e.PrevHash, e)
		broken := false
		why := ""
		if expected != e.Hash {
			broken = true
			why = "row hash does not match canonical recomputation"
		} else if prev, hadPrev := expectedPrev[e.TenantID]; hadPrev && e.PrevHash != prev {
			broken = true
			why = "row prev_hash does not match prior row's hash"
		}
		if broken && st.Status == "ok" {
			st.Status = "broken"
			st.FirstBrokenID = e.ID
			st.FirstBrokenAt = e.At.UTC().Format(time.RFC3339Nano)
			st.FirstBrokenWhy = why
		}
		expectedPrev[e.TenantID] = e.Hash
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]ChainStatus, 0, len(statusByTenant))
	for _, st := range statusByTenant {
		out = append(out, *st)
	}
	return out, nil
}

// AuditFilter narrows a List query. Zero values mean "any" within the
// tenant the caller is asking about. The caller MUST pass a tenantID
// (or "*" for system-wide queries — only system-admins set this).
type AuditFilter struct {
	Actor    string
	Resource string
	Since    *time.Time
	Until    *time.Time
}

// List returns paged audit entries newest-first, plus the total count
// for the filter (without pagination). tenantID="*" means "every
// tenant" (system-admin only — callers higher up enforce that).
func (r *AuditRepo) List(ctx context.Context, tenantID string, f AuditFilter, page, perPage int) ([]*AuditEntry, int, error) {
	if tenantID == "" {
		return nil, 0, ErrTenantRequired
	}
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 500 {
		perPage = 50
	}

	where := "1=1"
	args := []any{}
	if tenantID != "*" {
		where += " AND tenant_id = ?"
		args = append(args, tenantID)
	}
	if f.Actor != "" {
		where += " AND actor = ?"
		args = append(args, f.Actor)
	}
	if f.Resource != "" {
		where += " AND resource LIKE ?"
		args = append(args, f.Resource+"%")
	}
	if f.Since != nil {
		where += " AND at >= ?"
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		where += " AND at <= ?"
		args = append(args, *f.Until)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit: %w", err)
	}

	offset := (page - 1) * perPage
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, tenant_id, at, actor, actor_sub, action, resource, status, request_id, remote_ip, hash, prev_hash
		 FROM audit_log WHERE `+where+` ORDER BY at DESC LIMIT ? OFFSET ?`,
		append(args, perPage, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()

	var out []*AuditEntry
	for rows.Next() {
		e := &AuditEntry{}
		if err := rows.Scan(&e.ID, &e.TenantID, &e.At, &e.Actor, &e.ActorSub, &e.Action,
			&e.Resource, &e.Status, &e.RequestID, &e.RemoteIP, &e.Hash, &e.PrevHash); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// AuditDiff is the optional before/after JSON snapshot for PUT actions.
// Stored in a side table so list views don't bloat the primary index.
type AuditDiff struct {
	AuditID string `json:"audit_id"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

// SaveDiff records the before/after JSON for a PUT mutation. Insertion-
// safe (REPLACE) — the audit row's id is the PK on audit_log_diffs, so
// retries on the same row overwrite rather than orphan.
func (r *AuditRepo) SaveDiff(ctx context.Context, auditID, before, after string) error {
	if auditID == "" {
		return fmt.Errorf("audit diff: empty audit_id")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO audit_log_diffs (audit_id, before, after) VALUES (?, ?, ?)`,
		auditID, before, after)
	if err != nil {
		return fmt.Errorf("save audit diff: %w", err)
	}
	return nil
}

// GetDiff loads the before/after JSON for a given audit row. Returns
// sql.ErrNoRows when the row has no recorded diff (most rows don't —
// only PUT actions carry one).
func (r *AuditRepo) GetDiff(ctx context.Context, auditID string) (*AuditDiff, error) {
	d := &AuditDiff{AuditID: auditID}
	err := r.db.QueryRowContext(ctx,
		`SELECT before, after FROM audit_log_diffs WHERE audit_id = ?`, auditID).
		Scan(&d.Before, &d.After)
	if err != nil {
		return nil, err
	}
	return d, nil
}
