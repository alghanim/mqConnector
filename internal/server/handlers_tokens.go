package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// handleListTokens returns every API token in the caller's tenant.
// Secrets are never returned — only the metadata + the saved prefix
// (for "which row is which" identification).
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	list, err := s.store.APITokens.List(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []*storage.APIToken{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// createTokenReq is the JSON shape POST /api/v1/tokens accepts. role
// defaults to the caller's role (the most restrictive sensible
// default); expires_at is optional.
type createTokenReq struct {
	Name      string  `json:"name"`
	Role      string  `json:"role,omitempty"`
	ExpiresIn *int    `json:"expires_in_seconds,omitempty"` // optional, relative
	ExpiresAt *string `json:"expires_at,omitempty"`         // optional, RFC3339
}

// handleCreateToken mints a new API token. The secret is shown EXACTLY
// ONCE in the response — after that only the prefix is recoverable.
// The token's role is upper-bounded by the caller's role (so an
// `operator` user can't mint an `admin` token).
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	claim, ok := auth.TenantFromContext(r.Context())
	if !ok || claim.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "no user in context")
		return
	}

	var req createTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Role == "" {
		req.Role = claim.Role
	}
	if !roleAtLeastAtLeast(claim.Role, req.Role) {
		writeError(w, http.StatusForbidden, "cannot mint a token at a role higher than your own")
		return
	}

	var expiresAt *time.Time
	if req.ExpiresIn != nil && *req.ExpiresIn > 0 {
		t := time.Now().UTC().Add(time.Duration(*req.ExpiresIn) * time.Second)
		expiresAt = &t
	} else if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad expires_at: "+err.Error())
			return
		}
		expiresAt = &parsed
	}

	secret, prefix, err := storage.GenerateSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tok := &storage.APIToken{
		TenantID:  claim.TenantID,
		UserSub:   user.Sub,
		Name:      req.Name,
		Prefix:    prefix,
		Role:      req.Role,
		ExpiresAt: expiresAt,
	}
	if err := s.store.APITokens.Create(r.Context(), tok, secret); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// One-shot return of the secret. Status 201 to signal creation.
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":   tok,
		"secret":  secret,
		"warning": "Copy this secret now — it cannot be retrieved later.",
	})
}

// handleRevokeToken marks a token as revoked. Idempotent — already-
// revoked rows still return 200 with status=revoked so the caller's
// retry doesn't need special-casing.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}
	err := s.store.APITokens.Revoke(r.Context(), tenant, id)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// roleAtLeastAtLeast is a tiny wrapper that ranks the four-role ladder
// the same way internal/auth does. Local copy to keep this file
// import-light (no auth-internal helper exposed).
func roleAtLeastAtLeast(have, want string) bool {
	rank := func(r string) int {
		switch r {
		case "viewer":
			return 0
		case "operator":
			return 1
		case "admin":
			return 2
		case "owner":
			return 3
		}
		return -1
	}
	h, n := rank(have), rank(want)
	return h >= 0 && h >= n
}
