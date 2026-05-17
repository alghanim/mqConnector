package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCSRF_StateChangingRequestRequiresHeader covers the core
// property: a state-changing request with a valid session cookie but
// no CSRF token (or a mismatched one) is rejected. Without this gate,
// any malicious site could lure the user's browser into POSTing here
// via a form submission.
func TestCSRF_StateChangingRequestRequiresHeader(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	// No CSRF cookie, no header — must reject.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connections",
		strings.NewReader(`{"name":"x","type":"rabbitmq","url":"amqp://x","queue_name":"q"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF should 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CSRF") {
		t.Errorf("expected CSRF error in body: %s", rec.Body)
	}
}

// TestCSRF_MismatchedTokenRejected — cookie value and header value
// must compare equal. Setting only one half of the pair lets the
// middleware know the request didn't originate from our SPA.
func TestCSRF_MismatchedTokenRejected(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connections",
		strings.NewReader(`{"name":"x","type":"rabbitmq","url":"amqp://x","queue_name":"q"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "secret-a"})
	req.Header.Set(csrfHeaderName, "secret-b")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mismatched CSRF should 403, got %d", rec.Code)
	}
}

// TestCSRF_BearerTokenBypassesCheck — API-token (Bearer) callers are
// not browser-driven and not vulnerable to CSRF. The middleware must
// let them through without a token, because curl / CI clients can't
// participate in the double-submit pattern.
func TestCSRF_BearerTokenBypassesCheck(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &Server{}
	mw := srv.requireCSRF(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/anywhere", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("Bearer-auth should bypass CSRF, got %d", rec.Code)
	}
}

// TestCSRF_GETIsExempt — read-only requests don't change state and
// don't need CSRF protection. A blanket gate would break dashboard
// polling.
func TestCSRF_GETIsExempt(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &Server{}
	mw := srv.requireCSRF(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anywhere", nil)
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET should bypass CSRF, got %d", rec.Code)
	}
}

// TestCSRF_LoginIssuesCookie — the SPA needs the CSRF token to be
// readable from document.cookie immediately after login. Verify the
// non-HttpOnly mqc_csrf cookie is in the Set-Cookie response.
func TestCSRF_LoginIssuesCookie(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"alice","password":"wonderland"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d", rec.Code)
	}
	var found *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == csrfCookieName {
			found = c
			break
		}
	}
	if found == nil {
		t.Fatal("login did not issue a CSRF cookie")
	}
	if found.HttpOnly {
		t.Error("CSRF cookie must NOT be HttpOnly — SPA needs to read it")
	}
	if found.SameSite != http.SameSiteStrictMode {
		t.Errorf("CSRF cookie SameSite = %v, want Strict", found.SameSite)
	}
	if len(found.Value) < 32 {
		t.Errorf("CSRF token looks too short: %q", found.Value)
	}
}
