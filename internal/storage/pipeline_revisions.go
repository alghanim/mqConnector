package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// PipelineRevisionRepo persists append-only snapshots of a pipeline's
// full configuration. See the PipelineRevision doc comment for the
// save / deploy split this enables.
//
// nextRevMu is a per-pipeline lock used to serialise the read-MAX +
// insert pair so two concurrent Create calls on the same pipeline
// can't both pick the same revision_number. SQLite already serialises
// writes process-wide, but the lock keeps the contract honest on
// Postgres (where row-level locks are the norm) and makes the
// ordering legible from Go code rather than relying on dialect-
// specific behaviour. mqConnector is a single-binary single-process
// app, so an in-process mutex is sufficient.
type PipelineRevisionRepo struct {
	db         *dbWrap
	nextRevMu  sync.Mutex
	pipelineMu map[string]*sync.Mutex
}

// pipelineLock returns the per-pipeline mutex, lazy-creating one on
// first reference. Held only across the read-MAX + insert pair —
// other operations (List, Get, MarkDeployed) don't touch it.
func (r *PipelineRevisionRepo) pipelineLock(pipelineID string) *sync.Mutex {
	r.nextRevMu.Lock()
	defer r.nextRevMu.Unlock()
	if r.pipelineMu == nil {
		r.pipelineMu = make(map[string]*sync.Mutex)
	}
	mu, ok := r.pipelineMu[pipelineID]
	if !ok {
		mu = &sync.Mutex{}
		r.pipelineMu[pipelineID] = mu
	}
	return mu
}

// Create writes a new revision. Hash-dedup: if the latest revision for
// this pipeline already carries the same snapshot_hash, the existing
// row is returned (the input *rev is populated with the existing
// row's fields) without inserting a duplicate — a re-save with
// identical bytes is a no-op at the storage layer rather than a
// history-churning duplicate.
//
// On a real insert: a new uuid is generated, revision_number is set
// to MAX(revision_number)+1 for this pipeline atomically (under a
// per-pipeline mutex), created_at falls back on the column DEFAULT,
// and deployed_at stays NULL.
func (r *PipelineRevisionRepo) Create(ctx context.Context, tenantID string, rev *PipelineRevision) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	if rev == nil {
		return errors.New("storage: nil pipeline revision")
	}
	if rev.PipelineID == "" {
		return errors.New("storage: revision missing pipeline_id")
	}
	if rev.SnapshotHash == "" {
		return errors.New("storage: revision missing snapshot_hash")
	}

	mu := r.pipelineLock(rev.PipelineID)
	mu.Lock()
	defer mu.Unlock()

	// Hash dedup: if the *latest* revision for this pipeline carries
	// the same hash, return it instead of inserting. Only the latest
	// row is checked — same-hash rows further back in history are
	// legitimate independent snapshots that happened to round-trip
	// to the same bytes, and surfacing the latest preserves the
	// user-visible chronology.
	existing, err := r.latestLocked(ctx, tenantID, rev.PipelineID)
	if err != nil && err != ErrNotFound {
		return fmt.Errorf("dedup probe: %w", err)
	}
	if existing != nil && existing.SnapshotHash == rev.SnapshotHash {
		*rev = *existing
		return nil
	}

	if rev.ID == "" {
		rev.ID = uuid.NewString()
	}
	rev.TenantID = tenantID
	rev.CreatedAt = time.Now().UTC()
	rev.DeployedAt = nil
	rev.DeployRequestID = ""

	// Atomic next-revision-number assignment. The mutex protects the
	// read-then-insert pair; the UNIQUE(pipeline_id, revision_number)
	// constraint is a belt-and-braces guard.
	var maxRev sql.NullInt64
	if err := r.db.QueryRowContext(ctx,
		`SELECT MAX(revision_number) FROM pipeline_revisions
		 WHERE pipeline_id = ? AND tenant_id = ?`,
		rev.PipelineID, tenantID).Scan(&maxRev); err != nil {
		return fmt.Errorf("max revision_number: %w", err)
	}
	rev.RevisionNumber = int(maxRev.Int64) + 1

	_, err = r.db.ExecContext(ctx, `
		INSERT INTO pipeline_revisions (id, tenant_id, pipeline_id, revision_number,
		                                snapshot, snapshot_hash, author_sub,
		                                author_username, change_summary, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rev.ID, tenantID, rev.PipelineID, rev.RevisionNumber,
		rev.Snapshot, rev.SnapshotHash, rev.AuthorSub,
		rev.AuthorUsername, rev.ChangeSummary, rev.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert pipeline_revision: %w", err)
	}
	return nil
}

// List returns revisions for one pipeline, newest-first, paginated.
// Returns (rows, total, error). limit <= 0 falls back to 50; offset
// is taken as-is (negative offsets are coerced to 0).
func (r *PipelineRevisionRepo) List(ctx context.Context, tenantID, pipelineID string, limit, offset int) ([]*PipelineRevision, int, error) {
	if tenantID == "" {
		return nil, 0, ErrTenantRequired
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM pipeline_revisions
		 WHERE tenant_id = ? AND pipeline_id = ?`,
		tenantID, pipelineID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count pipeline_revisions: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, pipelineRevisionSelect+`
		WHERE tenant_id = ? AND pipeline_id = ?
		ORDER BY revision_number DESC
		LIMIT ? OFFSET ?`, tenantID, pipelineID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list pipeline_revisions: %w", err)
	}
	defer rows.Close()
	var out []*PipelineRevision
	for rows.Next() {
		rev, err := scanPipelineRevision(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rev)
	}
	return out, total, rows.Err()
}

