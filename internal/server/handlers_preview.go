package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/storage"
)

// previewRequest is the JSON body for POST /api/v1/preview.
//
// Either of two modes:
//   - "Try this pipeline as-saved" — pass `pipeline_id` and leave stages
//     empty. The handler loads the stored stages, transforms, routing
//     rules, and schemas and runs them.
//   - "Try this draft" — pass `stages` (and optionally `transforms`,
//     `routing_rules`, `schemas`) inline. Useful from the editor while
//     the operator is iterating on a change they haven't saved yet.
//
// The sample message itself is always inline as `sample`.
type previewRequest struct {
	PipelineID   string                     `json:"pipeline_id,omitempty"`
	Stages       []*storage.Stage           `json:"stages,omitempty"`
	Transforms   []*storage.Transform       `json:"transforms,omitempty"`
	RoutingRules []*storage.RoutingRule     `json:"routing_rules,omitempty"`
	Schemas      map[string]*storage.Schema `json:"schemas,omitempty"`
	OutputFormat string                     `json:"output_format,omitempty"`
	Sample       string                     `json:"sample"`
}

// stageRunJSON mirrors pipeline.StageRun for the /preview response. It
// exists so the Studio dry-run dock can render a payload strip showing
// what each stage produced (or what input the failing stage saw). Body
// is encoded the same way as previewResponse.Output — a raw UTF-8
// string of the byte buffer — so the front-end uses one decode path
// for both. If Wave 2 ever needs to ship non-UTF8 bodies we can revisit
// with a transport-level encoding flag.
type stageRunJSON struct {
	Name       string `json:"name"`
	DurationNs int64  `json:"duration_ns"`
	Failed     bool   `json:"failed"`
	Body       string `json:"body,omitempty"`
	Format     string `json:"format,omitempty"`
	Err        string `json:"err,omitempty"`
}

type previewResponse struct {
	OK        bool           `json:"ok"`
	Output    string         `json:"output"`
	Format    string         `json:"format"`
	Routes    []string       `json:"routes,omitempty"`
	Error     string         `json:"error,omitempty"`
	StageRuns []stageRunJSON `json:"stage_runs,omitempty"`
}

// stageRunsJSON converts the pipeline-package observation log into the
// JSON shape served by /preview. Always called — operators want the
// per-stage strip even on failure, since the failure usually IS the
// thing they're trying to diagnose.
func stageRunsJSON(runs []pipeline.StageRun) []stageRunJSON {
	if len(runs) == 0 {
		return nil
	}
	out := make([]stageRunJSON, len(runs))
	for i, r := range runs {
		out[i] = stageRunJSON{
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

// handlePreview runs a sample message through a pipeline definition and
// returns what would be sent downstream. No brokers, no DLQ — purely
// in-process. This is the live "what does my filter do?" preview the
// 2024 prototype had at /api/filter, but for the full pipeline rather
// than just the filter stage.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if req.Sample == "" {
		writeError(w, http.StatusBadRequest, "sample is required")
		return
	}

	tenant := auth.TenantID(r.Context())
	var bctx pipeline.BuildContext
	if req.PipelineID != "" {
		// Load everything for this pipeline from storage — all calls
		// tenant-scoped so a draft preview can't peek at another
		// tenant's pipeline shape.
		p, err := s.store.Pipelines.Get(r.Context(), tenant, req.PipelineID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "pipeline not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		bctx.Pipeline = p
		bctx.StageRows, _ = s.store.Stages.ListByPipeline(r.Context(), tenant, req.PipelineID)
		bctx.Transforms, _ = s.store.Transforms.ListByPipeline(r.Context(), tenant, req.PipelineID)
		bctx.RoutingRules, _ = s.store.RoutingRules.ListByPipeline(r.Context(), tenant, req.PipelineID)
		// Schemas reachable from this pipeline are resolved by Build itself.
	} else {
		// Inline draft mode. We still need a non-nil Pipeline so Build can
		// consult its OutputFormat default for translate stages.
		bctx.Pipeline = &storage.Pipeline{OutputFormat: req.OutputFormat}
		bctx.StageRows = req.Stages
		bctx.Transforms = req.Transforms
		bctx.RoutingRules = req.RoutingRules
		bctx.Schemas = req.Schemas
	}

	stages, err := pipeline.Build(bctx)
	if err != nil {
		writeJSON(w, http.StatusOK, previewResponse{
			OK:    false,
			Error: "build: " + err.Error(),
		})
		return
	}

	outcome, err := pipeline.RunStages(r.Context(), stages, []byte(req.Sample))
	if err != nil {
		writeJSON(w, http.StatusOK, previewResponse{
			OK:        false,
			Error:     err.Error(),
			StageRuns: stageRunsJSON(outcome.Runs),
		})
		return
	}

	resp := previewResponse{
		OK:        true,
		Output:    string(outcome.Body),
		Format:    string(outcome.Format),
		StageRuns: stageRunsJSON(outcome.Runs),
	}
	if outcome.Route != nil {
		resp.Routes = outcome.Route.Destinations
	}
	writeJSON(w, http.StatusOK, resp)
}

// chi import sentinel — kept tiny so a future handler can grab the path
// param from r without re-importing.
var _ = chi.URLParam
