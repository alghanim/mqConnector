package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// Read-only revision-history endpoints for the Pipeline Studio. Every
// handler in this file requires a valid session (route-level
// RequireSession) plus tenant scoping via ensurePipelineInTenant, which
// returns the canonical 404 for cross-tenant or unknown pipeline
// references — never 403, mirroring the convention established by the
// other pipeline read handlers in handlers_pipelines.go.

// Revision listing pagination knobs. defaultRevisionListLimit matches
// the UI's history-pane page size; revisionListMaxLimit caps a caller
// asking for more so we don't accidentally return the whole table on a
// long-lived pipeline. limit <= 0 falls back to the default; limit
// over the max is clamped down (a clamp, not a 400, matches DLQ
// pagination's tolerant query parsing).
const (
	defaultRevisionListLimit = 25
	revisionListMaxLimit     = 200
)

// revisionResponse is the wire shape for a single revision. The stored
// snapshot is a canonical JSON string in the database; we decode it on
// the way out so API consumers don't have to do a second parse pass.
// Embedding *storage.PipelineRevision keeps every metadata field
// (revision_number, deployed_at, author, change_summary, etc.) in the
// response under their existing JSON names; replacing the `snapshot`
// field swaps the opaque string for the parsed object — Tasks 4-6
// (diff/rollback/deploy) all want the structured form and there's no
// caller today that depends on the raw string surfaced over HTTP.
type revisionResponse struct {
	*storage.PipelineRevision
	// Snapshot replaces the embedded PipelineRevision.Snapshot string
	// with the decoded object. Renamed via the `snapshot` JSON tag so
	// the field name takes precedence over the embedded one
	// (encoding/json uses the outermost tag when names collide).
	Snapshot *storage.PipelineSnapshot `json:"snapshot"`
}

// makeRevisionResponse decodes the stored canonical JSON into a
// PipelineSnapshot and returns the wire shape. A decode failure leaves
// the snapshot nil rather than failing the whole handler: a corrupted
// row shouldn't take down the history pane, and the metadata fields
// are still useful for triage.
func (s *Server) makeRevisionResponse(rev *storage.PipelineRevision) revisionResponse {
	resp := revisionResponse{PipelineRevision: rev}
	var snap storage.PipelineSnapshot
	if err := json.Unmarshal([]byte(rev.Snapshot), &snap); err == nil {
		resp.Snapshot = &snap
	} else {
		s.logger.Warn("pipeline revision snapshot decode failed",
			"err", err, "pipeline_id", rev.PipelineID,
			"revision", rev.RevisionNumber)
	}
	return resp
}

// handleListRevisions serves GET /api/v1/pipelines/{id}/revisions.
//
// Newest-first. Query params:
//   - limit  (default 25, max 200; 0 or negative → default)
//   - offset (default 0; negative → 0)
//
// Response envelope mirrors the DLQ list shape (items + total +
// pagination) but with the operation's actual knob names — limit /
// offset — since the underlying repo is offset/limit-paginated rather
// than page/per_page.
func (s *Server) handleListRevisions(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}

	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = defaultRevisionListLimit
	}
	if limit > revisionListMaxLimit {
		limit = revisionListMaxLimit
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}

	list, total, err := s.store.PipelineRevisions.List(r.Context(), tenant, id, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]revisionResponse, 0, len(list))
	for _, rev := range list {
		out = append(out, s.makeRevisionResponse(rev))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"revisions": out,
		"total":     total,
		"limit":     limit,
		"offset":    offset,
	})
}

// handleGetCurrentRevision serves GET
// /api/v1/pipelines/{id}/revisions/current — the latest *deployed*
// revision. 404 when nothing has been deployed yet (the executor would
// be running off the live tables anyway; the history pane just hasn't
// caught up).
func (s *Server) handleGetCurrentRevision(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	rev, err := s.store.PipelineRevisions.LatestDeployed(r.Context(), tenant, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no deployed revision for this pipeline")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.makeRevisionResponse(rev))
}

// handleGetRevision serves GET /api/v1/pipelines/{id}/revisions/{rev}.
// {rev} is the revision_number assigned at save time — a positive
// integer. Non-numeric or non-positive values return 400; cross-tenant
// or unknown numbers return 404 (matching the cross-tenant convention
// used everywhere else in the pipelines surface).
func (s *Server) handleGetRevision(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	revNum, ok := parseRevisionNumber(w, chi.URLParam(r, "rev"))
	if !ok {
		return
	}
	rev, err := s.store.PipelineRevisions.Get(r.Context(), tenant, id, revNum)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "revision not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.makeRevisionResponse(rev))
}

// parseRevisionNumber parses a `{rev}` URL parameter or `against` query
// parameter into a positive int. On failure it writes the canonical
// 400 envelope and returns ok=false; callers should `return` on
// !ok without writing anything more. Shared by handleGetRevision
// (path param) and handleDiffRevisions (path + query).
func parseRevisionNumber(w http.ResponseWriter, raw string) (int, bool) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		writeError(w, http.StatusBadRequest, "revision must be a positive integer")
		return 0, false
	}
	return n, true
}

