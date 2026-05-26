package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mqConnector/internal/dlq/cluster"
	"mqConnector/internal/storage"
)

// DLQ Intelligence Console handler tests (Wave 3 Task 3).
//
// Covers three endpoints:
//   - GET  /api/v1/dlq/clusters
//   - POST /api/v1/dlq/{id}/replay-sim
//   - GET  /api/v1/dlq/{id}/diff
//
// Each test seeds DLQ rows directly via the storage repo so it can
// control the fingerprint, pipeline, stage attribution, and tenant
// placement precisely — the executor path is exercised separately.

// seedDLQEntry inserts one DLQ row with a precomputed fingerprint so
// the cluster tests don't depend on the executor's Push path. The
// caller's pipelineID can be empty (legacy / bridge failure) or any
// id — the helper auto-creates a thin pipeline + connection pair in
// tenantID for non-empty pipeline ids so the FK constraint on
// dlq.pipeline_id is satisfied. The pipeline is created idempotently
// — repeat calls reuse the existing row.
func seedDLQEntry(t *testing.T, srv *Server, tenantID, pipelineID, errReason, stage string, msg []byte) *storage.DLQEntry {
	t.Helper()
	if pipelineID != "" {
		ensurePipeline(t, srv, tenantID, pipelineID)
	}
	fp := cluster.FingerprintWithStage(errReason, stage)
	e := &storage.DLQEntry{
		PipelineID:        pipelineID,
		OriginalMsg:       msg,
		ErrorReason:       errReason,
		ErrorFingerprint:  fp.Fingerprint,
		ErrorTemplate:     fp.Template,
		FailingStageName:  stage,
		FailingStageIndex: 0,
	}
	if err := srv.store.DLQ.Insert(context.Background(), tenantID, e); err != nil {
		t.Fatalf("seed dlq: %v", err)
	}
	return e
}

// ensurePipeline guarantees a pipeline + paired connection exists for
// (tenantID, pipelineID), creating both on first call and no-op'ing
// on subsequent calls. Used by seedDLQEntry so cluster tests can
// reference an arbitrary pipeline id without managing the FK chain
// per case.
func ensurePipeline(t *testing.T, srv *Server, tenantID, pipelineID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := srv.store.Pipelines.Get(ctx, tenantID, pipelineID); err == nil {
		return // already exists
	}
	// Build a connection first; pipelines require source + destination.
	conn := &storage.Connection{
		Name: "conn-" + pipelineID,
		Type: "rabbitmq",
		URL:  "amqp://test",
	}
	if err := srv.store.Connections.Create(ctx, tenantID, conn); err != nil {
		t.Fatalf("seed connection for %s: %v", pipelineID, err)
	}
	pipe := &storage.Pipeline{
		ID:            pipelineID,
		Name:          "pipe-" + pipelineID,
		SourceID:      conn.ID,
		DestinationID: conn.ID,
		OutputFormat:  "same",
		Enabled:       false,
	}
	if err := srv.store.Pipelines.Create(ctx, tenantID, pipe); err != nil {
		t.Fatalf("seed pipeline %s: %v", pipelineID, err)
	}
}

// getJSON does a GET with the test session cookie and decodes the
// response body into v. Fails the test if the status code isn't want.
func getJSON(t *testing.T, h http.Handler, cookie *http.Cookie, path string, want int, v any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("GET %s: status=%d want=%d body=%s", path, rec.Code, want, rec.Body)
	}
	if v != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("decode %s: %v body=%s", path, err, rec.Body)
		}
	}
	return rec
}

// postJSON does a POST with the test session cookie + empty body and
// decodes the response into v. Fails the test if status != want.
func postJSON(t *testing.T, h http.Handler, cookie *http.Cookie, path string, want int, v any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("POST %s: status=%d want=%d body=%s", path, rec.Code, want, rec.Body)
	}
	if v != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("decode %s: %v body=%s", path, err, rec.Body)
		}
	}
	return rec
}

// ─── Clusters endpoint ───────────────────────────────────────────────

