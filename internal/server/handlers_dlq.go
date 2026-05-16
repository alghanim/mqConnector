package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/dlq"
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
