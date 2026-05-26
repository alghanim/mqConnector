package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/ai"
	"mqConnector/internal/auth"
	"mqConnector/internal/explain"
	"mqConnector/internal/logging"
)

// Wave 4 Task 1+2 — composable explainer engine + HTTP surface.
//
// GET /api/v1/explain/{subject}/{id}
//
// subject is one of: circuit | drift | latency | dlq_cluster | dlq_entry.
// id is the keying id appropriate for the subject (pipeline_id for
// the first three, fingerprint for dlq_cluster, entry id for
// dlq_entry).
//
// Auth: viewer (RequireSession in the route group). Tenant scope
// from auth.TenantID(ctx). RBAC at the route level only — the
// explanations carry no operator-only data.
//
// Optional ?ai=summary: when the AI subsystem is enabled and
// CapExplainWhySummary is allowed, the structured Explanation is
// passed to the LLM for a 2-sentence operator-language paraphrase.
// Failures degrade to ai_source="deterministic" with no
// ai_summary; the deterministic Explanation always lands first.

// explainResponse is the wire envelope. Embeds the Explanation so
// the wire shape stays flat for the deterministic fields; the
// optional AI sidecar lives in two separate fields.
type explainResponse struct {
	explain.Explanation
	// AISummary is the LLM-produced paraphrase. Empty when
	// ?ai=summary wasn't requested or the provider failed.
	AISummary string `json:"ai_summary,omitempty"`
	// AISource records what produced the summary: "ai" when the
	// LLM answered, "deterministic" when ?ai=summary was set but
	// the provider failed (and the UI should fall back to the
	// Headline as the summary). Empty when ?ai=summary wasn't
	// requested.
	AISource string `json:"ai_source,omitempty"`
}

// explainSubjects is the closed set of accepted subjects. Used
// for input validation before the engine dispatch so a malformed
// URL gets a clean 400 with the list of valid subjects.
var explainSubjects = map[string]bool{
	"circuit":     true,
	"drift":       true,
	"latency":     true,
	"dlq_cluster": true,
	"dlq_entry":   true,
}

// handleExplain serves GET /api/v1/explain/{subject}/{id}.
//
// Errors:
//   - 400 unknown subject
//   - 404 id not found for the subject (cross-tenant looks
//     identical to "doesn't exist" — no info leak)
//   - 500 anything else (logged warn; never panics)
func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenant := auth.TenantID(ctx)
	logger := logging.FromContext(ctx)

	subject := chi.URLParam(r, "subject")
	id := chi.URLParam(r, "id")
	if !explainSubjects[subject] {
		writeError(w, http.StatusBadRequest,
			"unknown subject; must be one of: circuit, drift, latency, dlq_cluster, dlq_entry")
		return
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}

	if s.explainEngine == nil {
		// Defence in depth — the engine is always built in New,
		// so this branch only fires in degenerate tests that
		// construct a bare Server. Still: better a 500 than a
		// nil-pointer panic.
		writeError(w, http.StatusInternalServerError, "explain engine not initialised")
		return
	}

	exp, err := s.explainEngine.Explain(ctx, subject, id, tenant)
	if err != nil {
		switch {
		case errors.Is(err, explain.ErrUnknownSubject):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, explain.ErrNotFound):
			writeError(w, http.StatusNotFound, "id not found for subject")
		default:
			logger.Warn("explain: engine error", "err", err, "subject", subject, "id", id)
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	resp := explainResponse{Explanation: exp}

	// Optional AI sidecar. Two preconditions: the query asked for
	// it AND the AI subsystem allows CapExplainWhySummary. A
	// disabled / unconfigured AI subsystem silently skips this
	// branch — the deterministic explanation still ships.
	if strings.EqualFold(r.URL.Query().Get("ai"), "summary") &&
		s.aiCfg.Allows(ai.CapExplainWhySummary) {
		summary, ok := s.runExplainAISummary(ctx, tenant, exp)
		if ok {
			resp.AISummary = summary
			resp.AISource = "ai"
		} else {
			resp.AISource = "deterministic"
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// runExplainAISummary asks the LLM provider for a 2-sentence
// operator paraphrase of the Explanation. Returns (summary, true)
// on success, ("", false) on any failure — the handler maps the
// outcome to ai_source. Best-effort: a provider error is logged
// at warn but never bubbles up.
func (s *Server) runExplainAISummary(ctx context.Context, tenant string, exp explain.Explanation) (string, bool) {
	logger := logging.FromContext(ctx)
	if s.aiProvider == nil {
		return "", false
	}

	// Compact serialisation — the LLM doesn't need pretty JSON.
	payload, err := json.Marshal(exp)
	if err != nil {
		logger.Warn("explain: ai summary marshal failed", "err", err)
		return "", false
	}

	// Stamp caller + tenant onto the AI ctx so the audit row
	// records them. Mirrors the dlq-cluster naming wiring.
	var callerSub string
	if u, ok := auth.UserFromContext(ctx); ok && u != nil {
		callerSub = u.Sub
	}
	aiCtx := ai.WithTenant(ai.WithCaller(ctx, callerSub), tenant)

	const system = `You are a senior site-reliability engineer. ` +
		`Given a structured Explanation document from an operations system, ` +
		`produce a two-sentence paraphrase in plain operator language. ` +
		`Rules: at most two sentences, no markdown, no code fences, no JSON. ` +
		`Do not invent numbers — only paraphrase what the Explanation contains.`

	res, err := s.aiProvider.Complete(aiCtx, ai.CompletionRequest{
		Feature:     ai.CapExplainWhySummary,
		System:      system,
		User:        string(payload),
		MaxTokens:   180,
		Temperature: 0.2,
	})
	if err != nil {
		logger.Warn("explain: ai summary provider failed",
			"err", err, "subject", exp.Subject, "id", exp.ID)
		return "", false
	}
	summary := strings.TrimSpace(res.Text)
	if summary == "" {
		return "", false
	}
	return summary, true
}
