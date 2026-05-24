package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mqConnector/internal/storage"
)

// snapshotTestFixture seeds a pipeline (plus its source/destination
// connections) for the legacy-PUT snapshot tests. Returns the handler,
// the server, the auth cookie, and the pipeline id. CreatePipeline is
// not wired to snapshot per task spec — the first PUT in each test
// is what creates revision 1.
type snapshotTestFixture struct {
	h      http.Handler
	srv    *Server
	cookie *http.Cookie
	src    storage.Connection
	dst    storage.Connection
	pipe   storage.Pipeline
}

func setupSnapshotFixture(t *testing.T) snapshotTestFixture {
	t.Helper()
	h, srv, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)
	src := postConn(t, h, cookie, `{"name":"snap-src","type":"rabbitmq","url":"amqp://x","queue_name":"q1"}`)
	dst := postConn(t, h, cookie, `{"name":"snap-dst","type":"rabbitmq","url":"amqp://x","queue_name":"q2"}`)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"name":"snap-pipe","source_id":"` + src.ID +
		`","destination_id":"` + dst.ID + `","output_format":"same","enabled":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines", body)
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create pipeline: %d %s", rec.Code, rec.Body)
	}
	var pipe storage.Pipeline
	_ = json.Unmarshal(rec.Body.Bytes(), &pipe)
	return snapshotTestFixture{h: h, srv: srv, cookie: cookie, src: src, dst: dst, pipe: pipe}
}

// putJSON is a small helper to PUT a JSON body with a valid auth +
// CSRF session. The two snapshot tests reach for it a lot.
func (f snapshotTestFixture) putJSON(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, f.cookie)
	f.h.ServeHTTP(rec, req)
	return rec
}

// TestSnapshot_UpdatePipelineRecordsRevision covers the first PUT
// path: PUT /api/v1/pipelines/{id} succeeds → one revision row with
// revision_number=1, deployed_at non-NULL, the right author, the
// right change summary, and a snapshot that decodes back to the
// pipeline we wrote.
func TestSnapshot_UpdatePipelineRecordsRevision(t *testing.T) {
	f := setupSnapshotFixture(t)
	ctx := context.Background()

	body := `{"name":"snap-pipe-renamed","source_id":"` + f.src.ID +
		`","destination_id":"` + f.dst.ID + `","output_format":"same","enabled":true}`
	rec := f.putJSON(t, "/api/v1/pipelines/"+f.pipe.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update pipeline: %d %s", rec.Code, rec.Body)
	}

	list, total, err := f.srv.store.PipelineRevisions.List(ctx,
		storage.DefaultTenantID, f.pipe.ID, 50, 0)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected exactly 1 revision, got total=%d len=%d", total, len(list))
	}
	rev := list[0]
	if rev.RevisionNumber != 1 {
		t.Errorf("revision_number = %d, want 1", rev.RevisionNumber)
	}
	if rev.DeployedAt == nil {
		t.Error("deployed_at should be non-NULL for legacy save-and-ship")
	}
	if rev.AuthorSub != "alice" {
		t.Errorf("author_sub = %q, want %q", rev.AuthorSub, "alice")
	}
	if rev.AuthorUsername != "alice" {
		t.Errorf("author_username = %q, want %q", rev.AuthorUsername, "alice")
	}
	if rev.ChangeSummary != "Update pipeline metadata" {
		t.Errorf("change_summary = %q", rev.ChangeSummary)
	}
	var snap storage.PipelineSnapshot
	if err := json.Unmarshal([]byte(rev.Snapshot), &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Pipeline == nil || snap.Pipeline.Name != "snap-pipe-renamed" {
		t.Errorf("snapshot pipeline name = %v, want %q", snap.Pipeline, "snap-pipe-renamed")
	}
	if snap.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", snap.SchemaVersion)
	}
}

