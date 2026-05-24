package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// snapshotPipelineRevision captures the current state of a pipeline
// (pipeline + stages + transforms + routing rules) into a new
// pipeline_revisions row. The repo's hash-dedup means no row is
// inserted if the snapshot is byte-identical to the latest revision.
//
// If markDeployed is true, the revision is immediately stamped as
// deployed. The legacy PUT handlers use markDeployed=true — their
// "save" semantic is "save and ship in one shot". A future
// Save-Draft path will pass markDeployed=false.
//
// Best-effort: errors are logged with slog and swallowed. A snapshot
// failure must never block the HTTP response or roll back the live
// write that triggered the snapshot. Same intent as the audit insert
// in middleware_audit.go.
func (s *Server) snapshotPipelineRevision(
	ctx context.Context,
	tenantID, pipelineID, changeSummary, requestID string,
	markDeployed bool,
) {
	if s == nil || s.store == nil || s.store.PipelineRevisions == nil {
		return
	}

	// 1. Read the pipeline + all child rows in their executor-visible
	//    order. Each repo's ListBy method already returns ordered
	//    slices (stages by stage_order ASC, transforms by ord ASC,
	//    routing rules by priority ASC).
	pipe, err := s.store.Pipelines.Get(ctx, tenantID, pipelineID)
	if err != nil {
		s.logger.Warn("snapshot: pipeline read failed",
			"err", err, "pipeline_id", pipelineID, "tenant_id", tenantID,
			"request_id", requestID)
		return
	}
	stages, err := s.store.Stages.ListByPipeline(ctx, tenantID, pipelineID)
	if err != nil {
		s.logger.Warn("snapshot: stages read failed",
			"err", err, "pipeline_id", pipelineID, "request_id", requestID)
		return
	}
	transforms, err := s.store.Transforms.ListByPipeline(ctx, tenantID, pipelineID)
	if err != nil {
		s.logger.Warn("snapshot: transforms read failed",
			"err", err, "pipeline_id", pipelineID, "request_id", requestID)
		return
	}
	rules, err := s.store.RoutingRules.ListByPipeline(ctx, tenantID, pipelineID)
	if err != nil {
		s.logger.Warn("snapshot: routing rules read failed",
			"err", err, "pipeline_id", pipelineID, "request_id", requestID)
		return
	}

	// 2. Build the canonical snapshot. encoding/json emits struct
	//    fields in declaration order, which is deterministic for these
	//    types — sufficient for the hash to be stable across two
	//    identical PUTs.
	//
	//    Timestamps on the Pipeline row (CreatedAt, UpdatedAt) are
	//    bookkeeping, not configuration — and UpdatedAt is bumped on
	//    every PUT, which would otherwise defeat hash dedup. Zero
	//    them on a copy of the row so two identical configuration
	//    PUTs collapse to a single revision.
	pipeCopy := *pipe
	pipeCopy.CreatedAt = time.Time{}
	pipeCopy.UpdatedAt = time.Time{}
	snap := storage.PipelineSnapshot{
		Pipeline:      &pipeCopy,
		Stages:        stages,
		Transforms:    transforms,
		RoutingRules:  rules,
		SchemaVersion: 1,
	}
	bytes, err := json.Marshal(snap)
	if err != nil {
		s.logger.Warn("snapshot: marshal failed",
			"err", err, "pipeline_id", pipelineID, "request_id", requestID)
		return
	}
	sum := sha256.Sum256(bytes)
	hash := hex.EncodeToString(sum[:])

	// 3. Build the revision. The author is read off the request
	//    context via the auth package's helper — same accessor the
	//    audit middleware uses.
	var authorSub, authorUsername string
	if u, ok := auth.UserFromContext(ctx); ok && u != nil {
		authorSub = u.Sub
		authorUsername = u.PreferredUsername
	}
	rev := &storage.PipelineRevision{
		PipelineID:      pipelineID,
		Snapshot:        string(bytes),
		SnapshotHash:    hash,
		AuthorSub:       authorSub,
		AuthorUsername:  authorUsername,
		ChangeSummary:   changeSummary,
		DeployRequestID: requestID,
	}
	if err := s.store.PipelineRevisions.Create(ctx, tenantID, rev); err != nil {
		s.logger.Warn("snapshot: revision insert failed",
			"err", err, "pipeline_id", pipelineID, "request_id", requestID)
		return
	}

	// 4. Legacy "save and ship" semantics: every successful save by
	//    the old PUT handlers is also a deploy. The hash-dedup case
	//    leaves rev pointing at the pre-existing row (possibly
	//    already deployed); MarkDeployed is idempotent — it
	//    preserves an existing deployed_at and only fills an empty
	//    deploy_request_id, so calling it unconditionally is safe.
	if !markDeployed {
		return
	}
	if err := s.store.PipelineRevisions.MarkDeployed(ctx, tenantID, pipelineID, rev.RevisionNumber, requestID); err != nil {
		s.logger.Warn("snapshot: mark deployed failed",
			"err", err, "pipeline_id", pipelineID,
			"revision", rev.RevisionNumber, "request_id", requestID)
	}
}