// TestDLQClusters_EmptyTenantReturnsEmptyArray asserts the endpoint
// returns 200 + an empty `clusters` array when the tenant has no DLQ
// entries — UI consumers should never see 404 for a healthy tenant.
func TestDLQClusters_EmptyTenantReturnsEmptyArray(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	var resp clustersResponse
	getJSON(t, h, cookie, "/api/v1/dlq/clusters", http.StatusOK, &resp)
	if resp.Total != 0 || resp.Returned != 0 {
		t.Errorf("empty tenant total/returned = %d/%d, want 0/0", resp.Total, resp.Returned)
	}
	if resp.Clusters == nil {
		t.Error("clusters should be a non-nil empty array")
	}
	if resp.GeneratedAt.IsZero() {
		t.Error("generated_at should be set")
	}
}

// TestDLQClusters_GroupsSameFingerprint seeds 3 rows whose error
// strings collapse to the same template. One cluster should come back
// with count=3; representative_id is the OLDEST row; recent_ids
// orders newest-first.
func TestDLQClusters_GroupsSameFingerprint(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	tenant := storage.DefaultTenantID

	// Three errors that should collapse to one template ("validation:
	// missing field <FIELD>"). Seed oldest first; advance the clock
	// between inserts so recent_ids has a deterministic order.
	e1 := seedDLQEntry(t, srv, tenant, "p1", "validation: missing field customer.id", "validate", []byte("a"))
	bumpDLQTimestamp(t, srv, e1.ID, time.Now().Add(-3*time.Hour))
	e2 := seedDLQEntry(t, srv, tenant, "p1", "validation: missing field order.id", "validate", []byte("b"))
	bumpDLQTimestamp(t, srv, e2.ID, time.Now().Add(-2*time.Hour))
	e3 := seedDLQEntry(t, srv, tenant, "p1", "validation: missing field payment.id", "validate", []byte("c"))
	bumpDLQTimestamp(t, srv, e3.ID, time.Now().Add(-1*time.Hour))

	var resp clustersResponse
	getJSON(t, h, cookie, "/api/v1/dlq/clusters", http.StatusOK, &resp)
	if len(resp.Clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d (clusters=%+v)", len(resp.Clusters), resp.Clusters)
	}
	c := resp.Clusters[0]
	if c.Count != 3 {
		t.Errorf("count = %d, want 3", c.Count)
	}
	if c.RepresentativeID != e1.ID {
		t.Errorf("representative = %s, want oldest %s", c.RepresentativeID, e1.ID)
	}
	if len(c.RecentIDs) != 3 {
		t.Fatalf("recent_ids = %v, want 3 entries", c.RecentIDs)
	}
	// Newest first.
	if c.RecentIDs[0] != e3.ID || c.RecentIDs[2] != e1.ID {
		t.Errorf("recent_ids ordering = %v, want [%s, %s, %s]",
			c.RecentIDs, e3.ID, e2.ID, e1.ID)
	}
	if len(c.PipelinesAffected) != 1 || c.PipelinesAffected[0] != "p1" {
		t.Errorf("pipelines_affected = %v, want [p1]", c.PipelinesAffected)
	}
	if len(c.FailingStages) != 1 || c.FailingStages[0] != "validate" {
		t.Errorf("failing_stages = %v, want [validate]", c.FailingStages)
	}
	// first_seen / last_seen must be populated — operators use these
	// to triage cluster lifetime. A zero-time slip would silently
	// render the timestamp gutters as 1 Jan 0001 in the UI.
	if c.FirstSeen.IsZero() {
		t.Errorf("first_seen is zero — driver timestamp scan must not silently drop")
	}
	if c.LastSeen.IsZero() {
		t.Errorf("last_seen is zero — driver timestamp scan must not silently drop")
	}
	if c.LastSeen.Before(c.FirstSeen) {
		t.Errorf("last_seen %v before first_seen %v", c.LastSeen, c.FirstSeen)
	}
}

