package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// topologyGet issues a GET /api/v1/topology with a logged-in alice
// cookie and returns the decoded body + raw response. Used by every
// case in this file.
func topologyGet(t *testing.T, h http.Handler, cookie *http.Cookie) (*httptest.ResponseRecorder, topologyResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/topology", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	var body topologyResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode topology body: %v\n%s", err, rec.Body)
		}
	}
	return rec, body
}

// TestTopology_HappyPath seeds two connections + one pipeline with
// shadow + one enabled routing rule, plus some metrics + one DLQ
// entry, then asserts every field of the response.
func TestTopology_HappyPath(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)

	src := postConn(t, h, cookie, `{"name":"topo-src","type":"rabbitmq","url":"amqp://src","queue_name":"q1","topic":"topic1"}`)
	dst := postConn(t, h, cookie, `{"name":"topo-dst","type":"kafka","brokers":"kafka:9092","topic":"out"}`)
	rt := postConn(t, h, cookie, `{"name":"topo-route","type":"rabbitmq","url":"amqp://rt","queue_name":"r"}`)
	shadow := postConn(t, h, cookie, `{"name":"topo-shadow","type":"rabbitmq","url":"amqp://sh","queue_name":"s"}`)

	// Create pipeline with shadow + 30% shadow rate. enabled=false
	// keeps the pipeline manager from starting an executor against
	// the fake amqp:// URL — we want a deterministic snapshot of the
	// configuration, not a racing reload.
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"name":"topo-pipe","source_id":"` + src.ID +
		`","destination_id":"` + dst.ID +
		`","output_format":"same","enabled":false,` +
		`"shadow_destination_id":"` + shadow.ID + `","shadow_percent":30}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines", body)
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create pipeline: %d %s", rec.Code, rec.Body)
	}
	var pipe storage.Pipeline
	_ = json.Unmarshal(rec.Body.Bytes(), &pipe)

	// Seed a routing rule.
	rrBody := `[{"condition_path":"$.type","condition_operator":"eq","condition_value":"x",` +
		`"destination_id":"` + rt.ID + `","priority":1,"enabled":true}]`
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/pipelines/"+pipe.ID+"/routing-rules",
		strings.NewReader(rrBody))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("put routing rules: %d %s", rec.Code, rec.Body)
	}

	// Seed metrics — Register + record one success so the snapshot
	// has non-zero processed/avg latency.
	srv.metrics.Register(pipe.ID, "q1", "out")
	srv.metrics.RecordSuccess(pipe.ID, 100, 25)
	srv.metrics.RecordSuccess(pipe.ID, 100, 25)
	srv.metrics.RecordFailure(pipe.ID)

	// Seed one DLQ entry.
	ctx := context.Background()
	if err := srv.store.DLQ.Insert(ctx, storage.DefaultTenantID, &storage.DLQEntry{
		PipelineID:  pipe.ID,
		OriginalMsg: []byte("dlq-payload"),
		ErrorReason: "test failure",
	}); err != nil {
		t.Fatalf("insert dlq: %v", err)
	}

	rec, resp := topologyGet(t, h, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/topology: %d %s", rec.Code, rec.Body)
	}

	if resp.TenantID != storage.DefaultTenantID {
		t.Errorf("tenant_id = %q, want %q", resp.TenantID, storage.DefaultTenantID)
	}
	if resp.GeneratedAt.IsZero() {
		t.Error("generated_at should be set")
	}

	// Connections — assert names + types come through and topic is
	// preserved for the connections that carry one.
	connByID := map[string]topologyConnection{}
	for _, c := range resp.Connections {
		connByID[c.ID] = c
	}
	if len(connByID) != 4 {
		t.Fatalf("expected 4 connections, got %d", len(connByID))
	}
	if connByID[src.ID].Name != "topo-src" || connByID[src.ID].Type != "rabbitmq" {
		t.Errorf("src connection = %+v", connByID[src.ID])
	}
	if connByID[dst.ID].Type != "kafka" {
		t.Errorf("dst type = %q, want kafka", connByID[dst.ID].Type)
	}
	if connByID[src.ID].Topic != "topic1" {
		t.Errorf("src topic = %q, want topic1", connByID[src.ID].Topic)
	}
	// Depth should be nil (no running pipeline / no DepthReporter sample).
	if connByID[src.ID].Depth != nil {
		t.Errorf("src depth should be nil, got %v", *connByID[src.ID].Depth)
	}

	// Pipelines.
	if len(resp.Pipelines) != 1 {
		t.Fatalf("expected 1 pipeline, got %d", len(resp.Pipelines))
	}
	p := resp.Pipelines[0]
	if p.ID != pipe.ID {
		t.Errorf("pipeline id = %q, want %q", p.ID, pipe.ID)
	}
	if p.SourceID != src.ID || p.DestinationID != dst.ID {
		t.Errorf("pipeline source/dest = %s/%s want %s/%s",
			p.SourceID, p.DestinationID, src.ID, dst.ID)
	}
	if p.ShadowDestID != shadow.ID || p.ShadowPercent != 30 {
		t.Errorf("shadow fields = %s/%d, want %s/30", p.ShadowDestID, p.ShadowPercent, shadow.ID)
	}
	if len(p.RouteDestIDs) != 1 || p.RouteDestIDs[0] != rt.ID {
		t.Errorf("route_destination_ids = %v, want [%s]", p.RouteDestIDs, rt.ID)
	}
	if p.DlqDepth != 1 {
		t.Errorf("dlq_depth = %d, want 1", p.DlqDepth)
	}
	if p.Processed != 2 || p.Failed != 1 {
		t.Errorf("counters = (%d, %d), want (2, 1)", p.Processed, p.Failed)
	}
	if p.AvgLatencyMs != 25.0 {
		t.Errorf("avg_latency_ms = %v, want 25", p.AvgLatencyMs)
	}
	if p.Enabled {
		t.Errorf("enabled = true, want false (test pipeline)")
	}
	// First topology call has no prior sample, so msg_per_min = 0.
	if p.MsgPerMin != 0 {
		t.Errorf("msg_per_min first call = %d, want 0", p.MsgPerMin)
	}
	// Pipeline isn't actually running on the test manager (no Reload),
	// so circuit_state defaults to "unknown".
	if p.CircuitState != "unknown" {
		t.Errorf("circuit_state = %q, want unknown", p.CircuitState)
	}
	// metrics Register sets Status = "connected"; the response carries
	// it through.
	if p.Status != "connected" {
		t.Errorf("status = %q, want connected", p.Status)
	}

	// Second call: we observe a rate. Bump the counter, wait a tick
	// so the elapsed time is measurable, hit the endpoint again.
	srv.metrics.RecordSuccess(pipe.ID, 100, 25)
	// 50ms is enough for the sub-second rate math without slowing
	// the suite noticeably.
	time.Sleep(50 * time.Millisecond)
	_, resp = topologyGet(t, h, cookie)
	if len(resp.Pipelines) != 1 {
		t.Fatalf("second call pipelines len = %d", len(resp.Pipelines))
	}
	if resp.Pipelines[0].MsgPerMin <= 0 {
		t.Errorf("msg_per_min second call should be > 0, got %d",
			resp.Pipelines[0].MsgPerMin)
	}
}

// TestTopology_CrossTenantIsolation confirms tenant B's GET returns
// only B's empty topology even when A has populated state.
func TestTopology_CrossTenantIsolation(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	srv.auth.SetTenantResolver(tenantSwitcher{
		defaultTenant: "tenant-a", defaultRole: "owner"})

	ctx := context.Background()
	for _, tid := range []string{"tenant-a", "tenant-b"} {
		_ = srv.store.Tenants.Create(ctx, &storage.Tenant{
			ID: tid, Slug: tid, Name: tid, Status: "active"})
		_ = srv.store.Memberships.Upsert(ctx, &storage.Membership{
			TenantID: tid, UserSub: "alice", Username: "alice",
			Role: storage.RoleOwner})
	}

	connA := &storage.Connection{Name: "ca", Type: "rabbitmq", URL: "amqp://a"}
	if err := srv.store.Connections.Create(ctx, "tenant-a", connA); err != nil {
		t.Fatal(err)
	}
	pipeA := &storage.Pipeline{Name: "pa", SourceID: connA.ID,
		DestinationID: connA.ID, OutputFormat: "same", Enabled: false}
	if err := srv.store.Pipelines.Create(ctx, "tenant-a", pipeA); err != nil {
		t.Fatal(err)
	}

	doAs := func(tid string) topologyResponse {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/topology", nil)
		req.Header.Set("X-Test-Tenant", tid)
		attachSession(req, cookie)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET as %s: %d %s", tid, rec.Code, rec.Body)
		}
		var body topologyResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return body
	}

	a := doAs("tenant-a")
	if len(a.Connections) != 1 || len(a.Pipelines) != 1 {
		t.Errorf("tenant-a topology: conns=%d pipes=%d, want 1/1",
			len(a.Connections), len(a.Pipelines))
	}
	if a.TenantID != "tenant-a" {
		t.Errorf("tenant-a tenant_id = %q", a.TenantID)
	}

	b := doAs("tenant-b")
	if len(b.Connections) != 0 || len(b.Pipelines) != 0 {
		t.Errorf("tenant-b should see empty topology, got conns=%d pipes=%d (%v %v)",
			len(b.Connections), len(b.Pipelines), b.Connections, b.Pipelines)
	}
	if b.TenantID != "tenant-b" {
		t.Errorf("tenant-b tenant_id = %q", b.TenantID)
	}
}

// TestTopology_EmptyTenant — no connections, no pipelines → 200 with
// empty arrays (NOT 404, NOT null).
func TestTopology_EmptyTenant(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)
	rec, resp := topologyGet(t, h, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	if resp.Connections == nil || resp.Pipelines == nil {
		t.Errorf("empty arrays should be non-nil; got conns=%v pipes=%v",
			resp.Connections, resp.Pipelines)
	}
	if len(resp.Connections) != 0 || len(resp.Pipelines) != 0 {
		t.Errorf("expected empty topology, got conns=%d pipes=%d",
			len(resp.Connections), len(resp.Pipelines))
	}
	// And the raw body must encode arrays as [] not null — same
	// contract the writeJSONList helper enforces elsewhere.
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"connections":[]`)) {
		t.Errorf("raw body should encode empty connections as []: %s", rec.Body)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"pipelines":[]`)) {
		t.Errorf("raw body should encode empty pipelines as []: %s", rec.Body)
	}
}

// TestTopology_BestEffortDegradation drops the dlq table mid-test and
// asserts the endpoint still returns 200 with dlq_depth=0 for the
// affected pipeline, and that the slog warn fires.
func TestTopology_BestEffortDegradation(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)

	// Capture the server logger so we can inspect the warn.
	var buf bytes.Buffer
	srv.logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	src := postConn(t, h, cookie, `{"name":"src","type":"rabbitmq","url":"amqp://x","queue_name":"q"}`)
	dst := postConn(t, h, cookie, `{"name":"dst","type":"rabbitmq","url":"amqp://x","queue_name":"q2"}`)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"name":"p","source_id":"` + src.ID + `","destination_id":"` + dst.ID +
		`","output_format":"same","enabled":false}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines", body)
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create pipeline: %d %s", rec.Code, rec.Body)
	}

	// Same trick the snapshot test uses to force a sub-source error
	// from the database driver: drop the table the query reads.
	if _, err := srv.store.DB.ExecContext(context.Background(),
		`DROP TABLE dlq`); err != nil {
		t.Fatalf("drop dlq: %v", err)
	}

	rec, resp := topologyGet(t, h, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("endpoint must still return 200 on sub-source failure: %d %s",
			rec.Code, rec.Body)
	}
	if len(resp.Pipelines) != 1 {
		t.Fatalf("pipelines len = %d", len(resp.Pipelines))
	}
	if resp.Pipelines[0].DlqDepth != 0 {
		t.Errorf("dlq_depth = %d, want 0 (degraded)", resp.Pipelines[0].DlqDepth)
	}
	// Best-effort log: the handler emits a warn naming the failed
	// sub-source.
	if !bytes.Contains(buf.Bytes(), []byte("topology: dlq count by pipeline failed")) {
		t.Errorf("expected slog warn for dlq failure, got: %s", buf.String())
	}
}

// TestTopology_ConnectedFlag walks the pool from empty → seeded and
// asserts the connected column flips on the seeded connection. Uses
// the InjectForTest hook on the pool so we don't need a live broker.
func TestTopology_ConnectedFlag(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)

	src := postConn(t, h, cookie, `{"name":"connsrc","type":"rabbitmq","url":"amqp://x","queue_name":"q"}`)
	dst := postConn(t, h, cookie, `{"name":"conndst","type":"rabbitmq","url":"amqp://x","queue_name":"q2"}`)
	rec := httptest.NewRecorder()
	// enabled=false keeps the manager from autostarting a reload that
	// races our pool inspection — we drive the pool by hand below.
	body := strings.NewReader(`{"name":"connpipe","source_id":"` + src.ID +
		`","destination_id":"` + dst.ID + `","output_format":"same","enabled":false}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines", body)
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create pipeline: %d %s", rec.Code, rec.Body)
	}
	var pipe storage.Pipeline
	_ = json.Unmarshal(rec.Body.Bytes(), &pipe)

	// First topology: pool is empty, both connections show connected=false.
	_, resp := topologyGet(t, h, cookie)
	connByID := map[string]topologyConnection{}
	for _, c := range resp.Connections {
		connByID[c.ID] = c
	}
	if connByID[src.ID].Connected || connByID[dst.ID].Connected {
		t.Errorf("expected both connections disconnected initially, got src=%v dst=%v",
			connByID[src.ID].Connected, connByID[dst.ID].Connected)
	}

	// Inject a "live" source connection under the executor-style key.
	// The MemoryConnector is fine — we just need any Connector for the
	// pool's Has() to see the key.
	reg := mq.NewMemoryRegistry(8)
	srv.pool.InjectForTest("source-"+pipe.ID, mq.NewMemoryConnector(reg, "q"))

	_, resp = topologyGet(t, h, cookie)
	connByID = map[string]topologyConnection{}
	for _, c := range resp.Connections {
		connByID[c.ID] = c
	}
	if !connByID[src.ID].Connected {
		t.Errorf("expected src connected=true after pool inject, got %+v", connByID[src.ID])
	}
	if connByID[dst.ID].Connected {
		t.Errorf("dst should remain disconnected, got %+v", connByID[dst.ID])
	}

	// Inject the destination under the worker-suffix form to verify
	// the prefix-matching helper handles multi-worker keys too.
	srv.pool.InjectForTest("dest-"+pipe.ID, mq.NewMemoryConnector(reg, "q2"))
	_, resp = topologyGet(t, h, cookie)
	for _, c := range resp.Connections {
		connByID[c.ID] = c
	}
	if !connByID[dst.ID].Connected {
		t.Errorf("expected dst connected=true after pool inject, got %+v", connByID[dst.ID])
	}
}

// TestTopology_Unauthenticated locks in the same auth contract every
// other /api/v1/* endpoint carries.
func TestTopology_Unauthenticated(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/topology", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
