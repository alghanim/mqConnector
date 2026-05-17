package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// csrfCookieName is the non-HttpOnly cookie that carries the CSRF
// double-submit token. The SPA reads it from document.cookie and
// echoes it in X-CSRF-Token on every state-changing request.
const csrfCookieName = "mqc_csrf"

// csrfHeaderName is the header the SPA echoes the token in.
const csrfHeaderName = "X-CSRF-Token"

// EnsureCSRFCookie sets a fresh CSRF token cookie if the request
// doesn't already carry one. Called from handlers that establish a
// session (login, refresh) so the SPA has a token to read before
// making its first state-changing call. The cookie is intentionally
// NOT HttpOnly — the browser-side JS needs to read it for the
// double-submit pattern to work.
//
// Secure mirrors the session cookie's Secure flag (false in dev/
// http, true in prod/https). SameSite=Strict means the token is
// only sent on same-origin requests, which is the property that
// makes "attacker echoes a cookie value they can't read" infeasible.
func (s *Server) EnsureCSRFCookie(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(csrfCookieName); err == nil && c.Value != "" {
		return
	}
	token, err := generateCSRFToken()
	if err != nil {
		// Refusing to set a cookie is preferable to setting a
		// predictable one. The next request after a Cookie-Set
		// failure will simply retry.
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false, // SPA must read this from document.cookie
		Secure:   s.cfg.Server.TLS.Enabled,
		SameSite: http.SameSiteStrictMode,
		// No MaxAge → session cookie, dies with the browser tab.
		// We re-set it on every fresh login so an interactive user
		// always has a current token, and an idle tab that returns
		// after the session cookie expires will be 401'd by the auth
		// middleware before the CSRF check matters.
	})
}

// requireCSRF rejects state-changing requests that don't carry a
// matching X-CSRF-Token header. Read-only methods (GET/HEAD/OPTIONS)
// pass through; so do Bearer-authenticated requests (API tokens are
// not vulnerable to CSRF because the browser doesn't auto-attach a
// custom Authorization header to cross-site requests).
//
// Pattern: synchronizer-token via double-submit cookie. The server
// sets `mqc_csrf=<random>` on login; the SPA reads it via
// document.cookie and echoes it in `X-CSRF-Token`. The middleware
// verifies header == cookie via constant-time compare. A malicious
// cross-origin site can't read the cookie (SameSite=Strict on the
// session cookie keeps the browser from sending the session at all
// for cross-site requests), but even if it could read it, it can't
// set the custom header without CORS preflight that we don't grant.
func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1) Read-only methods don't change state.
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		// 2) Bearer-token auth isn't browser-driven, so it can't be
		//    CSRF'd. Recognise an explicit Bearer prefix; reject
		//    bare Authorization headers so a half-configured client
		//    can't smuggle past the gate.
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			next.ServeHTTP(w, r)
			return
		}
		// 3) Cookie auth: double-submit check.
		c, err := r.Cookie(csrfCookieName)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusForbidden, "missing CSRF cookie; re-authenticate")
			return
		}
		hdr := r.Header.Get(csrfHeaderName)
		if hdr == "" {
			writeError(w, http.StatusForbidden, "missing "+csrfHeaderName+" header")
			return
		}
		// Constant-time compare avoids timing-side-channel leakage
		// of the secret token value (which the SPA does have, but
		// an attacker doesn't).
		if subtle.ConstantTimeCompare([]byte(c.Value), []byte(hdr)) != 1 {
			writeError(w, http.StatusForbidden, "CSRF token mismatch")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// generateCSRFToken returns a hex-encoded 32-byte random token.
// crypto/rand failure is rare enough to bubble up; the caller treats
// it as "skip setting the cookie this time" rather than killing the
// request.
func generateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