// TestDLQClusters_DistinctFingerprintsOrderedByCount seeds two
// clusters with 3 and 1 entries respectively; the 3-cluster must
// come first.
func TestDLQClusters_DistinctFingerprintsOrderedByCount(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	tenant := storage.DefaultTenantID

	// Cluster A: 3 entries.
	for i := 0; i < 3; i++ {
		seedDLQEntry(t, srv, tenant, "p1", "validation: missing field x", "validate", []byte("a"))
	}
	// Cluster B: 1 entry.
	seedDLQEntry(t, srv, tenant, "p1", "TLS handshake error", "send", []byte("b"))

	var resp clustersResponse
	getJSON(t, h, cookie, "/api/v1/dlq/clusters", http.StatusOK, &resp)
	if len(resp.Clusters) != 2 {
		t.Fatalf("got %d clusters, want 2", len(resp.Clusters))
	}
	if resp.Clusters[0].Count != 3 || resp.Clusters[1].Count != 1 {
		t.Errorf("ordering broken: counts = [%d, %d]",
			resp.Clusters[0].Count, resp.Clusters[1].Count)
	}
}

// TestDLQClusters_MinCountFiltersSingletons proves min_count=2
// excludes clusters with fewer than 2 entries.
func TestDLQClusters_MinCountFiltersSingletons(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	tenant := storage.DefaultTenantID

	// 1 cluster of 2 (dotted field names collapse to <FIELD>), 1 singleton.
	seedDLQEntry(t, srv, tenant, "p1", "validation: missing field customer.id", "validate", []byte("a"))
	seedDLQEntry(t, srv, tenant, "p1", "validation: missing field order.id", "validate", []byte("b"))
	seedDLQEntry(t, srv, tenant, "p1", "TLS handshake error", "send", []byte("c"))

	var resp clustersResponse
	getJSON(t, h, cookie, "/api/v1/dlq/clusters?min_count=2", http.StatusOK, &resp)
	if len(resp.Clusters) != 1 {
		t.Fatalf("got %d clusters with min_count=2, want 1", len(resp.Clusters))
	}
	if resp.Clusters[0].Count != 2 {
		t.Errorf("filtered cluster count = %d, want 2", resp.Clusters[0].Count)
	}
}

// TestDLQClusters_PipelineIDFilterScopes confirms the pipeline_id
// query parameter narrows the rollup to a single pipeline.
func TestDLQClusters_PipelineIDFilterScopes(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	tenant := storage.DefaultTenantID

	for i := 0; i < 2; i++ {
		seedDLQEntry(t, srv, tenant, "p1", "validation: missing field a", "validate", []byte("a"))
	}
	for i := 0; i < 3; i++ {
		seedDLQEntry(t, srv, tenant, "p2", "TLS handshake error", "send", []byte("b"))
	}

	var resp clustersResponse
	getJSON(t, h, cookie, "/api/v1/dlq/clusters?pipeline_id=p2", http.StatusOK, &resp)
	if len(resp.Clusters) != 1 {
		t.Fatalf("got %d clusters scoped to p2, want 1", len(resp.Clusters))
	}
	if resp.Clusters[0].Count != 3 {
		t.Errorf("p2 cluster count = %d, want 3", resp.Clusters[0].Count)
	}
	if len(resp.Clusters[0].PipelinesAffected) != 1 || resp.Clusters[0].PipelinesAffected[0] != "p2" {
		t.Errorf("pipelines_affected = %v", resp.Clusters[0].PipelinesAffected)
	}
}

// TestDLQClusters_CrossTenantIsolation seeds rows in two tenants and
// confirms the rollup only sees the caller's tenant.
func TestDLQClusters_CrossTenantIsolation(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	// Alice is admin on the default tenant. Drop a row in another
	// tenant — the default-tenant scoped clusters must not see it.
	seedDLQEntry(t, srv, storage.DefaultTenantID, "p1", "tenant-a failure", "validate", []byte("a"))
	seedDLQEntry(t, srv, "tenant-other", "p9", "tenant-other failure", "validate", []byte("b"))

	var resp clustersResponse
	getJSON(t, h, cookie, "/api/v1/dlq/clusters", http.StatusOK, &resp)
	if len(resp.Clusters) != 1 {
		t.Fatalf("got %d clusters, want 1 (cross-tenant leak)", len(resp.Clusters))
	}
	// Only the default-tenant row's template/fingerprint should appear.
	if !strings.Contains(resp.Clusters[0].Template, "tenant-a failure") {
		t.Errorf("unexpected template %q (cross-tenant leak?)", resp.Clusters[0].Template)
	}
}

