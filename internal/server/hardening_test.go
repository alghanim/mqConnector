package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecover_PanicReturns500(t *testing.T) {
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestRecover_NoPanic_NoOp(t *testing.T) {
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("Recover should pass through, got %d", rec.Code)
	}
}

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	l := newLoginLimiter(3, time.Second)
	for i := 0; i < 3; i++ {
		if !l.allow("1.2.3.4") {
			t.Errorf("attempt %d should have been allowed", i+1)
		}
	}
}

func TestRateLimit_DeniesOverLimit(t *testing.T) {
	l := newLoginLimiter(2, time.Second)
	_ = l.allow("1.2.3.4")
	_ = l.allow("1.2.3.4")
	if l.allow("1.2.3.4") {
		t.Error("third attempt should be denied")
	}
}

func TestRateLimit_IndependentPerIP(t *testing.T) {
	l := newLoginLimiter(1, time.Second)
	if !l.allow("1.1.1.1") {
		t.Fatal("first IP denied")
	}
	if !l.allow("2.2.2.2") {
		t.Fatal("second IP denied despite being a different bucket")
	}
}

func TestRateLimit_WindowResets(t *testing.T) {
	l := newLoginLimiter(1, 25*time.Millisecond)
	_ = l.allow("1.1.1.1")
	if l.allow("1.1.1.1") {
		t.Fatal("should be denied within window")
	}
	time.Sleep(40 * time.Millisecond)
	if !l.allow("1.1.1.1") {
		t.Fatal("should be allowed after window resets")
	}
}

func TestRateLimit_ConcurrentAccessIsSafe(t *testing.T) {
	l := newLoginLimiter(100, time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.allow("1.1.1.1")
		}()
	}
	wg.Wait()
}

func TestLoginRateLimit_Triggers429(t *testing.T) {
	h, _, _ := newTestServer(t)
	bad := strings.NewReader(`{"username":"alice","password":"bad"}`)
	for i := 0; i < 12; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
			strings.NewReader(`{"username":"alice","password":"bad"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "10.0.0.1:54321"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
	// 13th attempt should be rate-limited.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bad)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:54321"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
}

func TestSecurityHeaders_IncludesCSP(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("missing Content-Security-Policy header")
	}
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing directive %q (got: %s)", want, csp)
		}
	}
	if rec.Header().Get("Permissions-Policy") == "" {
		t.Error("missing Permissions-Policy header")
	}
}

func TestRequestContextTimeout_AppliesDeadline(t *testing.T) {
	var got context.Context
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Context()
	})
	h := RequestContextTimeout(50 * time.Millisecond)(inner)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	dl, ok := got.Deadline()
	if !ok {
		t.Fatal("expected request context to have a deadline")
	}
	if remaining := time.Until(dl); remaining > 60*time.Millisecond || remaining < -10*time.Millisecond {
		t.Errorf("unexpected deadline: %v from now", remaining)
	}
}

func TestRequestContextTimeout_ZeroIsNoOp(t *testing.T) {
	called := false
	h := RequestContextTimeout(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("downstream handler should still run with zero timeout")
	}
}

func TestClientIP_StripsPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:54321"
	if got := clientIP(req); got != "192.0.2.10" {
		t.Errorf("clientIP = %q, want 192.0.2.10", got)
	}
}