// TestSnapshot_ReplaceStagesRecordsRevision covers the second PUT
// path: a metadata PUT creates rev 1; a stages PUT then creates
// rev 2 whose embedded Stages slice matches the body we sent.
func TestSnapshot_ReplaceStagesRecordsRevision(t *testing.T) {
	f := setupSnapshotFixture(t)
	ctx := context.Background()

	// Bump rev 1 with a metadata update so we can confirm rev 2
	// comes from the stages PUT specifically.
	metaBody := `{"name":"snap-pipe","source_id":"` + f.src.ID +
		`","destination_id":"` + f.dst.ID + `","output_format":"same","enabled":true}`
	if rec := f.putJSON(t, "/api/v1/pipelines/"+f.pipe.ID, metaBody); rec.Code != http.StatusOK {
		t.Fatalf("seed rev 1: %d %s", rec.Code, rec.Body)
	}

	stagesBody := `[{"stage_order":1,"stage_type":"filter","stage_config":"{}","enabled":true},` +
		`{"stage_order":2,"stage_type":"transform","stage_config":"{}","enabled":true}]`
	rec := f.putJSON(t, "/api/v1/pipelines/"+f.pipe.ID+"/stages", stagesBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace stages: %d %s", rec.Code, rec.Body)
	}

	latest, err := f.srv.store.PipelineRevisions.Latest(ctx,
		storage.DefaultTenantID, f.pipe.ID)
	if err != nil {
		t.Fatalf("latest revision: %v", err)
	}
	if latest.RevisionNumber != 2 {
		t.Errorf("expected rev 2, got %d", latest.RevisionNumber)
	}
	if latest.DeployedAt == nil {
		t.Error("deployed_at should be non-NULL")
	}
	if latest.ChangeSummary != "Replace stages" {
		t.Errorf("change_summary = %q", latest.ChangeSummary)
	}
	var snap storage.PipelineSnapshot
	if err := json.Unmarshal([]byte(latest.Snapshot), &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.Stages) != 2 {
		t.Fatalf("snapshot stages len = %d, want 2", len(snap.Stages))
	}
	// Stages must be stored in stage_order order — the snapshot is
	// the executor's view of the world.
	if snap.Stages[0].StageOrder != 1 || snap.Stages[0].StageType != "filter" {
		t.Errorf("stage[0] = %+v", snap.Stages[0])
	}
	if snap.Stages[1].StageOrder != 2 || snap.Stages[1].StageType != "transform" {
		t.Errorf("stage[1] = %+v", snap.Stages[1])
	}
}

// TestSnapshot_HashDedupOnIdenticalPUTs walks through two identical
// PUTs and asserts the hash-dedup at the repo layer collapses them
// into a single revision row. LatestDeployed must still point at
// the single (original) revision so a later deploy isn't accidentally
// reset.
func TestSnapshot_HashDedupOnIdenticalPUTs(t *testing.T) {
	f := setupSnapshotFixture(t)
	ctx := context.Background()

	body := `{"name":"snap-pipe-once","source_id":"` + f.src.ID +
		`","destination_id":"` + f.dst.ID + `","output_format":"same","enabled":true}`

	// First PUT → revision 1.
	if rec := f.putJSON(t, "/api/v1/pipelines/"+f.pipe.ID, body); rec.Code != http.StatusOK {
		t.Fatalf("put 1: %d %s", rec.Code, rec.Body)
	}
	first, err := f.srv.store.PipelineRevisions.Latest(ctx,
		storage.DefaultTenantID, f.pipe.ID)
	if err != nil {
		t.Fatalf("latest after put 1: %v", err)
	}
	if first.RevisionNumber != 1 {
		t.Fatalf("first rev should be 1, got %d", first.RevisionNumber)
	}
	firstID := first.ID

	// Second PUT with byte-identical body → hash dedup → no new row.
	if rec := f.putJSON(t, "/api/v1/pipelines/"+f.pipe.ID, body); rec.Code != http.StatusOK {
		t.Fatalf("put 2: %d %s", rec.Code, rec.Body)
	}
	_, total, err := f.srv.store.PipelineRevisions.List(ctx,
		storage.DefaultTenantID, f.pipe.ID, 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 revision (dedup), got %d", total)
	}
	deployed, err := f.srv.store.PipelineRevisions.LatestDeployed(ctx,
		storage.DefaultTenantID, f.pipe.ID)
	if err != nil {
		t.Fatalf("latest deployed: %v", err)
	}
	if deployed.ID != firstID {
		t.Errorf("LatestDeployed should still be the original rev (%s), got %s",
			firstID, deployed.ID)
	}
}

