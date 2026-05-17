package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	simpleauth "github.com/bodaay/simpleauth-go"
)

// idleTimeoutFake is the minimum AuthClient surface the middleware
// needs — a fixed token resolves to a stub user, anything else fails.
type idleTimeoutFake struct {
	users map[string]*simpleauth.User
}

func (f *idleTimeoutFake) Login(_ context.Context, _, _ string) (*simpleauth.TokenResponse, error) {
	return nil, ErrInvalidCredentials
}
func (f *idleTimeoutFake) Verify(token string) (*simpleauth.User, error) {
	if u, ok := f.users[token]; ok {
		return u, nil
	}
	return nil, ErrInvalidCredentials
}
func (f *idleTimeoutFake) Refresh(_ context.Context, _ string) (*simpleauth.TokenResponse, error) {
	return nil, ErrInvalidCredentials
}

// TestIdleTimeout_SlidesCookieOnAuthedRequest — the canonical
// property: each authenticated request re-issues the session cookie
// so the browser's idle timer resets. Without the fix, the cookie
// just sits at MaxAge=SessionTTL until absolute expiry.
func TestIdleTimeout_SlidesCookieOnAuthedRequest(t *testing.T) {
	svc := NewServiceForTest(&idleTimeoutFake{
		users: map[string]*simpleauth.User{"tok-alice": {Sub: "alice"}},
	}, Options{
		CookieName:  "mqc_session",
		SessionTTL:  time.Hour,
		IdleTimeout: 5 * time.Minute,
	})

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := svc.RequireSession(inner)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: "mqc_session", Value: "tok-alice"})
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	// The response must carry a fresh Set-Cookie with MaxAge
	// matching IdleTimeout (in whole seconds).
	var refreshed *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "mqc_session" {
			refreshed = c
		}
	}
	if refreshed == nil {
		t.Fatal("middleware did not re-issue the session cookie")
	}
	wantMaxAge := int((5 * time.Minute).Seconds())
	if refreshed.MaxAge != wantMaxAge {
		t.Errorf("refreshed MaxAge = %d, want %d", refreshed.MaxAge, wantMaxAge)
	}
	if refreshed.Value != "tok-alice" {
		t.Errorf("refreshed token mutated: %q", refreshed.Value)
	}
}

// TestIdleTimeout_DisabledLeavesCookieAlone — when IdleTimeout is
// zero (legacy behaviour) the middleware must NOT re-issue the
// cookie. Some deployments rely on the absolute SessionTTL anchor
// and a sliding cookie would change the semantics under their feet.
func TestIdleTimeout_DisabledLeavesCookieAlone(t *testing.T) {
	svc := NewServiceForTest(&idleTimeoutFake{
		users: map[string]*simpleauth.User{"tok-alice": {Sub: "alice"}},
	}, Options{
		CookieName: "mqc_session",
		SessionTTL: time.Hour,
		// IdleTimeout: 0 — disabled
	})

	mw := svc.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: "mqc_session", Value: "tok-alice"})
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Name == "mqc_session" {
			t.Errorf("middleware re-issued cookie despite IdleTimeout=0: %#v", c)
		}
	}
}

// TestIdleTimeout_LongerThanTTLDisablesItself — IdleTimeout greater
// than SessionTTL would extend the cookie past the JWT's own
// validity. The accessor refuses that case so the cookie's MaxAge
// remains anchored to the absolute SessionTTL.
func TestIdleTimeout_LongerThanTTLDisablesItself(t *testing.T) {
	svc := NewServiceForTest(&idleTimeoutFake{
		users: map[string]*simpleauth.User{"tok-alice": {Sub: "alice"}},
	}, Options{
		CookieName:  "mqc_session",
		SessionTTL:  time.Hour,
		IdleTimeout: 2 * time.Hour, // > sessionTTL — should be ignored
	})

	if svc.IdleTimeoutEnabled() {
		t.Error("IdleTimeout > SessionTTL should not enable the sliding path")
	}
	// And the cookie MaxAge should still be SessionTTL.
	if got, want := svc.cookieMaxAgeSeconds(), int(time.Hour.Seconds()); got != want {
		t.Errorf("cookieMaxAgeSeconds = %d, want %d", got, want)
	}
}
