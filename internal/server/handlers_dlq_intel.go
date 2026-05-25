package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/storage"
)

// The DLQ Intelligence Console handlers (Wave 3 — Task 3).
//
// Three endpoints power the operator-facing triage UI:
//
//   - GET  /api/v1/dlq/clusters         — fingerprint-aggregated rollups
//   - POST /api/v1/dlq/{id}/replay-sim  — preview replay against the live
//                                          deployed revision (no broker sends)
//   - GET  /api/v1/dlq/{id}/diff        — side-by-side payload diff
//
// Auth/tenant plumbing mirrors handlers_dlq.go: every handler takes the
// tenant from auth.TenantID(ctx) and 404s rather than 403s on cross-tenant
// reads (no information leak).

// dlqClustersDefaultLimit / dlqClustersMaxLimit constrain how many cluster
// rows the rollup endpoint will return per call. The default matches the
// UI's first-page render budget; the hard cap is the N+1 safety net (each
// cluster fans out into 3 follow-up queries, see handleListDLQClusters).
const (
	dlqClustersDefaultLimit = 50
	dlqClustersMaxLimit     = 200
	dlqClusterRecentIDLimit = 5
)

// dlqCluster is one fingerprint-bucket row of the rollup response. See
// handleListDLQClusters for the SQL and how the per-cluster follow-up
// queries (pipelines/stages/recent) are populated.
type dlqCluster struct {
	Fingerprint       string    `json:"fingerprint"`
	Template          string    `json:"template"`
	Count             int       `json:"count"`
	FirstSeen         time.Time `json:"first_seen"`
	LastSeen          time.Time `json:"last_seen"`
	PipelinesAffected []string  `json:"pipelines_affected"`
	FailingStages     []string  `json:"failing_stages"`
	RepresentativeID  string    `json:"representative_id"`
	RecentIDs         []string  `json:"recent_ids"`
}

// clustersResponse is the wire envelope for GET /api/v1/dlq/clusters.
// Returned and Total currently mirror each other — the SQL HAVING +
// LIMIT already filter out everything we don't ship — but the wire
// shape keeps both so a future "exclude clusters with zero pipelines"
// post-filter (or paginating Total over multiple pages) doesn't
// require a wire-shape change.
type clustersResponse struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Total       int          `json:"total"`
	Returned    int          `json:"returned"`
	Clusters    []dlqCluster `json:"clusters"`
}

// handleListDLQClusters serves GET /api/v1/dlq/clusters.
//
// SQL strategy: one GROUP BY scan over the fingerprint column gives us
// the top-N clusters; per-cluster follow-up queries materialise the
// distinct pipeline_ids, failing_stage_names, and the 5 most-recent
// entry ids. N+1 is bounded by dlqClustersMaxLimit (200) and tenant
// DLQs are typically small — if this becomes a hotspot, swap the per-
// cluster fan-out for a single SQL using SQLite's GROUP_CONCAT.
//
// Empty-fingerprint rows (legacy data written before migration 0023,
// plus send-side failures where the executor couldn't attribute a
// stage) are excluded here; they remain reachable via the regular
// list endpoint.
//
// Query params:
//   - pipeline_id — exact match
//   - since       — RFC3339 lower bound on created_at (malformed → ignored)
//   - limit       — max clusters (default 50, hard cap 200)
//   - min_count   — exclude clusters with fewer entries (default 1)
//
// RBAC: viewer (route-level RequireSession only).
func (s *Server) handleListDLQClusters(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())

	q := r.URL.Query()
	f := storage.DLQClusterFilter{PipelineID: q.Get("pipeline_id")}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = &t
		}
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = dlqClustersDefaultLimit
	}
	if limit > dlqClustersMaxLimit {
		limit = dlqClustersMaxLimit
	}
	f.Limit = limit
	minCount, _ := strconv.Atoi(q.Get("min_count"))
	if minCount < 1 {
		minCount = 1
	}
	f.MinCount = minCount

	rawClusters, err := s.store.DLQ.ListClusters(r.Context(), tenant, f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	clusters := make([]dlqCluster, 0, len(rawClusters))
	for _, raw := range rawClusters {
		c := dlqCluster{
			Fingerprint: raw.Fingerprint,
			Template:    raw.Template,
			Count:       raw.Count,
			FirstSeen:   raw.FirstSeen,
			LastSeen:    raw.LastSeen,
		}
		if err := s.fillClusterDetails(r.Context(), tenant, &c); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		clusters = append(clusters, c)
	}

	writeJSON(w, http.StatusOK, clustersResponse{
		GeneratedAt: time.Now().UTC(),
		Total:       len(clusters),
		Returned:    len(clusters),
		Clusters:    clusters,
	})
}

