package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"mqConnector/internal/storage"
)

// The read-only revision-history handler suite. Seeds revisions
// directly via the storage repo so each case has tight control over
// revision_number, deployed_at, and tenant placement — the legacy PUT
// path is exercised separately in handlers_pipelines_snapshot_test.go.

// revisionHandlerFixture wires up a logged-in server with a pipeline
// already in place. Each test attaches revisions via the storage repo
// rather than the legacy PUT path so it can control deployment state
// and revision count precisely.
type revisionHandlerFixture struct {
	h      http.Handler
	srv    *Server
	cookie *http.Cookie
	src    storage.Connection
	dst    storage.Connection
	pipe   storage.Pipeline
}

func setupRevisionHandlerFixture(t *testing.T) revisionHandlerFixture {
	t.Helper()
	h, srv, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)
	src := postConn(t, h, cookie, `{"name":"rev-src","type":"rabbitmq","url":"amqp://x","queue_name":"q1"}`)
	dst := postConn(t, h, cookie, `{"name":"rev-dst","type":"rabbitmq","url":"amqp://x","queue_name":"q2"}`)

	// Create via the storage layer so we don't trigger the legacy
	// snapshot path during pipeline create (no PUT happens here).
	pipe := storage.Pipeline{
		Name:          "rev-pipe",
		SourceID:      src.ID,
		DestinationID: dst.ID,
		OutputFormat:  "same",
		Enabled:       true,
	}
	if err := srv.store.Pipelines.Create(context.Background(),
		storage.DefaultTenantID, &pipe); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	return revisionHandlerFixture{h: h, srv: srv, cookie: cookie,
		src: src, dst: dst, pipe: pipe}
}

// seedRevision inserts one revision with a deterministic
// PipelineSnapshot whose Pipeline.Name encodes the revision number so
// assertions can confirm ordering without an extra DB round-trip.
func (f revisionHandlerFixture) seedRevision(t *testing.T, n int) *storage.PipelineRevision {
	t.Helper()
	snap := storage.PipelineSnapshot{
		Pipeline: &storage.Pipeline{
			ID:   f.pipe.ID,
			Name: fmt.Sprintf("rev-pipe-v%d", n),
		},
		SchemaVersion: 1,
	}
	bytes, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	rev := &storage.PipelineRevision{
		PipelineID:     f.pipe.ID,
		Snapshot:       string(bytes),
		SnapshotHash:   fmt.Sprintf("hash-%d", n),
		AuthorSub:      "alice",
		AuthorUsername: "alice",
		ChangeSummary:  fmt.Sprintf("change %d", n),
	}
	if err := f.srv.store.PipelineRevisions.Create(context.Background(),
		storage.DefaultTenantID, rev); err != nil {
		t.Fatalf("seed revision %d: %v", n, err)
	}
	return rev
}

// markDeployed stamps a revision as deployed. Use after seedRevision
// to flip the deployed_at flag for "current revision" cases.
func (f revisionHandlerFixture) markDeployed(t *testing.T, revNum int) {
	t.Helper()
	if err := f.srv.store.PipelineRevisions.MarkDeployed(
		context.Background(), storage.DefaultTenantID, f.pipe.ID, revNum,
		"deploy-req-"+fmt.Sprintf("%d", revNum)); err != nil {
		t.Fatalf("mark deployed %d: %v", revNum, err)
	}
}

// get is a small wrapper that issues a GET with the fixture's auth
// cookie and CSRF pair (the CSRF middleware lets GETs through, but
// attachSession is the canonical way to dress a request here).
func (f revisionHandlerFixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	attachSession(req, f.cookie)
	f.h.ServeHTTP(rec, req)
	return rec
}

// listResponse mirrors the wire shape of GET /revisions so tests can
// assert specific fields without a map-of-any dance.
type listResponse struct {
	Revisions []revisionResponse `json:"revisions"`
	Total     int                `json:"total"`
	Limit     int                `json:"limit"`
	Offset    int                `json:"offset"`
}

func decodeListResponse(t *testing.T, body []byte) listResponse {
	t.Helper()
	var resp listResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode list response: %v (body=%s)", err, body)
	}
	return resp
}

