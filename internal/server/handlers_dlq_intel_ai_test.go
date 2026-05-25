package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	simpleauth "github.com/bodaay/simpleauth-go"

	"mqConnector/internal/ai"
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

// Wave 3 Task 4 — AI cluster-naming integration tests.
//
// These cases drive the /api/v1/dlq/clusters?ai=names path with a
// FakeProvider so they don't need a live LLM. The fake exercises the
// same code paths as the production OpenAI provider — preflight gate,
// audit emit, metrics counter, cache write — except the network call
// itself.

// newTestServerWithAI wires a server with a FakeProvider already
// programmed for CapDLQClusterNaming so individual tests just enable
// the feature in the config or override the canned response.
func newTestServerWithAI(t *testing.T, enabled bool, fake *ai.FakeProvider) (http.Handler, *Server, *fakeAuthClient) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "srv.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	store, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fakeAuth := &fakeAuthClient{
		users: map[string]string{"alice": "wonderland"},
		jwts: map[string]*simpleauth.User{
			"tok-alice":   {Sub: "alice", PreferredUsername: "alice", Roles: []string{"admin"}},
			"tok-alice-2": {Sub: "alice", PreferredUsername: "alice", Roles: []string{"admin"}},
		},
		refreshes: map[string]string{"ref-alice": "tok-alice-2"},
	}
	authSvc := auth.NewServiceForTest(fakeAuth, auth.Options{
		CookieName: "mqc_session", SessionTTL: 0, Secure: false,
	})
	pool := mq.NewPool(mq.PoolOptions{})
	t.Cleanup(pool.Close)
	metricsStore := metrics.New()
	dlqSvc := dlq.NewService(store, pool, dlq.Options{MaxRetries: 3})
	pipeMgr := pipeline.NewManager(context.Background(), store, pool, metricsStore, dlqSvc,
		logging.New("error", "json"))
	checker := health.NewChecker(store, metricsStore, "test")

	cfg := config.Default()
	cfg.Server.Mode = "dev"
	cfg.Server.TLS.Enabled = false
	cfg.Server.MaxBodyBytes = 1 << 20
	cfg.Auth.SimpleAuthURL = "https://test.invalid"
	cfg.Auth.CookieName = "mqc_session"

	aiCfg := ai.Config{Enabled: enabled, AuditEvery: true}
	if enabled {
		aiCfg.Features = []ai.Capability{ai.CapDLQClusterNaming}
	}
	counter := ai.NewCallCounter()
	fake.SetCounter(counter)
	if store.AIAudit != nil {
		fake.SetAudit(store.AIAudit)
	}

	srv, err := New(cfg, Deps{
		Auth: authSvc, Store: store, Pool: pool, Metrics: metricsStore,
		DLQ: dlqSvc, Pipeline: pipeMgr, Health: checker,
		Logger:     logging.New("error", "json"),
		AIProvider: fake,
		AIAudit:    store.AIAudit,
		AICounter:  counter,
		AIConfig:   aiCfg,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return srv.routes(), srv, fakeAuth
}

// TestDLQClusters_AINames_PopulatesAIFields asserts the happy path:
// when ?ai=names is set + feature on + provider returns valid JSON,
// every cluster's ai_name + ai_source land on the response.
func TestDLQClusters_AINames_PopulatesAIFields(t *testing.T) {
	fake := ai.NewFakeProvider()
	fake.SetStructured(ai.CapDLQClusterNaming, json.RawMessage(`{
		"title": "Validation: missing required field",
		"summary": "All failures hit the validate stage with a required field absent. Likely caused by a producer schema change.",
		"suggestion": "Check the producer's recent deploys for a schema change."
	}`))
	h, srv, _ := newTestServerWithAI(t, true, fake)
	cookie := loginCookie(t, h, "alice", "wonderland")
	tenant := storage.DefaultTenantID

	// Three rows that collapse to one cluster.
	seedDLQEntry(t, srv, tenant, "p1", "validation: missing field customer.id", "validate", []byte("a"))
	seedDLQEntry(t, srv, tenant, "p1", "validation: missing field order.id", "validate", []byte("b"))
	seedDLQEntry(t, srv, tenant, "p1", "validation: missing field payment.id", "validate", []byte("c"))

	var resp clustersResponse
	getJSON(t, h, cookie, "/api/v1/dlq/clusters?ai=names", http.StatusOK, &resp)
	if len(resp.Clusters) != 1 {
		t.Fatalf("got %d clusters, want 1", len(resp.Clusters))
	}
	c := resp.Clusters[0]
	if c.AIName == nil {
		t.Fatalf("AIName is nil; want populated")
	}
	if !strings.Contains(c.AIName.Title, "Validation") {
		t.Errorf("AIName.Title = %q, want LLM canned value", c.AIName.Title)
	}
	if c.AISource != "ai" {
		t.Errorf("AISource = %q, want ai", c.AISource)
	}
	// FakeProvider records every call — verifies the gated path
	// actually reached the provider.
	if calls := fake.Calls(); len(calls) != 1 {
		t.Errorf("fake provider got %d calls, want 1", len(calls))
	}
	// Audit row should have landed via the storage repo. Use the
	// repo's List with a wide filter.
	rows, _, err := srv.store.AIAudit.List(context.Background(), storage.AIAuditFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("AIAudit.List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("AI audit rows = %d, want 1", len(rows))
	}
	if rows[0].Feature != "dlq_cluster_naming" || rows[0].Outcome != "ok" {
		t.Errorf("audit row = %+v", rows[0])
	}
}

// TestDLQClusters_AIDisabled_OmitsAIFields confirms the wire shape is
// backward-compatible when AI is off: no AIName/AISource fields,
// even if ?ai=names is passed.
func TestDLQClusters_AIDisabled_OmitsAIFields(t *testing.T) {
	fake := ai.NewFakeProvider()
	fake.SetStructured(ai.CapDLQClusterNaming, json.RawMessage(`{"title":"X","summary":"y","suggestion":"z"}`))
	h, srv, _ := newTestServerWithAI(t, false, fake)
	cookie := loginCookie(t, h, "alice", "wonderland")
	seedDLQEntry(t, srv, storage.DefaultTenantID, "p1", "boom", "validate", []byte("a"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dlq/clusters?ai=names", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	// Decode loosely: assert the raw JSON does NOT carry ai_name /
	// ai_source for any cluster (omitempty contract).
	if strings.Contains(rec.Body.String(), `"ai_name"`) {
		t.Errorf("response carries ai_name even though AI is disabled:\n%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"ai_source"`) {
		t.Errorf("response carries ai_source even though AI is disabled:\n%s", rec.Body.String())
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("fake provider called %d times, want 0 when AI off", len(fake.Calls()))
	}
}

// TestDLQClusters_AIProviderError_FallsBackDeterministic verifies that
// a per-cluster provider failure degrades to ai_source="deterministic"
// + ai_name=null without poisoning the rest of the response.
func TestDLQClusters_AIProviderError_FallsBackDeterministic(t *testing.T) {
	fake := ai.NewFakeProvider()
	fake.SetError(ai.CapDLQClusterNaming, &ai.Error{Kind: "transport", Err: errors.New("EOF")})
	h, srv, _ := newTestServerWithAI(t, true, fake)
	cookie := loginCookie(t, h, "alice", "wonderland")
	seedDLQEntry(t, srv, storage.DefaultTenantID, "p1", "boom", "validate", []byte("a"))

	var resp clustersResponse
	getJSON(t, h, cookie, "/api/v1/dlq/clusters?ai=names", http.StatusOK, &resp)
	if len(resp.Clusters) != 1 {
		t.Fatalf("got %d clusters, want 1", len(resp.Clusters))
	}
	c := resp.Clusters[0]
	if c.AIName != nil {
		t.Errorf("AIName = %+v, want nil on provider error", c.AIName)
	}
	if c.AISource != "deterministic" {
		t.Errorf("AISource = %q, want deterministic on provider error", c.AISource)
	}
	// Template + the rest of the row must still be populated.
	if c.Template == "" || c.Count != 1 {
		t.Errorf("non-AI fields corrupted by error: template=%q count=%d", c.Template, c.Count)
	}
}

// TestDLQClusters_AICache_SkipsSecondCall confirms two consecutive
// renders of the same fingerprint only invoke the provider once
// within the cache TTL.
func TestDLQClusters_AICache_SkipsSecondCall(t *testing.T) {
	fake := ai.NewFakeProvider()
	fake.SetStructured(ai.CapDLQClusterNaming, json.RawMessage(`{"title":"X","summary":"y","suggestion":"z"}`))
	h, srv, _ := newTestServerWithAI(t, true, fake)
	cookie := loginCookie(t, h, "alice", "wonderland")
	seedDLQEntry(t, srv, storage.DefaultTenantID, "p1", "boom", "validate", []byte("a"))

	var resp clustersResponse
	getJSON(t, h, cookie, "/api/v1/dlq/clusters?ai=names", http.StatusOK, &resp)
	getJSON(t, h, cookie, "/api/v1/dlq/clusters?ai=names", http.StatusOK, &resp)
	if calls := fake.Calls(); len(calls) != 1 {
		t.Errorf("fake provider got %d calls across two renders, want 1 (cache miss + hit)", len(calls))
	}
	if resp.Clusters[0].AISource != "ai" {
		t.Errorf("second render lost ai source: %q", resp.Clusters[0].AISource)
	}
	_ = srv // silence linter
}