// TestSnapshot_FailureDoesNotRollbackLiveWrite verifies the
// best-effort contract: if the snapshot side-channel breaks, the
// HTTP request still succeeds and the live tables still hold the
// new state. We force the failure by swapping the
// PipelineRevisions repo for a broken stub for the duration of the
// PUT.
func TestSnapshot_FailureDoesNotRollbackLiveWrite(t *testing.T) {
	f := setupSnapshotFixture(t)
	ctx := context.Background()

	// Nil out the repo so any helper call into it panics — except
	// the helper guards against a nil repo at entry, so the call
	// is a silent no-op. To genuinely exercise the error path, we
	// instead drop the underlying table so Create returns an
	// error from the database driver. The helper must swallow
	// that error and let the response through unchanged.
	if _, err := f.srv.store.DB.ExecContext(ctx,
		`DROP TABLE pipeline_revisions`); err != nil {
		t.Fatalf("drop pipeline_revisions: %v", err)
	}

	body := `{"name":"snap-pipe-renamed","source_id":"` + f.src.ID +
		`","destination_id":"` + f.dst.ID + `","output_format":"same","enabled":true}`
	rec := f.putJSON(t, "/api/v1/pipelines/"+f.pipe.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT should still succeed despite snapshot failure: %d %s",
			rec.Code, rec.Body)
	}

	// Live table reflects the write — snapshot failure didn't
	// reach in and roll it back.
	got, err := f.srv.store.Pipelines.Get(ctx, storage.DefaultTenantID, f.pipe.ID)
	if err != nil {
		t.Fatalf("get pipeline: %v", err)
	}
	if got.Name != "snap-pipe-renamed" {
		t.Errorf("pipeline name = %q, want %q (live write must not roll back)",
			got.Name, "snap-pipe-renamed")
	}
}

// TestSnapshot_TenantScoping confirms revisions are stamped with the
// acting tenant's id and are invisible to a different tenant's List.
// Uses the same tenant-switcher fixture as the isolation suite so a
// single login can act as two tenants.
func TestSnapshot_TenantScoping(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	// Two tenants, alice owns both. The acting tenant for each
	// request is picked from X-Test-Tenant by the switcher.
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

	// Seed a pipeline under tenant A via the storage layer.
	connA := &storage.Connection{Name: "ca", Type: "rabbitmq", URL: "amqp://a"}
	if err := srv.store.Connections.Create(ctx, "tenant-a", connA); err != nil {
		t.Fatal(err)
	}
	pipeA := &storage.Pipeline{Name: "pa", SourceID: connA.ID,
		DestinationID: connA.ID, OutputFormat: "same", Enabled: false}
	if err := srv.store.Pipelines.Create(ctx, "tenant-a", pipeA); err != nil {
		t.Fatal(err)
	}

	// Update as tenant-a → revision lands under tenant-a.
	rec := httptest.NewRecorder()
	body := `{"name":"pa-renamed","source_id":"` + connA.ID +
		`","destination_id":"` + connA.ID + `","output_format":"same","enabled":false}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/pipelines/"+pipeA.ID,
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Tenant", "tenant-a")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update pipeline as tenant-a: %d %s", rec.Code, rec.Body)
	}

	// Tenant A sees the revision.
	listA, totalA, err := srv.store.PipelineRevisions.List(ctx,
		"tenant-a", pipeA.ID, 50, 0)
	if err != nil {
		t.Fatalf("list tenant-a: %v", err)
	}
	if totalA != 1 || len(listA) != 1 {
		t.Fatalf("tenant-a should see 1 revision, got %d", totalA)
	}
	if listA[0].TenantID != "tenant-a" {
		t.Errorf("revision tenant_id = %q, want tenant-a", listA[0].TenantID)
	}

	// Tenant B sees nothing for the same pipeline id.
	_, totalB, err := srv.store.PipelineRevisions.List(ctx,
		"tenant-b", pipeA.ID, 50, 0)
	if err != nil {
		t.Fatalf("list tenant-b: %v", err)
	}
	if totalB != 0 {
		t.Errorf("tenant-b should see 0 revisions for tenant-a's pipeline, got %d", totalB)
	}
}
