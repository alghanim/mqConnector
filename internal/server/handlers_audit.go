package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
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

	// Tenant-scoped: a regular member sees only their tenant's audit.
	// (System-admin "see everything" comes in Phase 16b — for now the
	// owner of the default tenant gets the closest approximation.)
	tenant := auth.TenantID(r.Context())
	list, total, err := s.store.Audit.List(r.Context(), tenant, f, page, perPage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []*storage.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page":     page,
		"per_page": perPage,
		"total":    total,
		"items":    list,
	})
}

// handleVerifyAudit walks the tamper-evident hash chain and reports any
// row where the recomputed hash diverges from the stored one. Tenant-
// scoped: a regular member verifies only their tenant's chain. A
// system-admin (owner of the default tenant) can pass ?scope=all to
// verify every tenant.
func (s *Server) handleVerifyAudit(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	scope := r.URL.Query().Get("scope")
	if scope == "all" {
		// Coarse system-admin check: only the owner of the default
		// tenant gets cross-tenant visibility. Aligns with the policy
		// used in handleListAudit's future expansion.
		if tenant == storage.DefaultTenantID {
			tenant = "*"
		}
	}

	statuses, err := s.store.Audit.Verify(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	overall := "ok"
	for _, st := range statuses {
		if st.Status != "ok" {
			overall = "broken"
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  overall,
		"tenants": statuses,
	})
}

// handleGetAuditDiff returns the before/after JSON snapshot recorded
// for one audit row. PUT mutations get a diff; other actions don't.
// Returns 404 if no diff was recorded.
func (s *Server) handleGetAuditDiff(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}
	diff, err := s.store.Audit.GetDiff(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "no diff for this row")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, diff)
}
