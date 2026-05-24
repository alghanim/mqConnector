package server

import (
	"encoding/json"
	"errors"
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
