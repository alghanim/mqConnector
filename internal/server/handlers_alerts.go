package server

import (
	"net/http"
	"strings"
	"time"

	"mqConnector/internal/logging"
	"mqConnector/internal/slo"
)

// Wave 4 Task 4 — currently-firing SLO alerts.
//
// GET /api/v1/alerts/active
//
// Reads the in-process slo.Evaluator's snapshot and renders it for
// the frontend AlertRibbon + /observability/alerts page. The
// evaluator parses the same Prometheus rules YAML the operator's
// Prometheus consumes, so there's no rule drift between the
// in-binary view and the cluster's real Alertmanager.
//
// RBAC: viewer (any authenticated session). Tenant scope: alerts are
// GLOBAL today — the project's rule labels are pipeline-keyed but
// not tenant-keyed, so every authenticated user in the tenant gets
// the same view. When tenant_id eventually lands on labels, this
// handler will gain a tenant filter at the same place the
// severity/pipeline filters live.
//
// Query parameters (all optional, AND-combined):
//
//	severity=warning,critical   keep only alerts whose severity
//	                            label is in the comma-sep set
//	pipeline=p1                 keep only alerts whose labels carry
//	                            pipeline_id=p1
//
// Wire stability: the response shape is consumed by the frontend
// AlertRibbon (web/src/lib/components/AlertRibbon.svelte). Field
// additions are non-breaking; renames break the UI.

type alertsResponse struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Total       int               `json:"total"`
	Alerts      []slo.FiringAlert `json:"alerts"`
	// EvaluatorEnabled is false when the binary started without a
	// rules file — the UI uses it to suppress "no alerts" framing
	// in favour of "alerting not configured".
	EvaluatorEnabled bool `json:"evaluator_enabled"`
}

func (s *Server) handleListActiveAlerts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	if s.sloEvaluator == nil {
		// Evaluator was disabled at startup (no rules file). Return
		// a 200 with an empty list + evaluator_enabled=false so the
		// frontend can distinguish "all good" from "not configured".
		writeJSON(w, http.StatusOK, alertsResponse{
			GeneratedAt:      time.Now().UTC(),
			Total:            0,
			Alerts:           []slo.FiringAlert{},
			EvaluatorEnabled: false,
		})
		return
	}

	all := s.sloEvaluator.Snapshot()

	// severity filter — comma-separated.
	if raw := strings.TrimSpace(r.URL.Query().Get("severity")); raw != "" {
		want := map[string]bool{}
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(strings.ToLower(s))
			if s != "" {
				want[s] = true
			}
		}
		filtered := make([]slo.FiringAlert, 0, len(all))
		for _, a := range all {
			if want[strings.ToLower(a.Severity)] {
				filtered = append(filtered, a)
			}
		}
		all = filtered
	}

	// pipeline_id filter.
	if pid := strings.TrimSpace(r.URL.Query().Get("pipeline")); pid != "" {
		filtered := make([]slo.FiringAlert, 0, len(all))
		for _, a := range all {
			if a.Labels["pipeline_id"] == pid {
				filtered = append(filtered, a)
			}
		}
		all = filtered
	}

	logger.Debug("alerts: snapshot served",
		"total", len(all), "evaluator_rules", s.sloEvaluator.RuleCount())

	writeJSON(w, http.StatusOK, alertsResponse{
		GeneratedAt:      time.Now().UTC(),
		Total:            len(all),
		Alerts:           all,
		EvaluatorEnabled: true,
	})
}
