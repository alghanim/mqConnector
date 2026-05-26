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
	"mqConnector/internal/explain"
	"mqConnector/internal/health"
	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/storage"
)

// Wave 4 Task 1+2 — handler-level coverage for GET
// /api/v1/explain/{subject}/{id} and ?ai=summary.

// TestExplainEndpoint_UnknownSubject covers the 400 path.
func TestExplainEndpoint_UnknownSubject(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	rec := doExplainGET(t, h, cookie, "/api/v1/explain/nope/pipe-1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown subject") {
		t.Errorf("body = %q, want 'unknown subject'", rec.Body.String())
	}
}

// TestExplainEndpoint_UnknownID covers the 404 path.
func TestExplainEndpoint_UnknownID(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	rec := doExplainGET(t, h, cookie, "/api/v1/explain/circuit/no-such-pipeline")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestExplainEndpoint_Circuit_HappyPath asserts the deterministic body
// shape for the circuit subject.
func TestExplainEndpoint_Circuit_HappyPath(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	pid := seedExplainPipeline(t, srv, "p-circuit")
	srv.metrics.Register(pid, "src", "dst")
	srv.metrics.RecordSuccess(pid, 100, 5.0)

	var resp explainResponse
	getJSON(t, h, cookie, "/api/v1/explain/circuit/"+pid, http.StatusOK, &resp)
	if resp.Subject != "circuit" {
		t.Errorf("subject = %s, want circuit", resp.Subject)
	}
	if resp.ID != pid {
		t.Errorf("id = %s, want %s", resp.ID, pid)
	}
	if resp.Headline == "" {
		t.Error("headline must be non-empty")
	}
	if len(resp.Facts) == 0 {
		t.Error("facts must be non-empty")
	}
	if resp.AsOf.IsZero() {
		t.Error("as_of must be set")
	}
	if resp.AISource != "" || resp.AISummary != "" {
		t.Errorf("ai fields should be absent without ?ai=summary: source=%q summary=%q",
			resp.AISource, resp.AISummary)
	}
}

// TestExplainEndpoint_Drift_HappyPath asserts drift dispatch.
func TestExplainEndpoint_Drift_HappyPath(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	pid := seedExplainPipeline(t, srv, "p-drift")
	srv.metrics.Register(pid, "src", "dst")
	srv.metrics.RecordValidateAttempt(pid, true)
	srv.metrics.RecordValidateAttempt(pid, false)

	var resp explainResponse
	getJSON(t, h, cookie, "/api/v1/explain/drift/"+pid, http.StatusOK, &resp)
	if resp.Subject != "drift" {
		t.Errorf("subject = %s, want drift", resp.Subject)
	}
	if !explainFactHasLabel(resp.Facts, "Validate attempts (cumulative)") {
		t.Errorf("missing validate-attempts fact: %+v", resp.Facts)
	}
}

// TestExplainEndpoint_Latency_HappyPath asserts latency dispatch
// + that the stages section is emitted.
func TestExplainEndpoint_Latency_HappyPath(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	pid := seedExplainPipeline(t, srv, "p-lat")
	srv.metrics.Register(pid, "src", "dst")
	srv.metrics.RecordSuccess(pid, 100, 10)
	srv.metrics.RecordStageDuration(pid, "validate", 1.5)
	srv.metrics.RecordStageDuration(pid, "send", 8.0)

	var resp explainResponse
	getJSON(t, h, cookie, "/api/v1/explain/latency/"+pid, http.StatusOK, &resp)
	if resp.Subject != "latency" {
		t.Errorf("subject = %s, want latency", resp.Subject)
	}
	foundStages := false
	for _, sec := range resp.Sections {
		if sec.Kind == "stages" {
			foundStages = true
		}
	}
	if !foundStages {
		t.Errorf("expected a 'stages' section: sections=%+v", resp.Sections)
	}
}

// TestExplainEndpoint_DLQCluster_HappyPath seeds a real DLQ row
// + scans via the handler.
func TestExplainEndpoint_DLQCluster_HappyPath(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	tenant := storage.DefaultTenantID

	e := seedDLQEntry(t, srv, tenant, "p-dlq", "validation: missing field customer.id", "validate", []byte("a"))
	if e.ErrorFingerprint == "" {
		t.Fatalf("seed produced empty fingerprint")
	}
	var resp explainResponse
	getJSON(t, h, cookie, "/api/v1/explain/dlq_cluster/"+e.ErrorFingerprint, http.StatusOK, &resp)
	if resp.Subject != "dlq_cluster" {
		t.Errorf("subject = %s, want dlq_cluster", resp.Subject)
	}
	if !explainFactHasLabel(resp.Facts, "Cluster count") {
		t.Errorf("missing 'Cluster count' fact: %+v", resp.Facts)
	}
}

// TestExplainEndpoint_DLQEntry_HappyPath covers the per-entry path.
func TestExplainEndpoint_DLQEntry_HappyPath(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	tenant := storage.DefaultTenantID

	e := seedDLQEntry(t, srv, tenant, "p-dlq-entry", "send: dial tcp 10.0.0.1: refused", "send", []byte("payload"))
	var resp explainResponse
	getJSON(t, h, cookie, "/api/v1/explain/dlq_entry/"+e.ID, http.StatusOK, &resp)
	if resp.Subject != "dlq_entry" {
		t.Errorf("subject = %s, want dlq_entry", resp.Subject)
	}
	if !explainFactHasLabel(resp.Facts, "Error reason") {
		t.Errorf("missing 'Error reason' fact: %+v", resp.Facts)
	}
}

// TestExplainEndpoint_CrossTenantReturns404 ensures a pipeline
// that exists in a different tenant looks identical to one that
// doesn't exist (no information leak).
func TestExplainEndpoint_CrossTenantReturns404(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	ctx := context.Background()
	otherTenantID := "11111111-1111-1111-1111-111111111111"
	if err := srv.store.Tenants.Create(ctx, &storage.Tenant{ID: otherTenantID, Name: "other", Slug: "other"}); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	const pid = "leaked-pipeline"
	conn := &storage.Connection{Name: "c", Type: "rabbitmq", URL: "amqp://test"}
	if err := srv.store.Connections.Create(ctx, otherTenantID, conn); err != nil {
		t.Fatalf("create conn: %v", err)
	}
	if err := srv.store.Pipelines.Create(ctx, otherTenantID, &storage.Pipeline{
		ID: pid, Name: "p", SourceID: conn.ID, DestinationID: conn.ID,
		OutputFormat: "same",
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	// Alice runs under the default tenant — the cross-tenant
	// pipeline must look like ErrNotFound.
	rec := doExplainGET(t, h, cookie, "/api/v1/explain/circuit/"+pid)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (no cross-tenant leak)", rec.Code)
	}
}

// TestExplainEndpoint_AISummary_HappyPath asserts ?ai=summary
// attaches ai_summary + ai_source="ai" when the provider
// returns text.
func TestExplainEndpoint_AISummary_HappyPath(t *testing.T) {
	fake := ai.NewFakeProvider()
	fake.SetCompletion(ai.CapExplainWhySummary,
		"The circuit is closed; messages are flowing. No action required.")
	h, srv, _ := newTestServerForExplainAI(t, true, fake)
	cookie := loginCookie(t, h, "alice", "wonderland")
	pid := seedExplainPipeline(t, srv, "p-ai-ok")
	srv.metrics.Register(pid, "src", "dst")

	var resp explainResponse
	getJSON(t, h, cookie, "/api/v1/explain/circuit/"+pid+"?ai=summary",
		http.StatusOK, &resp)
	if resp.AISource != "ai" {
		t.Errorf("ai_source = %q, want ai (body=%+v)", resp.AISource, resp)
	}
	if !strings.Contains(resp.AISummary, "circuit is closed") {
		t.Errorf("ai_summary = %q, want it to contain the provider's canned text", resp.AISummary)
	}
}

// TestExplainEndpoint_AISummary_ProviderError asserts
// ai_source="deterministic" + no ai_summary when the provider
// fails.
func TestExplainEndpoint_AISummary_ProviderError(t *testing.T) {
	fake := ai.NewFakeProvider()
	fake.SetError(ai.CapExplainWhySummary,
		&ai.Error{Kind: "transport", Err: errors.New("network broken")})
	h, srv, _ := newTestServerForExplainAI(t, true, fake)
	cookie := loginCookie(t, h, "alice", "wonderland")
	pid := seedExplainPipeline(t, srv, "p-ai-err")
	srv.metrics.Register(pid, "src", "dst")

	var resp explainResponse
	getJSON(t, h, cookie, "/api/v1/explain/circuit/"+pid+"?ai=summary",
		http.StatusOK, &resp)
	if resp.AISource != "deterministic" {
		t.Errorf("ai_source = %q, want deterministic", resp.AISource)
	}
	if resp.AISummary != "" {
		t.Errorf("ai_summary should be empty on provider failure, got %q", resp.AISummary)
	}
}

// TestExplainEndpoint_AISummary_DisabledIgnoresQuery proves the
// ?ai=summary query param is silently skipped when the AI
// subsystem is disabled — the deterministic body still ships.
func TestExplainEndpoint_AISummary_DisabledIgnoresQuery(t *testing.T) {
	fake := ai.NewFakeProvider()
	fake.SetCompletion(ai.CapExplainWhySummary, "should not appear")
	h, srv, _ := newTestServerForExplainAI(t, false, fake)
	cookie := loginCookie(t, h, "alice", "wonderland")
	pid := seedExplainPipeline(t, srv, "p-ai-off")
	srv.metrics.Register(pid, "src", "dst")

	var resp explainResponse
	getJSON(t, h, cookie, "/api/v1/explain/circuit/"+pid+"?ai=summary",
		http.StatusOK, &resp)
	if resp.AISource != "" {
		t.Errorf("ai_source = %q, want empty when AI is disabled", resp.AISource)
	}
	if resp.AISummary != "" {
		t.Errorf("ai_summary = %q, want empty", resp.AISummary)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

// seedExplainPipeline creates a pipeline + connection pair the
// explainer can introspect. Returns the pipeline id. Idempotent.
func seedExplainPipeline(t *testing.T, srv *Server, pid string) string {
	t.Helper()
	ensurePipeline(t, srv, storage.DefaultTenantID, pid)
	return pid
}

// doExplainGET runs a GET that may return any status; the caller
// asserts. Returns the recorder for body inspection.
func doExplainGET(t *testing.T, h http.Handler, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	return rec
}

// explainFactHasLabel returns true if any fact in facts has the
// given label.
func explainFactHasLabel(facts []explain.Fact, want string) bool {
	for _, f := range facts {
		if f.Label == want {
			return true
		}
	}
	return false
}

// newTestServerForExplainAI mirrors newTestServerWithAI from
// handlers_dlq_intel_ai_test.go but allows CapExplainWhySummary
// in the feature list instead of CapDLQClusterNaming.
func newTestServerForExplainAI(t *testing.T, enabled bool, fake *ai.FakeProvider) (http.Handler, *Server, *fakeAuthClient) {
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
		aiCfg.Features = []ai.Capability{ai.CapExplainWhySummary}
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

// Reference exported symbols to keep imports tight and stable.
var _ = json.Marshal
