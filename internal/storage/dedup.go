package storage

import (
	"context"
	"fmt"
	"time"
)

// DedupRepo persists the per-pipeline payload-hash window used by the
// destination-side dedup feature. Callers — the pipeline executor —
// invoke CheckAndRecord once per outbound message; the repo answers
// whether that exact payload has been seen on this pipeline within
// the window and atomically records the new sighting.
type DedupRepo struct{ db *dbWrap }

// CheckAndRecord checks the (pipelineID, payloadHash) pair against
// the dedup window. windowSeconds is taken from the calling pipeline
// at the time of the check; passing it in (rather than reading it
// from a column) keeps the storage call cheap and lets the caller
// short-circuit the entire database round-trip when dedup is off.
//
// Semantics:
//
//   - Returns (true, ...) when an entry exists whose last_seen_at is
//     within the window. The caller treats this as a duplicate and
//     skips the destination send. The row's last_seen_at is bumped
//     so a long burst of dupes keeps refreshing the window.
//   - Returns (false, ...) when there's no entry OR the existing
//     entry's last_seen_at is older than the window (the previous
//     observation has expired and this counts as a fresh first
//     sighting). In both cases a fresh row is left behind.
//
// Concurrency: the DELETE-then-INSERT-OR-CONFLICT pair runs inside
// one transaction so two workers can't both observe "no row, insert
// fresh" simultaneously. The DELETE is bounded by `last_seen_at < cutoff`
// so it never removes a live row.
func (r *DedupRepo) CheckAndRecord(ctx context.Context, pipelineID string, payloadHash string, windowSeconds int) (bool, error) {
	if pipelineID == "" {
		return false, fmt.Errorf("dedup: pipeline_id required")
	}
	if payloadHash == "" {
		return false, fmt.Errorf("dedup: payload_hash required")
	}
	if windowSeconds <= 0 {
		return false, nil
	}
	now := time.Now().UTC()
	cutoff := now.Add(-time.Duration(windowSeconds) * time.Second)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("dedup: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Expire stale entries for this specific pair so the next INSERT
	// either populates a fresh row or hits an in-window conflict.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM pipeline_dedup
		WHERE pipeline_id = ? AND payload_hash = ? AND last_seen_at < ?`,
		pipelineID, payloadHash, cutoff); err != nil {
		return false, fmt.Errorf("dedup: expire stale: %w", err)
	}

	// UPSERT. The INSERT path is taken when no row exists; the UPDATE
	// path is taken when an in-window row already exists (its hits +
	// last_seen_at get bumped). The "was_dupe" decision is encoded in
	// the affected-rows behaviour: post-upsert we read hits back and
	// compare to 1.
	res, err := tx.ExecContext(ctx, `
		INSERT INTO pipeline_dedup (pipeline_id, payload_hash, first_seen_at, last_seen_at, hits)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT (pipeline_id, payload_hash) DO UPDATE SET
		  last_seen_at = excluded.last_seen_at,
		  hits = pipeline_dedup.hits + 1`,
		pipelineID, payloadHash, now, now)
	if err != nil {
		return false, fmt.Errorf("dedup: upsert: %w", err)
	}
	_ = res

	// Read back the hits count to decide. hits=1 → INSERT path took it
	// (fresh sighting); hits>1 → UPDATE path took it (in-window dupe).
	var hits int
	if err := tx.QueryRowContext(ctx, `
		SELECT hits FROM pipeline_dedup
		WHERE pipeline_id = ? AND payload_hash = ?`,
		pipelineID, payloadHash).Scan(&hits); err != nil {
		return false, fmt.Errorf("dedup: read hits: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("dedup: commit: %w", err)
	}
	return hits > 1, nil
}

// Prune removes pipeline_dedup rows whose last_seen_at is older than
// the supplied cutoff. The sweeper passes a cutoff derived from the
// LONGEST per-pipeline window so a short-window pipeline doesn't
// keep dead rows owned by another (longer-window) pipeline. Returns
// the count removed.
func (r *DedupRepo) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, nil
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM pipeline_dedup WHERE last_seen_at < ?`, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("dedup prune: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeletePipeline removes every dedup row owned by pipelineID. Called
// when a pipeline is deleted (the FK with ON DELETE CASCADE handles
// this transparently in SQLite; this helper exists for tests and for
// the Postgres path where the cascade may need a manual trigger
// during certain migrations).
func (r *DedupRepo) DeletePipeline(ctx context.Context, pipelineID string) error {
	if pipelineID == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM pipeline_dedup WHERE pipeline_id = ?`, pipelineID)
	if err != nil {
		return fmt.Errorf("dedup delete pipeline: %w", err)
	}
	return nil
}

// CountForPipeline returns the number of live dedup rows for one
// pipeline. Used by the metrics renderer and by tests.
func (r *DedupRepo) CountForPipeline(ctx context.Context, pipelineID string) (int64, error) {
	var n int64
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM pipeline_dedup WHERE pipeline_id = ?`,
		pipelineID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("dedup count: %w", err)
	}
	return n, nil
}
