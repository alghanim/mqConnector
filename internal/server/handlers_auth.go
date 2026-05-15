package server

import (
	"net/http"

	"mqConnector/internal/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	access, refresh, err := s.auth.LoginWithRefresh(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	s.auth.SetCookie(w, access)
	if refresh != "" {
		s.auth.SetRefreshCookie(w, refresh)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.ClearCookie(w)
	s.auth.ClearRefreshCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRefresh extends the session by exchanging the refresh cookie for a
// fresh access+refresh pair. Public (no RequireSession): the access cookie
// may already have expired by the time the UI's silent refresh fires, and
// requiring it would make the endpoint useless for that case.
//
// Returns 401 on missing or rejected refresh — the UI then sends the user
// back to /login. The refresh cookie is cleared in that path so the browser
// doesn't keep retrying a stale token.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	rt := s.auth.RefreshCookieValue(r)
	if rt == "" {
		writeError(w, http.StatusUnauthorized, "no refresh cookie")
		return
	}
	access, refresh, err := s.auth.Refresh(r.Context(), rt)
	if err != nil {
		s.auth.ClearCookie(w)
		s.auth.ClearRefreshCookie(w)
		writeError(w, http.StatusUnauthorized, "refresh rejected")
		return
	}
	s.auth.SetCookie(w, access)
	s.auth.SetRefreshCookie(w, refresh)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		writeError(w, http.StatusUnauthorized, "no session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sub":                user.Sub,
		"preferred_username": user.PreferredUsername,
		"name":               user.Name,
		"email":              user.Email,
		"roles":              user.Roles,
	})
}