// diffResponse is the wire envelope for the diff endpoint. From is
// the `{rev}` path parameter (the baseline); To is the `against`
// query parameter (the target). The convention is "if I moved FROM
// rev TO against, what would change?" so before=rev, after=against.
// See handleDiffRevisions for the matching logic on the URL side.
type diffResponse struct {
	From int           `json:"from"`
	To   int           `json:"to"`
	Diff *SnapshotDiff `json:"diff"`
}

// handleDiffRevisions serves GET
// /api/v1/pipelines/{id}/revisions/{rev}/diff?against={other}.
//
// Direction: from={rev}, to={against}. The diff describes what would
// change if the operator deployed `against` over `rev` — so the
// Studio diff viewer can preview a rollback ({rev}=current,
// {against}=older draft) or a forward deploy ({rev}=baseline,
// {against}=newer draft) without flipping the parameter convention.
//
// Validation is identical to handleGetRevision for both numbers:
// non-numeric or non-positive → 400. A missing `against` query param
// is also 400 — the diff has no useful default when the second
// revision isn't named. An unknown revision on either side returns
// 404 (cross-tenant matches: ensurePipelineInTenant fires first).
// RBAC: viewer-equivalent, no gatePipeline call — the diff exposes
// no more than the underlying revision-read endpoints, which are
// already viewer-readable.
func (s *Server) handleDiffRevisions(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	fromNum, ok := parseRevisionNumber(w, chi.URLParam(r, "rev"))
	if !ok {
		return
	}
	againstStr := r.URL.Query().Get("against")
	if againstStr == "" {
		writeError(w, http.StatusBadRequest, "missing required query parameter: against")
		return
	}
	toNum, ok := parseRevisionNumber(w, againstStr)
	if !ok {
		return
	}

	fromRev, err := s.store.PipelineRevisions.Get(r.Context(), tenant, id, fromNum)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "revision not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	toRev, err := s.store.PipelineRevisions.Get(r.Context(), tenant, id, toNum)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "revision not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Decode both stored snapshots. A corrupt row is logged + 500'd
	// rather than papered over with an empty snapshot — the Studio
	// diff viewer would otherwise render a confidently-wrong "no
	// changes" panel.
	var beforeSnap, afterSnap storage.PipelineSnapshot
	if err := json.Unmarshal([]byte(fromRev.Snapshot), &beforeSnap); err != nil {
		s.logger.Warn("diff: snapshot decode failed",
			"err", err, "pipeline_id", id, "revision", fromNum)
		writeError(w, http.StatusInternalServerError, "failed to decode revision snapshot")
		return
	}
	if err := json.Unmarshal([]byte(toRev.Snapshot), &afterSnap); err != nil {
		s.logger.Warn("diff: snapshot decode failed",
			"err", err, "pipeline_id", id, "revision", toNum)
		writeError(w, http.StatusInternalServerError, "failed to decode revision snapshot")
		return
	}

	writeJSON(w, http.StatusOK, diffResponse{
		From: fromNum,
		To:   toNum,
		Diff: DiffSnapshots(&beforeSnap, &afterSnap),
	})
}

// rollbackRequest is the optional body for the rollback endpoint.
// change_summary is the only field — if absent or empty the handler
// falls back to "Rollback to revision N". No other fields exist today;
// DisallowUnknownFields will surface any client typos as 400.
type rollbackRequest struct {
	ChangeSummary string `json:"change_summary"`
}

// The rollback + deploy endpoints share the same wire shape with the
// read endpoints: revisionResponse (embedded PipelineRevision metadata
// + the decoded snapshot, with the embedded raw `snapshot` string
// shadowed by the parsed object via Go's outermost-tag-wins rule).
// makeRevisionResponse is the single helper that produces it; reusing
// it here keeps the wire format consistent and prevents the snapshot
// bytes from being serialised twice (once as the embedded raw string,
// once as the parsed object).