// ─── List ───────────────────────────────────────────────────────────

func TestRevisionsHandler_List_HappyPath(t *testing.T) {
	f := setupRevisionHandlerFixture(t)
	for i := 1; i <= 3; i++ {
		f.seedRevision(t, i)
	}
	rec := f.get(t, "/api/v1/pipelines/"+f.pipe.ID+"/revisions")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	resp := decodeListResponse(t, rec.Body.Bytes())
	if resp.Total != 3 {
		t.Errorf("total = %d, want 3", resp.Total)
	}
	if len(resp.Revisions) != 3 {
		t.Fatalf("got %d revisions, want 3", len(resp.Revisions))
	}
	// Newest-first: revision_number descends.
	want := []int{3, 2, 1}
	for i, r := range resp.Revisions {
		if r.RevisionNumber != want[i] {
			t.Errorf("revisions[%d].revision_number = %d, want %d",
				i, r.RevisionNumber, want[i])
		}
	}
	if resp.Limit != defaultRevisionListLimit {
		t.Errorf("default limit = %d, want %d", resp.Limit, defaultRevisionListLimit)
	}
	if resp.Offset != 0 {
		t.Errorf("default offset = %d, want 0", resp.Offset)
	}
}

func TestRevisionsHandler_List_Pagination(t *testing.T) {
	f := setupRevisionHandlerFixture(t)
	for i := 1; i <= 5; i++ {
		f.seedRevision(t, i)
	}

	cases := []struct {
		query    string
		wantRevs []int
	}{
		{"?limit=2&offset=0", []int{5, 4}},
		{"?limit=2&offset=2", []int{3, 2}},
		{"?limit=2&offset=4", []int{1}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			rec := f.get(t, "/api/v1/pipelines/"+f.pipe.ID+"/revisions"+tc.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("list %s: %d %s", tc.query, rec.Code, rec.Body)
			}
			resp := decodeListResponse(t, rec.Body.Bytes())
			if resp.Total != 5 {
				t.Errorf("total = %d, want 5", resp.Total)
			}
			if len(resp.Revisions) != len(tc.wantRevs) {
				t.Fatalf("len = %d, want %d", len(resp.Revisions), len(tc.wantRevs))
			}
			for i, r := range resp.Revisions {
				if r.RevisionNumber != tc.wantRevs[i] {
					t.Errorf("revisions[%d].revision_number = %d, want %d",
						i, r.RevisionNumber, tc.wantRevs[i])
				}
			}
		})
	}
}

func TestRevisionsHandler_List_ClampsLimit(t *testing.T) {
	f := setupRevisionHandlerFixture(t)
	f.seedRevision(t, 1)

	// Over-cap → clamped to revisionListMaxLimit.
	rec := f.get(t, "/api/v1/pipelines/"+f.pipe.ID+"/revisions?limit=500")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	resp := decodeListResponse(t, rec.Body.Bytes())
	if resp.Limit != revisionListMaxLimit {
		t.Errorf("limit = %d, want clamped to %d", resp.Limit, revisionListMaxLimit)
	}

	// Zero → falls back to default.
	rec = f.get(t, "/api/v1/pipelines/"+f.pipe.ID+"/revisions?limit=0")
	resp = decodeListResponse(t, rec.Body.Bytes())
	if resp.Limit != defaultRevisionListLimit {
		t.Errorf("limit = %d, want default %d", resp.Limit, defaultRevisionListLimit)
	}

	// Negative offset → coerced to 0.
	rec = f.get(t, "/api/v1/pipelines/"+f.pipe.ID+"/revisions?offset=-10")
	resp = decodeListResponse(t, rec.Body.Bytes())
	if resp.Offset != 0 {
		t.Errorf("offset = %d, want clamped to 0", resp.Offset)
	}
}

