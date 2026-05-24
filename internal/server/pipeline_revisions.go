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

// snapshotWriteTimeout caps how long the async snapshot dispatch can
// block on the database. Five seconds matches the upper bound of a
// healthy SQLite write under contention; anything longer is almost
// certainly a stuck DB and should be abandoned rather than leaking a
// goroutine indefinitely.
const snapshotWriteTimeout = 5 * time.Second

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
// Best-effort + async: the work runs in a background goroutine off a
// fresh context.Background()-derived timeout, so a mid-flight client
// disconnect (which would cancel r.Context()) does not cancel the
// snapshot SQL after the live write has already committed. Errors are
// logged with slog and swallowed. A snapshot failure must never block
// the HTTP response or roll back the live write that triggered the
// snapshot. Same intent as the audit insert in middleware_audit.go.
func (s *Server) snapshotPipelineRevision(
	ctx context.Context,
	tenantID, pipelineID, changeSummary, requestID string,
	markDeployed bool,
) {
	if s == nil || s.store == nil || s.store.PipelineRevisions == nil {
		return
	}
	// Capture the auth user from the request context up-front — the
	// goroutine below uses a fresh background context for SQL, so the
	// request-scoped values aren't reachable from there.
	var authorSub, authorUsername string
	if u, ok := auth.UserFromContext(ctx); ok && u != nil {
		authorSub = u.Sub
		authorUsername = u.PreferredUsername
	}
	// Track this goroutine on pendingBackgroundOps so tests can drain
	// it deterministically (via Server.WaitForBackgroundOps) and so
	// graceful shutdown can give in-flight snapshots a bounded chance
	// to land before the process exits. Add must happen on the
	// caller's goroutine, before `go`, so a Wait() racing the spawn
	// sees the increment.
	s.pendingBackgroundOps.Add(1)
	go func() {
		defer s.pendingBackgroundOps.Done()
		bgCtx, cancel := context.WithTimeout(context.Background(), snapshotWriteTimeout)
		defer cancel()
		if err := s.snapshotPipelineRevisionSync(
			bgCtx, tenantID, pipelineID, changeSummary, requestID,
			authorSub, authorUsername, markDeployed); err != nil {
			s.logger.Warn("snapshot: async dispatch failed",
				"err", err, "pipeline_id", pipelineID,
				"tenant_id", tenantID, "request_id", requestID)
		}
	}()
}

// snapshotPipelineRevisionSync does the actual work synchronously and
// returns any error so tests and the async wrapper can react. The
// public snapshotPipelineRevision is a thin async dispatcher around
// this; all production callers go through the async wrapper. Author
// fields are passed in (not pulled from ctx) because the wrapper runs
// off a background context that no longer carries the request user.
func (s *Server) snapshotPipelineRevisionSync(
	ctx context.Context,
	tenantID, pipelineID, changeSummary, requestID, authorSub, authorUsername string,
	markDeployed bool,
) error {
	if s == nil || s.store == nil || s.store.PipelineRevisions == nil {
		return nil
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
		return err
	}
	stages, err := s.store.Stages.ListByPipeline(ctx, tenantID, pipelineID)
	if err != nil {
		s.logger.Warn("snapshot: stages read failed",
			"err", err, "pipeline_id", pipelineID, "request_id", requestID)
		return err
	}
	transforms, err := s.store.Transforms.ListByPipeline(ctx, tenantID, pipelineID)
	if err != nil {
		s.logger.Warn("snapshot: transforms read failed",
			"err", err, "pipeline_id", pipelineID, "request_id", requestID)
		return err
	}
	rules, err := s.store.RoutingRules.ListByPipeline(ctx, tenantID, pipelineID)
	if err != nil {
		s.logger.Warn("snapshot: routing rules read failed",
			"err", err, "pipeline_id", pipelineID, "request_id", requestID)
		return err
	}

	// 2. Build the canonical snapshot. encoding/json emits struct
	//    fields in declaration order, which is deterministic for these
	//    types.
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
		return err
	}
	// 2a. Hash a *separate* projection of the snapshot that strips the
	//     volatile child identifiers. StageRepo.ReplaceForPipeline (and
	//     friends) DELETE + INSERT with fresh uuid.NewString() IDs on
	//     every PUT, so two byte-identical client payloads land as
	//     different child IDs in the live tables. Hashing the raw
	//     snapshot bytes would therefore make every "no-op" PUT look
	//     unique and silently disable dedup. The stored Snapshot above
	//     keeps the full IDs (needed for replay/rollback fidelity);
	//     only the hash uses the stripped projection.
	hashSnap := snapshotForHash(&snap)
	hashBytes, err := json.Marshal(hashSnap)
	if err != nil {
		s.logger.Warn("snapshot: hash marshal failed",
			"err", err, "pipeline_id", pipelineID, "request_id", requestID)
		return err
	}
	sum := sha256.Sum256(hashBytes)
	hash := hex.EncodeToString(sum[:])

	// 3. Build the revision.
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
		return err
	}

	// 4. Legacy "save and ship" semantics: every successful save by
	//    the old PUT handlers is also a deploy. The hash-dedup case
	//    leaves rev pointing at the pre-existing row (possibly
	//    already deployed); MarkDeployed is idempotent — it
	//    preserves an existing deployed_at and only fills an empty
	//    deploy_request_id, so calling it unconditionally is safe.
	if !markDeployed {
		return nil
	}
	if err := s.store.PipelineRevisions.MarkDeployed(ctx, tenantID, pipelineID, rev.RevisionNumber, requestID); err != nil {
		s.logger.Warn("snapshot: mark deployed failed",
			"err", err, "pipeline_id", pipelineID,
			"revision", rev.RevisionNumber, "request_id", requestID)
		return err
	}
	return nil
}