// TestDLQClusters_EmptyFingerprintRowsExcluded confirms legacy rows
// (written before migration 0023 — represented here by an entry with
// no fingerprint set) don't show up in the clusters response.
func TestDLQClusters_EmptyFingerprintRowsExcluded(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	tenant := storage.DefaultTenantID

	// One legacy row (no fingerprint).
	ensurePipeline(t, srv, tenant, "p1")
	legacy := &storage.DLQEntry{
		PipelineID:  "p1",
		OriginalMsg: []byte("legacy"),
		ErrorReason: "legacy failure",
		// ErrorFingerprint and ErrorTemplate left empty.
	}
	if err := srv.store.DLQ.Insert(context.Background(), tenant, legacy); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	// One modern row.
	seedDLQEntry(t, srv, tenant, "p1", "validation: missing field x", "validate", []byte("a"))

	var resp clustersResponse
	getJSON(t, h, cookie, "/api/v1/dlq/clusters", http.StatusOK, &resp)
	if len(resp.Clusters) != 1 {
		t.Fatalf("got %d clusters, want 1 (legacy row should not produce a cluster)", len(resp.Clusters))
	}
	if resp.Clusters[0].Fingerprint == "" {
		t.Error("returned a cluster with empty fingerprint")
	}
}

// ─── Replay-sim endpoint ────────────────────────────────────────────

// replaySimFixture is the rollback fixture plus a DLQ entry attached
// to its pipeline. Tests pick a stage shape per case (succeed vs
// fail) via the seedRevision helper.
type replaySimFixture struct {
	rollbackDeployFixture
	entry *storage.DLQEntry
}

func setupReplaySim(t *testing.T) replaySimFixture {
	t.Helper()
	f := setupRollbackDeployFixture(t)
	// Seed a DLQ entry against this pipeline with a JSON body the
	// validate stage will reject (when we use validate); the body is
	// also valid for filter so the happy path can pass.
	entry := seedDLQEntry(t, f.srv, storage.DefaultTenantID, f.pipe.ID,
		"validation: missing required field",
		"validate",
		[]byte(`{"id":"x"}`))
	return replaySimFixture{rollbackDeployFixture: f, entry: entry}
}

// TestReplaySim_HappyPathFailingStage seeds a deployed pipeline with a
// validate stage whose schema demands a `name` field; the entry's
// payload is missing that field → would_succeed=false, stage_runs
// populated, failing_stage="validate".
func TestReplaySim_HappyPathFailingStage(t *testing.T) {
	f := setupReplaySim(t)
	ctx := context.Background()

	// Deploy a revision whose stages will fail on this payload. We use
	// a validate stage with an inline JSON schema that requires `name`.
	stages := []*storage.Stage{
		{
			StageOrder:  1,
			StageType:   "validate",
			StageConfig: `{"schema_type":"json","content":"{\"type\":\"object\",\"required\":[\"name\"]}"}`,
			Enabled:     true,
		},
	}
	rev := f.seedRevisionWithSnapshot(t, "pipe-v1", stages, "")
	if err := f.srv.store.PipelineRevisions.MarkDeployed(ctx,
		storage.DefaultTenantID, f.pipe.ID, rev.RevisionNumber, "req-1"); err != nil {
		t.Fatalf("mark deployed: %v", err)
	}

	var resp replaySimResponse
	postJSON(t, f.h, f.cookie,
		fmt.Sprintf("/api/v1/dlq/%s/replay-sim", f.entry.ID),
		http.StatusOK, &resp)

	if resp.EntryID != f.entry.ID {
		t.Errorf("entry_id = %q, want %q", resp.EntryID, f.entry.ID)
	}
	if resp.PipelineID != f.pipe.ID {
		t.Errorf("pipeline_id = %q, want %q", resp.PipelineID, f.pipe.ID)
	}
	if resp.RevisionNumber != rev.RevisionNumber {
		t.Errorf("revision_number = %d, want %d", resp.RevisionNumber, rev.RevisionNumber)
	}
	if resp.WouldSucceed {
		t.Errorf("would_succeed = true; want false (validate should fail on missing field)")
	}
	if len(resp.StageRuns) == 0 {
		t.Errorf("stage_runs empty")
	}
	if resp.FailingStage == "" {
		t.Errorf("failing_stage empty")
	}
	if resp.Error == "" {
		t.Errorf("error empty")
	}
}