// fillClusterDetails runs the three per-cluster follow-up queries via
// the DLQRepo: distinct pipelines, distinct failing stages, the N most-
// recent entry ids, and the oldest entry id (representative). Empty
// slices are guaranteed (never nil) so the wire shape is always `[]`.
func (s *Server) fillClusterDetails(ctx context.Context, tenant string, c *dlqCluster) error {
	pipes, err := s.store.DLQ.ClusterPipelines(ctx, tenant, c.Fingerprint)
	if err != nil {
		return err
	}
	// Strip the empty pipeline_id (bridge endpoint failures).
	clean := pipes[:0]
	for _, p := range pipes {
		if p != "" {
			clean = append(clean, p)
		}
	}
	c.PipelinesAffected = clean
	if c.PipelinesAffected == nil {
		c.PipelinesAffected = []string{}
	}

	stages, err := s.store.DLQ.ClusterFailingStages(ctx, tenant, c.Fingerprint)
	if err != nil {
		return err
	}
	c.FailingStages = stages
	if c.FailingStages == nil {
		c.FailingStages = []string{}
	}

	recent, err := s.store.DLQ.ClusterRecentIDs(ctx, tenant, c.Fingerprint, dlqClusterRecentIDLimit)
	if err != nil {
		return err
	}
	c.RecentIDs = recent
	if c.RecentIDs == nil {
		c.RecentIDs = []string{}
	}

	rep, err := s.store.DLQ.ClusterRepresentative(ctx, tenant, c.Fingerprint)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	c.RepresentativeID = rep
	return nil
}

// replaySimResponse is the wire shape for POST /api/v1/dlq/{id}/replay-sim.
// Mirrors previewResponse but adds DLQ-specific context (entry_id,
// pipeline_id, revision_number) so the UI can render the simulation
// alongside the DLQ entry without a second lookup.
type replaySimResponse struct {
	EntryID        string                  `json:"entry_id"`
	PipelineID     string                  `json:"pipeline_id"`
	RevisionNumber int                     `json:"revision_number"`
	WouldSucceed   bool                    `json:"would_succeed"`
	StageRuns      []replaySimStageRunJSON `json:"stage_runs"`
	FinalOutput    string                  `json:"final_output,omitempty"`
	Format         string                  `json:"format,omitempty"`
	Error          string                  `json:"error,omitempty"`
	FailingStage   string                  `json:"failing_stage,omitempty"`
}

// replaySimStageRunJSON is the per-stage observation. Field-for-field
// identical to stageRunJSON in handlers_preview.go — kept as a separate
// type so the two response shapes can evolve independently (preview
// may grow request-side hints; replay-sim may grow DLQ-side ones).
type replaySimStageRunJSON struct {
	Name       string `json:"name"`
	DurationNs int64  `json:"duration_ns"`
	Failed     bool   `json:"failed"`
	Body       string `json:"body,omitempty"`
	Format     string `json:"format,omitempty"`
	Err        string `json:"err,omitempty"`
}