// Get returns one revision by number, tenant-scoped. ErrNotFound when
// no such (tenant, pipeline, revision) tuple exists.
func (r *PipelineRevisionRepo) Get(ctx context.Context, tenantID, pipelineID string, revision int) (*PipelineRevision, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	row := r.db.QueryRowContext(ctx, pipelineRevisionSelect+`
		WHERE tenant_id = ? AND pipeline_id = ? AND revision_number = ?`,
		tenantID, pipelineID, revision)
	return scanPipelineRevision(row)
}

// Latest returns the highest-numbered revision for this pipeline
// regardless of deployment state. ErrNotFound when the pipeline has
// no revisions yet.
func (r *PipelineRevisionRepo) Latest(ctx context.Context, tenantID, pipelineID string) (*PipelineRevision, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	return r.latestLocked(ctx, tenantID, pipelineID)
}

// latestLocked is the Latest body without the tenant-required guard;
// callers (Create's dedup probe) have already validated tenantID.
func (r *PipelineRevisionRepo) latestLocked(ctx context.Context, tenantID, pipelineID string) (*PipelineRevision, error) {
	row := r.db.QueryRowContext(ctx, pipelineRevisionSelect+`
		WHERE tenant_id = ? AND pipeline_id = ?
		ORDER BY revision_number DESC
		LIMIT 1`, tenantID, pipelineID)
	return scanPipelineRevision(row)
}

// LatestDeployed returns the highest-numbered deployed revision for
// this pipeline. ErrNotFound when no revision has been deployed yet.
func (r *PipelineRevisionRepo) LatestDeployed(ctx context.Context, tenantID, pipelineID string) (*PipelineRevision, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	row := r.db.QueryRowContext(ctx, pipelineRevisionSelect+`
		WHERE tenant_id = ? AND pipeline_id = ? AND deployed_at IS NOT NULL
		ORDER BY revision_number DESC
		LIMIT 1`, tenantID, pipelineID)
	return scanPipelineRevision(row)
}

// MarkDeployed stamps deployed_at = CURRENT_TIMESTAMP and writes
// deploy_request_id on the row. Idempotent:
//   - If deployed_at is already set, it is NOT shifted on a repeat call.
//   - deploy_request_id is overwritten only when the existing value is
//     empty (so an after-the-fact MarkDeployed can fill it in without
//     trampling the original request id).
//
// Returns ErrNotFound when the (tenant, pipeline, revision) tuple
// doesn't exist.
func (r *PipelineRevisionRepo) MarkDeployed(ctx context.Context, tenantID, pipelineID string, revision int, requestID string) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	// COALESCE preserves the existing deployed_at when set.
	// CASE for deploy_request_id only overwrites the empty string.
	res, err := r.db.ExecContext(ctx, `
		UPDATE pipeline_revisions
		SET deployed_at = COALESCE(deployed_at, ?),
		    deploy_request_id = CASE WHEN deploy_request_id = '' THEN ? ELSE deploy_request_id END
		WHERE tenant_id = ? AND pipeline_id = ? AND revision_number = ?`,
		time.Now().UTC(), requestID, tenantID, pipelineID, revision)
	if err != nil {
		return fmt.Errorf("mark deployed: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const pipelineRevisionSelect = `
SELECT id, tenant_id, pipeline_id, revision_number, snapshot, snapshot_hash,
       author_sub, author_username, change_summary, created_at,
       deployed_at, deploy_request_id
FROM pipeline_revisions`

func scanPipelineRevision(s scanner) (*PipelineRevision, error) {
	rev := &PipelineRevision{}
	var deployedAt sql.NullTime
	err := s.Scan(&rev.ID, &rev.TenantID, &rev.PipelineID, &rev.RevisionNumber,
		&rev.Snapshot, &rev.SnapshotHash, &rev.AuthorSub, &rev.AuthorUsername,
		&rev.ChangeSummary, &rev.CreatedAt, &deployedAt, &rev.DeployRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if deployedAt.Valid {
		t := deployedAt.Time
		rev.DeployedAt = &t
	}
	return rev, nil
}
