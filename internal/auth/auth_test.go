package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	simpleauth "github.com/bodaay/simpleauth-go"
)

// fakeClient implements authClient for tests.
type fakeClient struct {
	loginUser  string
	loginPass  string
	loginToken string
	loginErr   error
	verifyOK   map[string]*simpleauth.User
}

func (f *fakeClient) Login(_ context.Context, u, p string) (*simpleauth.TokenResponse, error) {
	if f.loginErr != nil {
		return nil, f.loginErr
	}
	if u != f.loginUser || p != f.loginPass {
		return nil, errors.New("bad creds")
	}
	return &simpleauth.TokenResponse{AccessToken: f.loginToken}, nil
}

func (f *fakeClient) Verify(token string) (*simpleauth.User, error) {
	if u, ok := f.verifyOK[token]; ok {
		return u, nil
	}
	return nil, errors.New("invalid")
}

func newTestService(client authClient) *Service {
	return newServiceWith(client, Options{
		CookieName: "mqc_session",
		SessionTTL: time.Hour,
		Secure:     false,
	})
}

func TestLogin_Success(t *testing.T) {
	s := newTestService(&fakeClient{
		loginUser: "alice", loginPass: "wonderland", loginToken: "jwt-abc",
	})
	tok, err := s.Login(context.Background(), "alice", "wonderland")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tok != "jwt-abc" {
		t.Errorf("token = %q", tok)
	}
}

func TestLogin_BadCreds_ReturnsErrInvalidCredentials(t *testing.T) {
	s := newTestService(&fakeClient{loginUser: "alice", loginPass: "pw"})
	_, err := s.Login(context.Background(), "alice", "wrong")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestValidate_AcceptsKnownToken(t *testing.T) {
	user := &simpleauth.User{Sub: "u1", PreferredUsername: "alice", Roles: []string{"admin"}}
	s := newTestService(&fakeClient{verifyOK: map[string]*simpleauth.User{"good": user}})

	u, err := s.Validate("good")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if u.Sub != "u1" {
		t.Errorf("user mismatch: %+v", u)
	}
}

func TestValidate_RejectsUnknownToken(t *testing.T) {
	s := newTestService(&fakeClient{})
	if _, err := s.Validate("bogus"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestSetAndClearCookie(t *testing.T) {
	s := newTestService(&fakeClient{})
	rec := httptest.NewRecorder()
	s.SetCookie(rec, "jwt-token")

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != "jwt-token" {
		t.Fatalf("unexpected cookies: %#v", cookies)
	}
	if !cookies[0].HttpOnly {
		t.Error("cookie should be HttpOnly")
	}

	rec = httptest.NewRecorder()
	s.ClearCookie(rec)
	cookies = rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Errorf("ClearCookie should yield expired cookie, got %#v", cookies)
	}
}

func TestRequireSession_NoCookie_401(t *testing.T) {
	s := newTestService(&fakeClient{})
	called := false
	h := s.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if called {
		t.Error("downstream handler should not have been called")
	}
}

func TestRequireSession_ValidCookie_PassesUser(t *testing.T) {
	user := &simpleauth.User{Sub: "u1", PreferredUsername: "alice", Roles: []string{"admin"}}
	s := newTestService(&fakeClient{verifyOK: map[string]*simpleauth.User{"good": user}})

	var seen *simpleauth.User
	h := s.RequireSession(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		u, _ := UserFromContext(r.Context())
		seen = u
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "mqc_session", Value: "good"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if seen == nil || seen.Sub != "u1" {
		t.Errorf("downstream did not receive user: %+v", seen)
	}
}

func TestRequireRole(t *testing.T) {
	user := &simpleauth.User{Sub: "u1", Roles: []string{"viewer"}}
	s := newTestService(&fakeClient{verifyOK: map[string]*simpleauth.User{"good": user}})

	stack := s.RequireSession(s.RequireRole("admin")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "mqc_session", Value: "good"})
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for missing role, got %d", rec.Code)
	}
}