// TestReplaySim_NoDeployedRevision_409 — the entry's pipeline has no
// deployed revision yet → 409 with a clear error.
func TestReplaySim_NoDeployedRevision_409(t *testing.T) {
	f := setupReplaySim(t)
	// No revision marked deployed. (We could seed one without
	// deploying, but the test should also cover "no revision at all".)
	rec := postJSON(t, f.h, f.cookie,
		fmt.Sprintf("/api/v1/dlq/%s/replay-sim", f.entry.ID),
		http.StatusConflict, nil)
	if !strings.Contains(rec.Body.String(), "no deployed revision") {
		t.Errorf("body = %s, want mention of 'no deployed revision'", rec.Body)
	}
}

// TestReplaySim_UnknownEntry_404 — unknown DLQ id returns 404.
func TestReplaySim_UnknownEntry_404(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	postJSON(t, h, cookie, "/api/v1/dlq/does-not-exist/replay-sim", http.StatusNotFound, nil)
}

// TestReplaySim_CrossTenant_404 — an entry in tenant B is not visible
// to a caller scoped to tenant A.
func TestReplaySim_CrossTenant_404(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	// Insert a DLQ entry in a different tenant.
	other := seedDLQEntry(t, srv, "tenant-other", "p-other",
		"foo", "validate", []byte(`{}`))
	postJSON(t, h, cookie,
		fmt.Sprintf("/api/v1/dlq/%s/replay-sim", other.ID),
		http.StatusNotFound, nil)
}

// TestReplaySim_ViewerForbidden_403 — a viewer (via the X-Test-Role
// tenant switcher) cannot trigger a replay simulation.
func TestReplaySim_ViewerForbidden_403(t *testing.T) {
	f := setupReplaySim(t)
	ctx := context.Background()
	f.srv.auth.SetTenantResolver(tenantSwitcher{
		defaultTenant: storage.DefaultTenantID, defaultRole: "viewer"})
	// Membership keeps the resolver consistent if it ever falls
	// through to the membership lookup.
	_ = f.srv.store.Memberships.Upsert(ctx, &storage.Membership{
		TenantID: storage.DefaultTenantID, UserSub: "alice",
		Username: "alice", Role: storage.RoleViewer})

	// Deploy a revision so the handler reaches the gatePipeline check
	// — without a deployed revision the handler would 409 before
	// hitting RBAC, masking the actual gate behaviour. The order is
	// "entry lookup → operator gate → deployed revision lookup".
	stages := []*storage.Stage{
		{StageOrder: 1, StageType: "filter", StageConfig: `{}`, Enabled: true},
	}
	rev := f.seedRevisionWithSnapshot(t, "pipe-v1", stages, "")
	if err := f.srv.store.PipelineRevisions.MarkDeployed(ctx,
		storage.DefaultTenantID, f.pipe.ID, rev.RevisionNumber, "req-1"); err != nil {
		t.Fatalf("mark deployed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/dlq/%s/replay-sim", f.entry.ID), nil)
	req.Header.Set("X-Test-Role", "viewer")
	attachSession(req, f.cookie)
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer replay-sim: %d, want 403 (body=%s)", rec.Code, rec.Body)
	}
}

// ─── Diff endpoint ──────────────────────────────────────────────────

// TestDLQDiff_IdenticalBodies — two entries with byte-identical
// payloads → every op is "eq".
func TestDLQDiff_IdenticalBodies(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	tenant := storage.DefaultTenantID

	body := []byte("line one\nline two\nline three")
	e1 := seedDLQEntry(t, srv, tenant, "p1", "x", "validate", body)
	e2 := seedDLQEntry(t, srv, tenant, "p1", "y", "validate", body)

	var resp dlqDiffResponse
	getJSON(t, h, cookie,
		fmt.Sprintf("/api/v1/dlq/%s/diff?against=%s", e1.ID, e2.ID),
		http.StatusOK, &resp)
	if len(resp.Diff) != 3 {
		t.Fatalf("diff = %d ops, want 3", len(resp.Diff))
	}
	for i, op := range resp.Diff {
		if op.Op != "eq" {
			t.Errorf("op[%d] = %s, want eq", i, op.Op)
		}
	}
	if resp.From.ID != e1.ID || resp.To.ID != e2.ID {
		t.Errorf("from/to = %s/%s, want %s/%s", resp.From.ID, resp.To.ID, e1.ID, e2.ID)
	}
}

