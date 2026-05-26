package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"mqConnector/internal/storage"
)

// applyRevisionLive writes a captured PipelineSnapshot back to the
// live pipeline + stages + transforms + routing_rules tables. All four
// writes share a single transaction — partial application is the worst
// possible outcome (executor sees the pipeline with old stages, or new
// stages with the old destination), so on any failure the tx rolls
// back and the live tables are untouched.
//
// Live-row identity is preserved:
//   - The pipeline's live ID and TenantID always win over whatever
//     the snapshot carries. The snapshot's pipeline ID is stable for
//     a given pipeline but the snapshot also predates the live row,
//     so trusting it blindly would let a hand-edited revision rewrite
//     ownership.
//   - The snapshot's CreatedAt/UpdatedAt are bookkeeping — they were
//     zeroed before hashing — so the repo's Update stamps a fresh
//     UpdatedAt and CreatedAt stays whatever the live row already
//     held (not part of the UPDATE statement).
//
// Children are replaced wholesale via the repos' ReplaceForPipelineTx
// methods, which DELETE + INSERT under fresh uuid.NewString() IDs.
// This matches the legacy PUT path's behaviour exactly — the snapshot
// is a description of the executor-visible state, not a directive to
// preserve every old child row id.
//
// Callers are responsible for triggering pipeline.Manager.Reload after
// a successful return — the live tables are now ahead of the in-memory
// pipeline workers and the hot-reload is what closes the gap.
func (s *Server) applyRevisionLive(
	ctx context.Context,
	tenantID, pipelineID string,
	snap *storage.PipelineSnapshot,
) error {
	if s == nil || s.store == nil {
		return errors.New("apply revision: nil store")
	}
	if snap == nil || snap.Pipeline == nil {
		return errors.New("apply revision: snapshot missing pipeline")
	}
	if tenantID == "" {
		return errors.New("apply revision: tenant id required")
	}
	if pipelineID == "" {
		return errors.New("apply revision: pipeline id required")
	}

	tx, err := s.store.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("apply revision: begin tx: %w", err)
	}
	// Defer rollback for the error path. Commit() invalidates the tx,
	// so a post-commit Rollback() returns sql.ErrTxDone — harmless.
	// Track commit explicitly so a panic mid-flight still triggers
	// rollback.
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// 1. Pipeline row. Build a copy of the snapshot's pipeline with
	//    the live ID + tenant pinned. CreatedAt is not part of the
	//    UPDATE; UpdatedAt is overwritten by the repo.
	pipeCopy := *snap.Pipeline
	pipeCopy.ID = pipelineID
	pipeCopy.TenantID = tenantID
	pipeCopy.CreatedAt = time.Time{}
	pipeCopy.UpdatedAt = time.Time{}
	if err := s.store.Pipelines.UpdateTx(ctx, tx, tenantID, &pipeCopy); err != nil {
		return fmt.Errorf("apply pipeline row: %w", err)
	}

	// 2-4. Replace each child collection. ReplaceForPipelineTx clears
	//      the existing rows under (tenant_id, pipeline_id) then
	//      inserts fresh ones from the snapshot. The repo loop stamps
	//      the right tenant/pipeline ids and a fresh uuid on every
	//      child, same as the legacy PUT path.
	if err := s.store.Stages.ReplaceForPipelineTx(ctx, tx, tenantID, pipelineID, snap.Stages); err != nil {
		return fmt.Errorf("apply stages: %w", err)
	}
	if err := s.store.Transforms.ReplaceForPipelineTx(ctx, tx, tenantID, pipelineID, snap.Transforms); err != nil {
		return fmt.Errorf("apply transforms: %w", err)
	}
	if err := s.store.RoutingRules.ReplaceForPipelineTx(ctx, tx, tenantID, pipelineID, snap.RoutingRules); err != nil {
		return fmt.Errorf("apply routing rules: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("apply revision: commit: %w", err)
	}
	committed = true
	return nil
}