// handleReplaySimDLQ serves POST /api/v1/dlq/{id}/replay-sim.
//
// Re-runs the entry's original payload through the pipeline's current
// DEPLOYED revision in preview mode (no broker sends, no mutation). The
// operator uses this to decide whether a retry would now succeed — e.g.
// after a fix to a transform stage — without actually firing the
// message at the destination.
//
// RBAC: operator (gates inline below). Viewer is rejected 403.
//
// Errors:
//   - 403 caller lacks operator role on the pipeline
//   - 404 DLQ entry not found / cross-tenant
//   - 404 the entry has no pipeline_id (nothing to simulate against)
//   - 409 pipeline has no deployed revision yet
//   - 500 snapshot decode failure (logged, surfaced as plain envelope)
//
// Build failures (e.g. validate stage that references a missing
// schema, deleted source/destination not relevant here since we don't
// send) are surfaced via the response's Error field with a 200 — same
// pattern as the /preview handler — so the operator sees exactly what
// the executor would see at boot time.
func (s *Server) handleReplaySimDLQ(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")

	entry, err := s.store.DLQ.Get(r.Context(), tenant, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "dlq entry not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entry.PipelineID == "" {
		writeError(w, http.StatusNotFound, "dlq entry has no pipeline_id; nothing to simulate")
		return
	}

	// Operator gate on the entry's pipeline. gatePipeline returns
	// false after writing the 403/404 envelope itself.
	if !s.gatePipeline(w, r, entry.PipelineID, storage.RoleOperator) {
		return
	}

	rev, err := s.store.PipelineRevisions.LatestDeployed(r.Context(), tenant, entry.PipelineID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusConflict,
				"pipeline has no deployed revision; cannot simulate")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var snap storage.PipelineSnapshot
	if err := json.Unmarshal([]byte(rev.Snapshot), &snap); err != nil {
		s.logger.Warn("replay-sim: snapshot decode failed",
			"err", err, "pipeline_id", entry.PipelineID, "revision", rev.RevisionNumber)
		writeError(w, http.StatusInternalServerError, "failed to decode revision snapshot")
		return
	}

	resp := s.simulateReplay(r.Context(), entry, rev.RevisionNumber, &snap)
	writeJSON(w, http.StatusOK, resp)
}

// simulateReplay builds the snapshot's stage chain and runs the
// entry's payload through it in preview mode (no brokers touched).
// Extracted from handleReplaySimDLQ to keep the handler under the
// 80-line budget and to let any future caller (e.g. a CLI dry-run)
// reuse the same logic.
func (s *Server) simulateReplay(
	ctx context.Context,
	entry *storage.DLQEntry,
	revisionNumber int,
	snap *storage.PipelineSnapshot,
) replaySimResponse {
	// Mirror handlers_preview.go's pipeline_id-mode wiring. Schemas
	// are left empty: a validate-with-schema stage in the snapshot
	// would fail Build the same way preview does, surfacing via the
	// Error field rather than a 5xx — the operator sees exactly what
	// the executor sees.
	bctx := pipeline.BuildContext{
		Pipeline:     snap.Pipeline,
		StageRows:    snap.Stages,
		Transforms:   snap.Transforms,
		RoutingRules: snap.RoutingRules,
	}
	stages, err := pipeline.Build(bctx)
	if err != nil {
		return replaySimResponse{
			EntryID:        entry.ID,
			PipelineID:     entry.PipelineID,
			RevisionNumber: revisionNumber,
			WouldSucceed:   false,
			StageRuns:      []replaySimStageRunJSON{},
			Error:          "build: " + err.Error(),
		}
	}

	// Use the redacted form (OriginalMsg). Per the spec, admin-only
	// raw access is gated separately; replay-sim doesn't reach into
	// the sealed raw_msg even when the row was redacted.
	outcome, runErr := pipeline.RunStages(ctx, stages, entry.OriginalMsg)
	resp := replaySimResponse{
		EntryID:        entry.ID,
		PipelineID:     entry.PipelineID,
		RevisionNumber: revisionNumber,
		WouldSucceed:   runErr == nil,
		StageRuns:      replaySimStageRunsJSON(outcome.Runs),
	}
	if runErr != nil {
		resp.Error = runErr.Error()
		// FailingStage is the last run's name when the chain
		// errored — RunStages always appends the failing stage to
		// Runs before returning. Defensive guard for the
		// pathological "no stages and immediate error" case.
		if n := len(outcome.Runs); n > 0 {
			resp.FailingStage = outcome.Runs[n-1].Name
		}
		return resp
	}
	resp.FinalOutput = string(outcome.Body)
	resp.Format = string(outcome.Format)
	return resp
}

