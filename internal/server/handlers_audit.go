package server

import (
	"net/http"
	"strconv"
	"time"

	"mqConnector/internal/storage"
)

// handleListAudit GETs the audit log with optional filters.
//
// Query params:
//   actor    — exact preferred_username match
//   resource — prefix match against the recorded path
//   since    — RFC3339 timestamp (entries >= since)
//   until    — RFC3339 timestamp (entries <= until)
//   page, per_page — pagination, newest-first
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := storage.AuditFilter{
		Actor:    q.Get("actor"),
		Resource: q.Get("resource"),
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
	page, _ := strconv.Atoi(q.Get("page"))
	perPage, _ := strconv.Atoi(q.Get("per_page"))

	list, total, err := s.store.Audit.List(r.Context(), f, page, perPage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page":     page,
		"per_page": perPage,
		"total":    total,
		"items":    list,
	})
}