// TestDLQDiff_OneLineChanged — a single-line change produces one del
// and one add at the same position, surrounded by eq ops.
func TestDLQDiff_OneLineChanged(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	tenant := storage.DefaultTenantID

	body1 := []byte("alpha\nbeta\ngamma")
	body2 := []byte("alpha\nBETA\ngamma")
	e1 := seedDLQEntry(t, srv, tenant, "p1", "x", "validate", body1)
	e2 := seedDLQEntry(t, srv, tenant, "p1", "y", "validate", body2)

	var resp dlqDiffResponse
	getJSON(t, h, cookie,
		fmt.Sprintf("/api/v1/dlq/%s/diff?against=%s", e1.ID, e2.ID),
		http.StatusOK, &resp)
	// Expected sequence (del-before-add tie-break):
	// eq alpha; del beta; add BETA; eq gamma.
	if len(resp.Diff) != 4 {
		t.Fatalf("diff = %d ops, want 4 (ops=%+v)", len(resp.Diff), resp.Diff)
	}
	want := []dlqLineOp{
		{Op: "eq", Text: "alpha"},
		{Op: "del", Text: "beta"},
		{Op: "add", Text: "BETA"},
		{Op: "eq", Text: "gamma"},
	}
	for i, w := range want {
		if resp.Diff[i] != w {
			t.Errorf("op[%d] = %+v, want %+v", i, resp.Diff[i], w)
		}
	}
}

// TestDLQDiff_CrossTenantOnEitherSide — an id from another tenant
// produces 404 regardless of which side it appears on.
func TestDLQDiff_CrossTenantOnEitherSide(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	mine := seedDLQEntry(t, srv, storage.DefaultTenantID, "p1", "x", "validate", []byte("a"))
	other := seedDLQEntry(t, srv, "tenant-other", "p9", "y", "validate", []byte("b"))

	// other on the right.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/dlq/%s/diff?against=%s", mine.ID, other.ID), nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("mine vs other: %d, want 404", rec.Code)
	}

	// other on the left.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/dlq/%s/diff?against=%s", other.ID, mine.ID), nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("other vs mine: %d, want 404", rec.Code)
	}
}

// TestDLQDiff_MissingAgainstParam_400 — the `against` query parameter
// is required.
func TestDLQDiff_MissingAgainstParam_400(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	e := seedDLQEntry(t, srv, storage.DefaultTenantID, "p1", "x", "validate", []byte("a"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/dlq/%s/diff", e.ID), nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing against: %d, want 400", rec.Code)
	}
}

// TestDLQDiff_AgainstSelf — diffing an entry against itself is
// allowed and yields an all-eq diff (not an error).
func TestDLQDiff_AgainstSelf(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	e := seedDLQEntry(t, srv, storage.DefaultTenantID, "p1", "x", "validate",
		[]byte("one\ntwo\nthree"))

	var resp dlqDiffResponse
	getJSON(t, h, cookie,
		fmt.Sprintf("/api/v1/dlq/%s/diff?against=%s", e.ID, e.ID),
		http.StatusOK, &resp)
	if len(resp.Diff) != 3 {
		t.Fatalf("diff = %d, want 3 eq ops", len(resp.Diff))
	}
	for i, op := range resp.Diff {
		if op.Op != "eq" {
			t.Errorf("op[%d] = %s, want eq", i, op.Op)
		}
	}
	if resp.From.ID != resp.To.ID {
		t.Errorf("from.id %s != to.id %s when diffing against self", resp.From.ID, resp.To.ID)
	}
}

// ─── helpers ────────────────────────────────────────────────────────

// bumpDLQTimestamp forces a DLQ row's created_at to t. Used by cluster
// tests that need deterministic newest/oldest ordering without
// inserting actual sleeps.
func bumpDLQTimestamp(t *testing.T, srv *Server, id string, when time.Time) {
	t.Helper()
	if _, err := srv.store.DB.Exec(`UPDATE dlq SET created_at = ? WHERE id = ?`,
		when.UTC(), id); err != nil {
		t.Fatalf("bump created_at: %v", err)
	}
}
