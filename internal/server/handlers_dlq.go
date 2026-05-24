package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/dlq"
	"mqConnector/internal/logging"
	"mqConnector/internal/storage"
)

// handleListDLQ serves GET /api/v1/dlq with optional filters:
//
//	pipeline_id — exact match
//	error       — case-insensitive substring of error_reason
//	since       — RFC3339 lower bound on created_at
//	until       — RFC3339 upper bound (exclusive) on created_at
//	page, per_page — newest-first pagination
//
// Filter values that don't parse (e.g. malformed timestamps) are silently
// ignored rather than 400'd — the UI is the primary caller and degraded
// filtering is friendlier than a hard error on a typo.
func (s *Server) handleListDLQ(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	perPage, _ := strconv.Atoi(q.Get("per_page"))

	f := storage.DLQFilter{
		PipelineID: q.Get("pipeline_id"),
		Error:      q.Get("error"),
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = &t
		}
	}
	if v := q.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Until = &t
		}
	}

	tenant := auth.TenantID(r.Context())
	list, total, err := s.dlq.ListFiltered(r.Context(), tenant, f, page, perPage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []*storage.DLQEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page":     page,
		"per_page": perPage,
		"total":    total,
		"items":    list,
	})
}

func (s *Server) handleRetryDLQ(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.dlq.Retry(r.Context(), tenant, id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if errors.Is(err, dlq.ErrMaxRetries) {
			writeError(w, http.StatusBadRequest, "max retries exceeded")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "retried"})
}

func (s *Server) handleDeleteDLQ(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.dlq.Delete(r.Context(), tenant, id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleGetDLQRaw serves GET /api/v1/dlq/{id}/raw — the unredacted
// payload of a redacted DLQ row. Admin-gated at the route level
// (RequireRole("admin")), and every successful read is recorded as a
// dedicated audit-log entry with action="dlq_raw_view" so reviewers
// can correlate raw-payload access against incidents. The legacy
// AuditAdminActions middleware skips GETs by design (volume control),
// so we emit the audit entry inline here.
//
// Returns 204 No Content when the row exists but wasn't redacted —
// the caller already has the full payload from the list endpoint.
// Returns 503 Service Unavailable when the row is redacted but the
// sealer is no longer configured (operator removed the master key
// after redacted rows were written).
func (s *Server) handleGetDLQRaw(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	raw, err := s.dlq.GetRaw(r.Context(), tenant, id)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, dlq.ErrRawNotAvailable):
		// Successful look-up; the row wasn't redacted, so there's
		// nothing to disclose beyond what /api/v1/dlq already returns.
		w.WriteHeader(http.StatusNoContent)
		return
	case errors.Is(err, dlq.ErrSealerUnavailable):
		writeError(w, http.StatusServiceUnavailable,
			"master key not configured — cannot decrypt raw payload")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Audit the read explicitly. AuditAdminActions only records
	// state-changing verbs; raw payload access is a read with high
	// confidentiality impact and needs its own row.
	s.auditRawView(r, id)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// auditRawView records a dlq_raw_view audit entry. Best-effort with a
// short timeout — failure to audit must not break the response, but
// is logged so operators notice if the audit chain stops accepting
// raw-view rows.
func (s *Server) auditRawView(r *http.Request, dlqID string) {
	entry := storage.AuditEntry{
		At:        time.Now().UTC(),
		Action:    "dlq_raw_view",
		Resource:  "/api/v1/dlq/" + dlqID + "/raw",
		Status:    http.StatusOK,
		RequestID: RequestIDFromContext(r.Context()),
		RemoteIP:  clientIP(r),
	}
	if u, ok := auth.UserFromContext(r.Context()); ok && u != nil {
		entry.Actor = u.PreferredUsername
		entry.ActorSub = u.Sub
	}
	entry.TenantID = auth.TenantID(r.Context())

	go func(e storage.AuditEntry) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.store.Audit.Insert(ctx, &e); err != nil {
			logging.FromContext(context.Background()).Warn(
				"dlq raw-view audit insert failed",
				"err", err, "dlq_id", dlqID, "request_id", e.RequestID)
		}
	}(entry)
}

// handleListDLQRedactionRules serves GET /api/v1/pipelines/{id}/dlq-redaction-rules.
func (s *Server) handleListDLQRedactionRules(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	pipelineID := chi.URLParam(r, "id")
	rules, err := s.store.DLQRedaction.List(r.Context(), tenant, pipelineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rules == nil {
		rules = []storage.DLQRedactionRule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rules})
}

// handleReplaceDLQRedactionRules serves PUT /api/v1/pipelines/{id}/dlq-redaction-rules.
// Validates each rule's pattern up-front so a malformed regex or
// jsonpath is rejected at edit time rather than discovered on the
// next failure-path push. Refuses the write when the master key
// isn't configured — the rules would be a no-op anyway and the
// operator deserves to know.
func (s *Server) handleReplaceDLQRedactionRules(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	pipelineID := chi.URLParam(r, "id")

	var body struct {
		Items []storage.DLQRedactionRule `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if err := dlq.ValidateRules(body.Items); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Items) > 0 && (s.sealer == nil || !s.sealer.Enabled()) {
		writeError(w, http.StatusPreconditionFailed,
			"redaction rules require MQC_MASTER_KEY (envelope encryption) to be configured")
		return
	}
	if err := s.store.DLQRedaction.Replace(r.Context(), tenant, pipelineID, body.Items); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "pipeline not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "count": len(body.Items)})
}