// handleRollbackRevision serves POST
// /api/v1/pipelines/{id}/revisions/{rev}/rollback.
//
// RBAC: operator on the pipeline (per-pipeline gate, honours grants).
//
// Behaviour: the target revision's snapshot is written through to the
// live tables under one transaction (see applyRevisionLive), a NEW
// revision row is created carrying the same snapshot bytes
// (revision_number = MAX+1), the new revision is stamped deployed,
// and the pipeline manager is hot-reloaded. The hot-reload kick is
// non-blocking — Reload acquires its own context and runs to
// completion in the background.
//
// Response carries the freshly-created revision (not the source one)
// so the UI can re-render its history pane without a follow-up GET.
//
// Errors:
//   - 400 bad {rev} (non-numeric, ≤ 0, or malformed body)
//   - 403 caller lacks operator role on the pipeline (gatePipeline)
//   - 404 pipeline not in tenant, or {rev} doesn't exist
//   - 500 apply failed (tx rolled back; live tables untouched) or
//     follow-up revision insert / MarkDeployed failed
func (s *Server) handleRollbackRevision(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	if !s.gatePipeline(w, r, id, storage.RoleOperator) {
		return
	}
	revNum, ok := parseRevisionNumber(w, chi.URLParam(r, "rev"))
	if !ok {
		return
	}

	// Body is optional. An entirely empty request body (no bytes)
	// short-circuits the decoder so a caller invoking the endpoint
	// without a Content-Type doesn't trip a 400; a non-empty body
	// must still parse as JSON and pass DisallowUnknownFields. The
	// `if err != io.EOF` shape covers the body-less case under both
	// http.NoBody and a zero-length io.Reader.
	var body rollbackRequest
	if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	target, err := s.store.PipelineRevisions.Get(r.Context(), tenant, id, revNum)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "revision not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var snap storage.PipelineSnapshot
	if err := json.Unmarshal([]byte(target.Snapshot), &snap); err != nil {
		s.logger.Warn("rollback: snapshot decode failed",
			"err", err, "pipeline_id", id, "revision", revNum)
		writeError(w, http.StatusInternalServerError, "failed to decode revision snapshot")
		return
	}

	// 1. Atomic write-through to live tables. On failure the tx rolls
	//    back; live tables are unchanged and the caller sees 500.
	if err := s.applyRevisionLive(r.Context(), tenant, id, &snap); err != nil {
		s.logger.Warn("rollback: apply failed",
			"err", err, "pipeline_id", id, "revision", revNum)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 2. Create a new revision row holding the same snapshot bytes.
	//    Using the source revision's snapshot verbatim — rather than
	//    re-reading the live tables — keeps the JSON stable: child
	//    IDs in the live tables were regenerated by
	//    ReplaceForPipelineTx, but the revision row should describe
	//    the *configuration* that was rolled back to, not the
	//    incidental IDs the executor will see.
	summary := body.ChangeSummary
	if summary == "" {
		summary = fmt.Sprintf("Rollback to revision %d", revNum)
	}
	authorSub, authorUsername := authorFromCtx(r.Context())
	requestID := RequestIDFromContext(r.Context())
	newRev := &storage.PipelineRevision{
		PipelineID:      id,
		Snapshot:        target.Snapshot,
		SnapshotHash:    target.SnapshotHash,
		AuthorSub:       authorSub,
		AuthorUsername:  authorUsername,
		ChangeSummary:   summary,
		DeployRequestID: requestID,
	}
	// Use CreateForce so the per-pipeline hash-dedup that Create
	// applies doesn't collapse the rollback's new row into the
	// target revision when the target IS already the most recent
	// revision. A rollback carries operator intent (the
	// change_summary, the request id) that we record even when the
	// snapshot bytes haven't moved — dedup would silently swallow
	// that intent.
	if err := s.store.PipelineRevisions.CreateForce(r.Context(), tenant, newRev); err != nil {
		s.logger.Warn("rollback: revision insert failed",
			"err", err, "pipeline_id", id, "source_revision", revNum)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 3. Mark the new revision deployed. MarkDeployed is idempotent —
	//    if hash-dedup collapsed newRev into a pre-existing row that
	//    was already deployed, deployed_at is preserved and only an
	//    empty deploy_request_id is filled.
	if err := s.store.PipelineRevisions.MarkDeployed(r.Context(), tenant, id, newRev.RevisionNumber, requestID); err != nil {
		s.logger.Warn("rollback: mark deployed failed",
			"err", err, "pipeline_id", id, "revision", newRev.RevisionNumber)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Re-read so the response carries the fresh deployed_at.
	if refreshed, err := s.store.PipelineRevisions.Get(r.Context(), tenant, id, newRev.RevisionNumber); err == nil {
		newRev = refreshed
	}

	// 4. Hot-reload the pipeline manager so the new live tables are
	//    picked up. Backgrounded to keep the response latency low —
	//    Reload acquires its own context; the request-scoped ctx
	//    above would cancel when the response flushes.
	go s.reloadPipelines()

	writeJSON(w, http.StatusOK, s.makeRevisionResponse(newRev))
}

// deployRequest is the body for the deploy endpoint. revision_number
// is required; change_summary and approver are optional. The approver
// gate fires only when pipelines.requires_approval is true (default
// false, no UI to flip in Wave 1).
type deployRequest struct {
	RevisionNumber int    `json:"revision_number"`
	ChangeSummary  string `json:"change_summary"`
	Approver       string `json:"approver"`
}

// handleDeployRevision serves POST /api/v1/pipelines/{id}/deploy.
//
// RBAC: operator on the pipeline (per-pipeline gate, honours grants).
//
// Behaviour: an EXISTING revision is promoted to live. The endpoint
// loads the target revision, writes its snapshot through to the live
// tables (atomic, see applyRevisionLive), marks the existing revision
// deployed (no new revision row is created — this distinguishes
// /deploy from /rollback), and triggers a pipeline hot-reload. The
// "deploy a draft" Studio path will land here once the Save-Draft UI
// is shipped in a later wave; today the endpoint still works against
// any existing revision (the legacy save-and-ship PUTs already mark
// their revisions deployed, so deploying an already-deployed revision
// is a sanctioned no-op that re-reads from the snapshot).
//
// Approver gate: when the pipeline's requires_approval column is
// true, the request body MUST carry a non-empty approver field. The
// column default is false and there is no Wave-1 UI to flip it, so
// the gate is latent but correct for the day the flag flips.
//
// Errors:
//   - 400 bad body, missing revision_number, or revision_number <= 0
//   - 403 caller lacks operator role on the pipeline (gatePipeline)
//   - 404 pipeline not in tenant, or revision_number doesn't exist
//   - 409 requires_approval=true and approver is empty
//   - 500 apply failed (tx rolled back; live tables untouched) or
//     MarkDeployed failed
func (s *Server) handleDeployRevision(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	if !s.gatePipeline(w, r, id, storage.RoleOperator) {
		return
	}

	var body deployRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.RevisionNumber < 1 {
		writeError(w, http.StatusBadRequest, "revision_number must be a positive integer")
		return
	}

	// Read the live pipeline to check the approval gate. The
	// ensurePipelineInTenant call above already validated existence
	// and tenancy, so this Get is essentially a re-read for the
	// requires_approval flag; the cost is one indexed lookup.
	pipe, err := s.store.Pipelines.Get(r.Context(), tenant, id)
	if err != nil {
		// ensurePipelineInTenant succeeded, so this should only fail
		// on a transient DB error — surface 500.
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pipe.RequiresApproval && body.Approver == "" {
		writeError(w, http.StatusConflict, "this pipeline requires approval: include 'approver' in the request body")
		return
	}

	target, err := s.store.PipelineRevisions.Get(r.Context(), tenant, id, body.RevisionNumber)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "revision not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var snap storage.PipelineSnapshot
	if err := json.Unmarshal([]byte(target.Snapshot), &snap); err != nil {
		s.logger.Warn("deploy: snapshot decode failed",
			"err", err, "pipeline_id", id, "revision", body.RevisionNumber)
		writeError(w, http.StatusInternalServerError, "failed to decode revision snapshot")
		return
	}

	// 1. Atomic write-through.
	if err := s.applyRevisionLive(r.Context(), tenant, id, &snap); err != nil {
		s.logger.Warn("deploy: apply failed",
			"err", err, "pipeline_id", id, "revision", body.RevisionNumber)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 2. Mark the EXISTING revision deployed. No new revision is
	//    created — that's the spec's contract for /deploy (rollback
	//    creates a new one; deploy promotes an existing one).
	//    MarkDeployed is idempotent: re-deploying an already-deployed
	//    revision preserves the original deployed_at and only fills
	//    an empty deploy_request_id.
	requestID := RequestIDFromContext(r.Context())
	if err := s.store.PipelineRevisions.MarkDeployed(r.Context(), tenant, id, body.RevisionNumber, requestID); err != nil {
		s.logger.Warn("deploy: mark deployed failed",
			"err", err, "pipeline_id", id, "revision", body.RevisionNumber)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Re-read so the response carries fresh deployed_at /
	// deploy_request_id values.
	if refreshed, err := s.store.PipelineRevisions.Get(r.Context(), tenant, id, body.RevisionNumber); err == nil {
		target = refreshed
	}

	go s.reloadPipelines()

	writeJSON(w, http.StatusOK, s.makeRevisionResponse(target))
}

// authorFromCtx extracts (sub, preferred_username) from the request
// context for stamping onto a new revision row. Returns empties when
// the caller is unauthenticated — mutating handlers gate on
// RequireSession upstream so the empty case is defensive rather than
// load-bearing.
func authorFromCtx(ctx context.Context) (sub, username string) {
	if u, ok := auth.UserFromContext(ctx); ok && u != nil {
		return u.Sub, u.PreferredUsername
	}
	return "", ""
}
