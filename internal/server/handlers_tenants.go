package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// Tenant endpoints follow a three-tier authorization model:
//
//   - GET /api/v1/tenants                — the user's own memberships
//   - POST /api/v1/tenants               — system-admin only (owner of
//                                           the default tenant)
//   - GET /api/v1/tenants/{id}           — must be a member of {id}
//   - PUT/DELETE /api/v1/tenants/{id}    — must be owner of {id}
//                                           (delete also blocked on the
//                                           default tenant)
//   - GET/POST/PUT/DELETE
//       /api/v1/tenants/{id}/members     — must be owner of {id}
//   - POST /api/v1/tenants/{id}/switch   — must be a member; sets the
//                                           mqc_active_tenant cookie
//
// "System-admin" here means the user is the owner of the seeded
// default tenant. That's the bootstrap account; deployments without
// such a user simply can't create new tenants until one is granted.

func (s *Server) handleListMyTenants(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	username := user.PreferredUsername
	if username == "" {
		username = user.Name
	}
	ms, err := s.store.Memberships.ListByUser(r.Context(), user.Sub, strings.ToLower(username))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		t, err := s.store.Tenants.Get(r.Context(), m.TenantID)
		if err != nil {
			continue // ignore orphan memberships
		}
		out = append(out, map[string]any{
			"tenant":  t,
			"role":    m.Role,
			"is_active": currentTenantID(r) == m.TenantID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func currentTenantID(r *http.Request) string {
	c, _ := auth.TenantFromContext(r.Context())
	return c.TenantID
}

// handleCreateTenant requires the caller to be an owner of the default
// tenant (i.e. a system-level admin).
func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	if !s.isSystemAdmin(r) {
		writeError(w, http.StatusForbidden, "system-admin required")
		return
	}
	var t storage.Tenant
	if err := decodeJSON(r, &t); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if t.Slug == "" || t.Name == "" {
		writeError(w, http.StatusBadRequest, "slug and name are required")
		return
	}
	if err := s.store.Tenants.Create(r.Context(), &t); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Auto-add the creator as owner of the new tenant. Without this the
	// creator can't act inside it — the resolver would fall back to
	// their other memberships and writes would land in the wrong tenant.
	if user, ok := auth.UserFromContext(r.Context()); ok && user != nil {
		username := user.PreferredUsername
		if username == "" {
			username = user.Name
		}
		_ = s.store.Memberships.Upsert(r.Context(), &storage.Membership{
			TenantID: t.ID,
			UserSub:  user.Sub,
			Username: username,
			Role:     storage.RoleOwner,
		})
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.isMemberOf(r, id) {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}
	t, err := s.store.Tenants.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.isOwnerOf(r, id) {
		writeError(w, http.StatusForbidden, "owner required")
		return
	}
	var t storage.Tenant
	if err := decodeJSON(r, &t); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	t.ID = id
	if err := s.store.Tenants.Update(r.Context(), &t); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == storage.DefaultTenantID {
		writeError(w, http.StatusBadRequest, "cannot delete the default tenant")
		return
	}
	if !s.isOwnerOf(r, id) {
		writeError(w, http.StatusForbidden, "owner required")
		return
	}
	if err := s.store.Tenants.Delete(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleSwitchTenant sets the active-tenant cookie. Verifies membership
// first so the user can't "switch" into a tenant they don't belong to.
func (s *Server) handleSwitchTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.isMemberOf(r, id) {
		writeError(w, http.StatusForbidden, "not a member of this tenant")
		return
	}
	// The cookie's Secure flag should mirror the session cookie's. The
	// auth service holds that fact; for simplicity we set it Secure
	// based on the request's TLS state.
	http.SetCookie(w, &http.Cookie{
		Name:     "mqc_active_tenant",
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   30 * 24 * 60 * 60, // 30 days
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "switched", "tenant_id": id})
}

// ─── members ───────────────────────────────────────────────────────

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.isOwnerOf(r, id) {
		writeError(w, http.StatusForbidden, "owner required")
		return
	}
	ms, err := s.store.Memberships.ListByTenant(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, ms)
}

type memberDTO struct {
	UserSub  string `json:"user_sub"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func (s *Server) handleUpsertMember(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.isOwnerOf(r, id) {
		writeError(w, http.StatusForbidden, "owner required")
		return
	}
	var dto memberDTO
	if err := decodeJSON(r, &dto); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if dto.UserSub == "" {
		writeError(w, http.StatusBadRequest, "user_sub is required")
		return
	}
	role := storage.Role(dto.Role)
	if !role.Valid() {
		writeError(w, http.StatusBadRequest, "role must be one of viewer|operator|admin|owner")
		return
	}
	m := &storage.Membership{
		TenantID: id,
		UserSub:  dto.UserSub,
		Username: dto.Username,
		Role:     role,
	}
	if err := s.store.Memberships.Upsert(r.Context(), m); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleDeleteMember(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sub := chi.URLParam(r, "user_sub")
	if !s.isOwnerOf(r, id) {
		writeError(w, http.StatusForbidden, "owner required")
		return
	}
	// Self-protection: refuse to remove the last owner of a tenant.
	// Otherwise the tenant becomes unmanageable.
	all, _ := s.store.Memberships.ListByTenant(r.Context(), id)
	owners := 0
	for _, m := range all {
		if m.Role == storage.RoleOwner {
			owners++
		}
	}
	for _, m := range all {
		if m.UserSub == sub && m.Role == storage.RoleOwner && owners == 1 {
			writeError(w, http.StatusBadRequest, "cannot remove the last owner")
			return
		}
	}
	if err := s.store.Memberships.Delete(r.Context(), id, sub); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "membership not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// ─── authorization helpers ─────────────────────────────────────────

// isSystemAdmin reports whether the caller is an owner of the default
// tenant. Used to gate tenant creation.
func (s *Server) isSystemAdmin(r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		return false
	}
	m, err := s.store.Memberships.Get(r.Context(), storage.DefaultTenantID, user.Sub)
	if err != nil {
		return false
	}
	return m.Role == storage.RoleOwner
}

// isMemberOf reports whether the caller has any membership in the
// named tenant. The default-tenant owner (system admin) is implicitly
// a member of every tenant for read-only purposes.
func (s *Server) isMemberOf(r *http.Request, tenantID string) bool {
	if s.isSystemAdmin(r) {
		return true
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		return false
	}
	_, err := s.store.Memberships.Get(r.Context(), tenantID, user.Sub)
	return err == nil
}

// isOwnerOf reports whether the caller is the owner of the named
// tenant. The default-tenant owner is implicitly owner of every
// tenant — the on-prem operator's master key.
func (s *Server) isOwnerOf(r *http.Request, tenantID string) bool {
	if s.isSystemAdmin(r) {
		return true
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		return false
	}
	m, err := s.store.Memberships.Get(r.Context(), tenantID, user.Sub)
	if err != nil {
		return false
	}
	return m.Role == storage.RoleOwner
}