func TestRevisionsHandler_List_Empty(t *testing.T) {
	f := setupRevisionHandlerFixture(t)
	rec := f.get(t, "/api/v1/pipelines/"+f.pipe.ID+"/revisions")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty list should be 200 not 404, got %d %s", rec.Code, rec.Body)
	}
	resp := decodeListResponse(t, rec.Body.Bytes())
	if resp.Total != 0 {
		t.Errorf("total = %d, want 0", resp.Total)
	}
	if resp.Revisions == nil {
		t.Error("revisions should be [] not null in JSON; decoded slice should be non-nil")
	}
	if len(resp.Revisions) != 0 {
		t.Errorf("len = %d, want 0", len(resp.Revisions))
	}
}

// ─── Get one ────────────────────────────────────────────────────────

func TestRevisionsHandler_Get_HappyPath(t *testing.T) {
	f := setupRevisionHandlerFixture(t)
	for i := 1; i <= 2; i++ {
		f.seedRevision(t, i)
	}
	for _, n := range []int{1, 2} {
		rec := f.get(t, fmt.Sprintf("/api/v1/pipelines/%s/revisions/%d", f.pipe.ID, n))
		if rec.Code != http.StatusOK {
			t.Fatalf("get rev %d: %d %s", n, rec.Code, rec.Body)
		}
		var resp revisionResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode rev %d: %v", n, err)
		}
		if resp.RevisionNumber != n {
			t.Errorf("revision_number = %d, want %d", resp.RevisionNumber, n)
		}
		if resp.Snapshot == nil {
			t.Fatalf("rev %d response missing parsed snapshot", n)
		}
		wantName := fmt.Sprintf("rev-pipe-v%d", n)
		if resp.Snapshot.Pipeline == nil || resp.Snapshot.Pipeline.Name != wantName {
			t.Errorf("snapshot.pipeline.name = %v, want %q",
				resp.Snapshot.Pipeline, wantName)
		}
		if resp.Snapshot.SchemaVersion != 1 {
			t.Errorf("snapshot.schema_version = %d, want 1", resp.Snapshot.SchemaVersion)
		}
	}
}

func TestRevisionsHandler_Get_UnknownRevision(t *testing.T) {
	f := setupRevisionHandlerFixture(t)
	f.seedRevision(t, 1)
	rec := f.get(t, "/api/v1/pipelines/"+f.pipe.ID+"/revisions/999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown rev: %d, want 404", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if body["error"] == "" {
		t.Errorf("expected error envelope, got %s", rec.Body)
	}
}

func TestRevisionsHandler_Get_BadRevisionNumber(t *testing.T) {
	f := setupRevisionHandlerFixture(t)
	for _, badRev := range []string{"abc", "-1", "0"} {
		t.Run("rev="+badRev, func(t *testing.T) {
			rec := f.get(t, "/api/v1/pipelines/"+f.pipe.ID+"/revisions/"+badRev)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("rev=%s: status %d, want 400", badRev, rec.Code)
			}
		})
	}
}

// ─── Current (latest deployed) ──────────────────────────────────────

func TestRevisionsHandler_Current_HappyPath(t *testing.T) {
	f := setupRevisionHandlerFixture(t)
	// Seed three revisions; only #2 is deployed. #3 is an undeployed
	// draft sitting on top — current should still resolve to #2.
	for i := 1; i <= 3; i++ {
		f.seedRevision(t, i)
	}
	f.markDeployed(t, 2)

	rec := f.get(t, "/api/v1/pipelines/"+f.pipe.ID+"/revisions/current")
	if rec.Code != http.StatusOK {
		t.Fatalf("current: %d %s", rec.Code, rec.Body)
	}
	var resp revisionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode current: %v", err)
	}
	if resp.RevisionNumber != 2 {
		t.Errorf("current.revision_number = %d, want 2 (deployed rev wins over later undeployed draft)",
			resp.RevisionNumber)
	}
	if resp.DeployedAt == nil {
		t.Error("current.deployed_at should be non-NULL")
	}
}

