package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	simpleauth "github.com/bodaay/simpleauth-go"

	"mqConnector/internal/auth"
	"mqConnector/internal/config"
	"mqConnector/internal/dlq"
	"mqConnector/internal/health"
	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/storage"
)

// fakeAuthClient lets us drive the auth.Service in tests without a SimpleAuth
// server. It also exposes a hook to swap in a known token + user pair.
type fakeAuthClient struct {
	users     map[string]string           // password by username
	jwts      map[string]*simpleauth.User // accepted access tokens
	refreshes map[string]string           // refresh token → new access token
}

func (f *fakeAuthClient) Login(_ context.Context, u, p string) (*simpleauth.TokenResponse, error) {
	if want, ok := f.users[u]; ok && want == p {
		return &simpleauth.TokenResponse{
			AccessToken:  "tok-" + u,
			RefreshToken: "ref-" + u,
		}, nil
	}
	return nil, &authErr{"bad creds"}
}

func (f *fakeAuthClient) Verify(token string) (*simpleauth.User, error) {
	if u, ok := f.jwts[token]; ok {
		return u, nil
	}
	return nil, &authErr{"invalid"}
}

func (f *fakeAuthClient) Refresh(_ context.Context, rt string) (*simpleauth.TokenResponse, error) {
	if access, ok := f.refreshes[rt]; ok {
		return &simpleauth.TokenResponse{
			AccessToken:  access,
			RefreshToken: rt, // tests don't rotate
		}, nil
	}
	return nil, &authErr{"refresh rejected"}
}

type authErr struct{ msg string }

func (e *authErr) Error() string { return e.msg }

// newTestServer wires up a Server backed by an in-memory authClient and a
// tempdir SQLite. The returned handler is the chi router; cookies and routes
// behave identically to production.
func newTestServer(t *testing.T) (http.Handler, *Server, *fakeAuthClient) {
	t.Helper()

	dsn := "file:" + filepath.Join(t.TempDir(), "srv.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	store, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	fake := &fakeAuthClient{
		users: map[string]string{"alice": "wonderland"},
		jwts: map[string]*simpleauth.User{
			"tok-alice":   {Sub: "alice", PreferredUsername: "alice", Roles: []string{"admin"}},
			"tok-alice-2": {Sub: "alice", PreferredUsername: "alice", Roles: []string{"admin"}},
		},
		refreshes: map[string]string{"ref-alice": "tok-alice-2"},
	}
	authSvc := auth.NewServiceForTest(fake, auth.Options{
		CookieName: "mqc_session",
		SessionTTL: 0,
		Secure:     false,
	})

	pool := mq.NewPool(mq.PoolOptions{})
	t.Cleanup(pool.Close)
	metricsStore := metrics.New()
	dlqSvc := dlq.NewService(store, pool, dlq.Options{MaxRetries: 3})
	pipeMgr := pipeline.NewManager(context.Background(), store, pool, metricsStore, dlqSvc, logging.New("error", "json"))
	checker := health.NewChecker(store, metricsStore, "test")

	cfg := config.Default()
	cfg.Server.Mode = "dev"
	cfg.Server.TLS.Enabled = false
	cfg.Server.MaxBodyBytes = 1 << 20
	cfg.Auth.SimpleAuthURL = "https://test.invalid"
	cfg.Auth.CookieName = "mqc_session"

	srv, err := New(cfg, Deps{
		Auth:     authSvc,
		Store:    store,
		Pool:     pool,
		Metrics:  metricsStore,
		DLQ:      dlqSvc,
		Pipeline: pipeMgr,
		Health:   checker,
		Logger:   logging.New("error", "json"),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return srv.routes(), srv, fake
}

func loginCookie(t *testing.T, h http.Handler, username, password string) *http.Cookie {
	t.Helper()
	body := strings.NewReader(`{"username":"` + username + `","password":"` + password + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "mqc_session" {
			return c
		}
	}
	t.Fatal("login did not return cookie")
	return nil
}

// attachSession adds the session cookie AND a matching CSRF
// (cookie + header) pair so a state-changing request makes it past
// the requireCSRF middleware. Tests should use this everywhere
// instead of req.AddCookie(cookie). The double-submit middleware
// only checks cookie == header, so tests can synthesize any
// matching value rather than threading a real login-issued token.
func attachSession(req *http.Request, cookie *http.Cookie) {
	req.AddCookie(cookie)
	const csrf = "test-csrf-token"
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	req.Header.Set(csrfHeaderName, csrf)
}

// ----------------------------------------------------------------------------
// Public endpoints
// ----------------------------------------------------------------------------

func TestHealth_Public(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status"`) {
		t.Errorf("expected status field in body: %s", rec.Body)
	}
}

func TestLogin_BadCredentialsReturns401(t *testing.T) {
	h, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"alice","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: %d, want 401", rec.Code)
	}
}

func TestLogin_GoodCredentialsSetsCookie(t *testing.T) {
	h, _, _ := newTestServer(t)
	c := loginCookie(t, h, "alice", "wonderland")
	if c.HttpOnly != true {
		t.Error("expected HttpOnly cookie")
	}
	if c.Value != "tok-alice" {
		t.Errorf("cookie value = %q", c.Value)
	}
}

// ----------------------------------------------------------------------------
// Auth gating
// ----------------------------------------------------------------------------

func TestConnections_Unauthenticated_401(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: %d, want 401", rec.Code)
	}
}

func TestConnections_FullCRUD(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	// LIST → empty
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}

	// CREATE
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/connections",
		strings.NewReader(`{"name":"rab1","type":"rabbitmq","url":"amqp://x","queue_name":"q"}`))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created storage.Connection
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("create response missing id")
	}

	// GET
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/connections/"+created.ID, nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}

	// UPDATE
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/connections/"+created.ID,
		strings.NewReader(`{"name":"renamed","type":"rabbitmq","url":"amqp://x","queue_name":"q"}`))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}

	// DELETE
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/connections/"+created.ID, nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}

	// GET after delete → 404
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/connections/"+created.ID, nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: %d, want 404", rec.Code)
	}
}

func TestMetricsPrometheus_RequiresAuth(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/metrics/prometheus", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: %d, want 401", rec.Code)
	}
}

func TestMetricsPrometheus_Authenticated_OK(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/metrics/prometheus", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "mqconnector_uptime_seconds") {
		t.Errorf("missing uptime metric in body")
	}
}

func TestSecurityHeaders_Present(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	for _, header := range []string{"X-Content-Type-Options", "X-Frame-Options", "Strict-Transport-Security"} {
		if rec.Header().Get(header) == "" {
			t.Errorf("missing security header: %s", header)
		}
	}
}

func TestRequestID_RoundTrip(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("X-Request-Id", "test-rid-123")
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-Id"); got != "test-rid-123" {
		t.Errorf("X-Request-Id round-trip: %q", got)
	}
}

func TestLogout_ClearsCookie(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "mqc_session" && c.MaxAge < 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected expired session cookie after logout")
	}
}

func TestMe_ReturnsUser(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["preferred_username"] != "alice" {
		t.Errorf("preferred_username = %v", body["preferred_username"])
	}
}