// snapshotForHash returns a *new* PipelineSnapshot whose child rows
// have their volatile identifier fields (ID, TenantID, PipelineID)
// zeroed out. The returned value is what we hash for dedup; the
// caller still stores the un-stripped original in the revision row.
//
// Why this exists: the legacy "replace all children" repo methods
// (StageRepo.ReplaceForPipeline, TransformRepo.ReplaceForPipeline,
// RoutingRuleRepo.ReplaceForPipeline) DELETE the old rows and INSERT
// new ones with fresh uuid.NewString() IDs. Two byte-identical
// client payloads therefore yield different child IDs in the live
// tables — and so a hash over the raw snapshot bytes would never
// collide, defeating the repo-layer dedup contract.
//
// Tenant ID and pipeline ID are also stripped from each child for
// symmetry: they're constant for a given pipeline, but if a row were
// ever re-tenanted (operational error, future migration) we don't
// want history to fork on it. The top-level Pipeline row's ID is
// kept — pipelines aren't recreated on update, so it's stable.
func snapshotForHash(s *storage.PipelineSnapshot) *storage.PipelineSnapshot {
	if s == nil {
		return nil
	}
	out := &storage.PipelineSnapshot{
		SchemaVersion: s.SchemaVersion,
	}
	if s.Pipeline != nil {
		p := *s.Pipeline
		out.Pipeline = &p
	}
	if s.Stages != nil {
		out.Stages = make([]*storage.Stage, len(s.Stages))
		for i, st := range s.Stages {
			if st == nil {
				continue
			}
			c := *st
			c.ID = ""
			c.TenantID = ""
			c.PipelineID = ""
			out.Stages[i] = &c
		}
	}
	if s.Transforms != nil {
		out.Transforms = make([]*storage.Transform, len(s.Transforms))
		for i, tr := range s.Transforms {
			if tr == nil {
				continue
			}
			c := *tr
			c.ID = ""
			c.TenantID = ""
			c.PipelineID = ""
			out.Transforms[i] = &c
		}
	}
	if s.RoutingRules != nil {
		out.RoutingRules = make([]*storage.RoutingRule, len(s.RoutingRules))
		for i, rr := range s.RoutingRules {
			if rr == nil {
				continue
			}
			c := *rr
			c.ID = ""
			c.TenantID = ""
			c.PipelineID = ""
			out.RoutingRules[i] = &c
		}
	}
	return out
}