// replaySimStageRunsJSON converts the pipeline-package observation log
// into the JSON shape served by /replay-sim. Mirrors stageRunsJSON in
// handlers_preview.go but always returns a non-nil slice so the wire
// shape is `[]` and never `null`.
func replaySimStageRunsJSON(runs []pipeline.StageRun) []replaySimStageRunJSON {
	if len(runs) == 0 {
		return []replaySimStageRunJSON{}
	}
	out := make([]replaySimStageRunJSON, len(runs))
	for i, r := range runs {
		out[i] = replaySimStageRunJSON{
			Name:       r.Name,
			DurationNs: r.Duration.Nanoseconds(),
			Failed:     r.Failed,
			Body:       string(r.Body),
			Format:     string(r.Format),
			Err:        r.Err,
		}
	}
	return out
}

// dlqDiffSide is one side of the payload diff response. Mirrors enough
// of the DLQ entry for the Studio diff viewer to render the row
// metadata without a second fetch.
type dlqDiffSide struct {
	ID          string    `json:"id"`
	PipelineID  string    `json:"pipeline_id"`
	CreatedAt   time.Time `json:"created_at"`
	ErrorReason string    `json:"error_reason"`
	Fingerprint string    `json:"fingerprint"`
	Template    string    `json:"template"`
	Body        string    `json:"body"`
	Format      string    `json:"format"`
}

// dlqLineOp is one operation in the line-diff output. Op is "eq" |
// "add" | "del" so the front-end can render gutter colours directly.
type dlqLineOp struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// dlqDiffResponse is the wire envelope for GET /api/v1/dlq/{id}/diff.
// From → To direction matches the URL convention (`{id}` is the base,
// `against=` is the target).
type dlqDiffResponse struct {
	From dlqDiffSide `json:"from"`
	To   dlqDiffSide `json:"to"`
	Diff []dlqLineOp `json:"diff"`
}

// handleDiffDLQ serves GET /api/v1/dlq/{id}/diff?against={other_id}.
//
// Side-by-side payload diff between two DLQ entries. Useful for "are
// these the same failure or did the payload shape drift?" triage. The
// `against` query parameter is required; passing the same id is OK
// (returns an all-eq diff).
//
// RBAC: viewer.
//
// Errors:
//   - 400 missing `against=` query parameter
//   - 404 either id not found / cross-tenant
//   - 500 unexpected DB error
func (s *Server) handleDiffDLQ(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	against := r.URL.Query().Get("against")
	if against == "" {
		writeError(w, http.StatusBadRequest, "missing required query parameter: against")
		return
	}

	from, err := s.fetchDLQEntryForDiff(w, r, tenant, id)
	if err != nil {
		return
	}
	to, err := s.fetchDLQEntryForDiff(w, r, tenant, against)
	if err != nil {
		return
	}

	fromBody := string(from.OriginalMsg)
	toBody := string(to.OriginalMsg)
	writeJSON(w, http.StatusOK, dlqDiffResponse{
		From: makeDiffSide(from, fromBody),
		To:   makeDiffSide(to, toBody),
		Diff: lcsLineDiff(fromBody, toBody),
	})
}