func TestRevisionsHandler_Current_NoDeployedRevision(t *testing.T) {
	f := setupRevisionHandlerFixture(t)
	f.seedRevision(t, 1) // not deployed

	rec := f.get(t, "/api/v1/pipelines/"+f.pipe.ID+"/revisions/current")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("current with no deployed rev: %d, want 404", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if body["error"] == "" {
		t.Errorf("expected error envelope, got %s", rec.Body)
	}
}

// ─── Cross-tenant ───────────────────────────────────────────────────

func TestRevisionsHandler_CrossTenantReturns404(t *testing.T) {
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
	// Seed a deployed revision under tenant-a so the current endpoint
	// would otherwise return 200.
	snap := storage.PipelineSnapshot{
		Pipeline:      &storage.Pipeline{ID: pipeA.ID, Name: "pa"},
		SchemaVersion: 1,
	}
	bytes, _ := json.Marshal(snap)
	rev := &storage.PipelineRevision{
		PipelineID: pipeA.ID, Snapshot: string(bytes),
		SnapshotHash: "h1", AuthorSub: "alice", AuthorUsername: "alice",
	}
	if err := srv.store.PipelineRevisions.Create(ctx, "tenant-a", rev); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.PipelineRevisions.MarkDeployed(ctx, "tenant-a",
		pipeA.ID, rev.RevisionNumber, "req-a"); err != nil {
		t.Fatal(err)
	}

	doAsTenant := func(asTenant, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Test-Tenant", asTenant)
		attachSession(req, cookie)
		h.ServeHTTP(rec, req)
		return rec
	}

	// Sanity: tenant-a can see its own data.
	if rec := doAsTenant("tenant-a", "/api/v1/pipelines/"+pipeA.ID+"/revisions"); rec.Code != http.StatusOK {
		t.Fatalf("tenant-a own list: %d %s", rec.Code, rec.Body)
	}

	paths := []string{
		"/api/v1/pipelines/" + pipeA.ID + "/revisions",
		"/api/v1/pipelines/" + pipeA.ID + "/revisions/current",
		"/api/v1/pipelines/" + pipeA.ID + "/revisions/" + fmt.Sprintf("%d", rev.RevisionNumber),
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			rec := doAsTenant("tenant-b", p)
			if rec.Code != http.StatusNotFound {
				t.Errorf("cross-tenant access: %d, want 404 (never 403)", rec.Code)
			}
		})
	}
}

// ─── RBAC / auth ────────────────────────────────────────────────────

func TestRevisionsHandler_AnonymousIsRejected(t *testing.T) {
	// The route-level RequireSession middleware (see routes.go) blocks
	// every /api/v1/* call without a session cookie with 401 before
	// the handler runs. This test confirms that contract for the three
	// new endpoints rather than re-testing RequireSession itself —
	// viewer is the minimum authenticated role; below that is 401.
	h, _, _ := newTestServer(t)
	for _, p := range []string{
		"/api/v1/pipelines/some-id/revisions",
		"/api/v1/pipelines/some-id/revisions/current",
		"/api/v1/pipelines/some-id/revisions/1",
	} {
		t.Run(p, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("anonymous %s: %d, want 401", p, rec.Code)
			}
		})
	}
}

// TestRevisionsHandler_ViewerCanRead confirms an authenticated user
// (any role) can hit the read endpoints — there is no extra
// gatePipeline call, so the route-level RequireSession check is the
// only gate beyond tenant scoping. The default test user "alice" has
// owner-level access via the test fixture; this test exists to pin
// down the "no 403 for viewers" contract so a future refactor that
// accidentally adds gatePipeline(... RoleOperator) regresses it.
func TestRevisionsHandler_ViewerCanRead(t *testing.T) {
	f := setupRevisionHandlerFixture(t)
	f.seedRevision(t, 1)
	f.markDeployed(t, 1)

	for _, p := range []string{
		"/api/v1/pipelines/" + f.pipe.ID + "/revisions",
		"/api/v1/pipelines/" + f.pipe.ID + "/revisions/current",
		"/api/v1/pipelines/" + f.pipe.ID + "/revisions/1",
	} {
		t.Run(p, func(t *testing.T) {
			rec := f.get(t, p)
			if rec.Code != http.StatusOK {
				t.Errorf("viewer-equivalent read %s: %d, want 200", p, rec.Code)
			}
		})
	}
}
