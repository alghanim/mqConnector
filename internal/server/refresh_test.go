package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Login → expect both the session cookie and the refresh cookie to be set.
func TestLogin_SetsBothCookies(t *testing.T) {
	h, _, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"alice","password":"wonderland"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}

	hasAccess, hasRefresh := false, false
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "mqc_session" && ck.Value == "tok-alice" {
			hasAccess = true
		}
		if ck.Name == "mqc_session_refresh" && ck.Value == "ref-alice" {
			hasRefresh = true
		}
	}
	if !hasAccess || !hasRefresh {
		t.Errorf("missing cookies: access=%v refresh=%v", hasAccess, hasRefresh)
	}
}

// Refresh with a good refresh cookie → 200 and rotates the access cookie.
func TestRefresh_RotatesAccessCookie(t *testing.T) {
	h, _, _ := newTestServer(t)

	// First login to mint a refresh token.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"alice","password":"wonderland"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	var refreshCookie *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "mqc_session_refresh" {
			refreshCookie = ck
		}
	}
	if refreshCookie == nil {
		t.Fatal("login did not set refresh cookie")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.AddCookie(refreshCookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d %s", rec.Code, rec.Body)
	}
	gotNew := false
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "mqc_session" && ck.Value == "tok-alice-2" {
			gotNew = true
		}
	}
	if !gotNew {
		t.Errorf("expected new access cookie after refresh, got cookies %+v", rec.Result().Cookies())
	}
}

// Refresh with no cookie → 401.
func TestRefresh_NoCookie_401(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// Refresh with a rejected token → 401 + cookies cleared.
func TestRefresh_BadCookie_ClearsCookies(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "mqc_session_refresh", Value: "no-such-token"})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	cleared := 0
	for _, ck := range rec.Result().Cookies() {
		if (ck.Name == "mqc_session" || ck.Name == "mqc_session_refresh") && ck.MaxAge < 0 {
			cleared++
		}
	}
	if cleared < 2 {
		t.Errorf("expected both cookies to be cleared, cleared=%d", cleared)
	}
}

// Logout → both cookies cleared.
func TestLogout_ClearsBothCookies(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	cleared := 0
	for _, ck := range rec.Result().Cookies() {
		if (ck.Name == "mqc_session" || ck.Name == "mqc_session_refresh") && ck.MaxAge < 0 {
			cleared++
		}
	}
	if cleared < 2 {
		t.Errorf("expected both cookies cleared on logout, got %d", cleared)
	}
}