// fetchDLQEntryForDiff loads one entry tenant-scoped and writes the
// canonical 404/500 envelope on error. Returns (entry, nil) on
// success, (_, err) on failure with the response already flushed.
func (s *Server) fetchDLQEntryForDiff(
	w http.ResponseWriter,
	r *http.Request,
	tenant, id string,
) (*storage.DLQEntry, error) {
	entry, err := s.store.DLQ.Get(r.Context(), tenant, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "dlq entry not found")
			return nil, err
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, err
	}
	return entry, nil
}

// makeDiffSide projects a DLQ entry into the diff response shape.
func makeDiffSide(e *storage.DLQEntry, body string) dlqDiffSide {
	return dlqDiffSide{
		ID:          e.ID,
		PipelineID:  e.PipelineID,
		CreatedAt:   e.CreatedAt,
		ErrorReason: e.ErrorReason,
		Fingerprint: e.ErrorFingerprint,
		Template:    e.ErrorTemplate,
		Body:        body,
		Format:      guessPayloadFormat(body),
	}
}

// guessPayloadFormat sniffs a body string and returns "json" | "xml" |
// "text". Mirrors the heuristic the Studio's PayloadDiffView.svelte
// uses on the front-end so the badge sticks regardless of which side
// of the wire renders it. Conservative: doesn't try to parse XML
// (cost outweighs benefit for a UI badge), but JSON validity is
// genuinely cheap.
func guessPayloadFormat(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return "text"
	}
	if json.Valid([]byte(trimmed)) {
		return "json"
	}
	if strings.HasPrefix(trimmed, "<?xml") || strings.HasPrefix(trimmed, "<") {
		return "xml"
	}
	return "text"
}

// lcsLineDiff returns the line-level edit script transforming `a` into
// `b`, encoded as a flat list of {op, text} operations in order. Hand-
// rolled LCS over the line vectors — same algorithm Studio's
// PayloadDiffView.svelte uses on the front-end, ported to Go so both
// surfaces produce identical output.
//
// Op encoding: "eq" lines exist in both inputs at the same relative
// position; "del" lines exist only in `a`; "add" lines exist only in
// `b`. The result interleaves "del" at the pre-removal position
// rather than collapsing into a separate section — that's what the
// strikethrough renderer wants.
//
// Complexity is O(len(a) * len(b)) for both time and space. DLQ
// payloads are typically small (a few KB); if this becomes a hotspot
// we can swap in Myers diff (O(ND)) without changing the wire shape.
func lcsLineDiff(a, b string) []dlqLineOp {
	aLines := splitLines(a)
	bLines := splitLines(b)
	m, n := len(aLines), len(bLines)
	if m == 0 && n == 0 {
		return []dlqLineOp{}
	}

	// LCS suffix table. dp[i][j] = length of the LCS between
	// aLines[i:] and bLines[j:]. Suffix-based so the walk emits lines
	// in natural top-to-bottom order without reversing.
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if aLines[i] == bLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	// Walk the table. At each cell, emit eq (match), del (advance
	// `a` only), or add (advance `b` only). Tie-break favours del-
	// before-add so a "swap one line" change reads naturally as
	// `-old / +new`.
	out := make([]dlqLineOp, 0, m+n)
	i, j := 0, 0
	for i < m && j < n {
		switch {
		case aLines[i] == bLines[j]:
			out = append(out, dlqLineOp{Op: "eq", Text: aLines[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, dlqLineOp{Op: "del", Text: aLines[i]})
			i++
		default:
			out = append(out, dlqLineOp{Op: "add", Text: bLines[j]})
			j++
		}
	}
	for ; i < m; i++ {
		out = append(out, dlqLineOp{Op: "del", Text: aLines[i]})
	}
	for ; j < n; j++ {
		out = append(out, dlqLineOp{Op: "add", Text: bLines[j]})
	}
	return out
}

// splitLines returns the input split on "\n". A trailing newline does
// NOT produce a phantom empty line — operators are diffing payload
// bodies, not source code, and a missing final newline is normal.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	out := strings.Split(s, "\n")
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
