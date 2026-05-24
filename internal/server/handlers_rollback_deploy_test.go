package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mqConnector/internal/storage"
)

// Test suite for POST /api/v1/pipelines/{id}/revisions/{rev}/rollback
// (Task 5) and POST /api/v1/pipelines/{id}/deploy (Task 6). Mirrors
// the seeding conventions from handlers_pipeline_revisions_test.go
// (storage-layer revision insertion, no legacy PUT path) so each
// case has tight control over revision numbers and deployment state.

// rollbackDeployFixture wires up a logged-in server, a pipeline, and
// two connections — enough surface for both endpoints. Revisions are
// seeded per-test via seedRevisionWithSnapshot so each case can pin
// the live-table state it wants applyRevisionLive to write through.
type rollbackDeployFixture struct {
	h      http.Handler
	srv    *Server
	cookie *http.Cookie
	src    storage.Connection
	dst    storage.Connection
	pipe   storage.Pipeline
}

func setupRollbackDeployFixture(t *testing.T) rollbackDeployFixture {
	t.Helper()
	h, srv, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)
	src := postConn(t, h, cookie, `{"name":"rbd-src","type":"rabbitmq","url":"amqp://x","queue_name":"q1"}`)
	dst := postConn(t, h, cookie, `{"name":"rbd-dst","type":"rabbitmq","url":"amqp://x","queue_name":"q2"}`)

	// Create directly via storage so no snapshot fires on creation —
	// every test starts with zero revisions and a clean live pipeline.
	pipe := storage.Pipeline{
		Name:          "rbd-pipe",
		SourceID:      src.ID,
		DestinationID: dst.ID,
		OutputFormat:  "same",
		Enabled:       true,
	}
	if err := srv.store.Pipelines.Create(context.Background(),
		storage.DefaultTenantID, &pipe); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	return rollbackDeployFixture{
		h: h, srv: srv, cookie: cookie,
		src: src, dst: dst, pipe: pipe,
	}
}

// seedRevisionWithSnapshot inserts a revision whose embedded
// PipelineSnapshot mirrors the live row's id/source/destination plus
// the caller-supplied name and stages. Returns the inserted revision
// so the test can read back its revision_number.
func (f rollbackDeployFixture) seedRevisionWithSnapshot(t *testing.T, name string, stages []*storage.Stage, summary string) *storage.PipelineRevision {
	t.Helper()
	snapPipe := storage.Pipeline{
		ID:            f.pipe.ID,
		Name:          name,
		SourceID:      f.src.ID,
		DestinationID: f.dst.ID,
		OutputFormat:  "same",
		Enabled:       true,
	}
	snap := storage.PipelineSnapshot{
		Pipeline:      &snapPipe,
		Stages:        stages,
		SchemaVersion: 1,
	}
	bytes, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	rev := &storage.PipelineRevision{
		PipelineID: f.pipe.ID,
		Snapshot:   string(bytes),
		// Hash needs to be unique per revision so the repo's
		// hash-dedup doesn't collapse separate test revisions; tag
		// with the name so each call produces a fresh hash.
		SnapshotHash:   fmt.Sprintf("hash-%s-%d", name, len(stages)),
		AuthorSub:      "alice",
		AuthorUsername: "alice",
		ChangeSummary:  summary,
	}
	if err := f.srv.store.PipelineRevisions.Create(context.Background(),
		storage.DefaultTenantID, rev); err != nil {
		t.Fatalf("seed revision %q: %v", name, err)
	}
	return rev
}

// post issues a POST with the fixture's auth + CSRF and an optional
// JSON body. nil body sends nothing (Content-Length=0).
func (f rollbackDeployFixture) post(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	var req *http.Request
	if rdr != nil {
		req = httptest.NewRequest(http.MethodPost, path, rdr)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(http.MethodPost, path, nil)
	}
	attachSession(req, f.cookie)
	f.h.ServeHTTP(rec, req)
	return rec
}

// decodeApply parses an applyResponse body. Tests use this for both
// /rollback and /deploy since the response envelope is identical.
func decodeApply(t *testing.T, b []byte) applyResponse {
	t.Helper()
	var resp applyResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("decode apply response: %v (body=%s)", err, b)
	}
	return resp
}

// stageA / stageB are the two canonical seed stage slices used by the
// rollback tests. Distinct StageType / StageOrder lets the live-tables
// assertion confirm which revision's content actually landed.
func stageA() []*storage.Stage {
	return []*storage.Stage{
		{StageOrder: 1, StageType: "filter", StageConfig: `{}`, Enabled: true},
	}
}

func stageB() []*storage.Stage {
	return []*storage.Stage{
		{StageOrder: 1, StageType: "transform", StageConfig: `{}`, Enabled: true},
		{StageOrder: 2, StageType: "script", StageConfig: `{}`, Enabled: true},
	}
}

// ─── Rollback (Task 5) ──────────────────────────────────────────────

// TestRollback_HappyPath seeds rev 1 (stages A) and rev 2 (stages B)
// both deployed; rollback to rev 1 → new rev 3 holding stages A and
// marked deployed; live `stages` table now matches A; response
// envelope carries the new revision + decoded snapshot.
func TestRollback_HappyPath(t *testing.T) {
	f := setupRollbackDeployFixture(t)
	ctx := context.Background()

	rev1 := f.seedRevisionWithSnapshot(t, "pipe-v1", stageA(), "initial")
	if err := f.srv.store.PipelineRevisions.MarkDeployed(ctx,
		storage.DefaultTenantID, f.pipe.ID, rev1.RevisionNumber, "req-1"); err != nil {
		t.Fatalf("mark rev1 deployed: %v", err)
	}
	rev2 := f.seedRevisionWithSnapshot(t, "pipe-v2", stageB(), "upgrade")
	if err := f.srv.store.PipelineRevisions.MarkDeployed(ctx,
		storage.DefaultTenantID, f.pipe.ID, rev2.RevisionNumber, "req-2"); err != nil {
		t.Fatalf("mark rev2 deployed: %v", err)
	}

	rec := f.post(t,
		fmt.Sprintf("/api/v1/pipelines/%s/revisions/%d/rollback",
			f.pipe.ID, rev1.RevisionNumber),
		"")
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body)
	}
	resp := decodeApply(t, rec.Body.Bytes())

	if resp.Revision == nil {
		t.Fatal("response missing revision")
	}
	if resp.Revision.RevisionNumber != 3 {
		t.Errorf("new rev number = %d, want 3 (MAX(1,2)+1)", resp.Revision.RevisionNumber)
	}
	if resp.Revision.DeployedAt == nil {
		t.Error("new revision deployed_at should be non-NULL")
	}
	if resp.Revision.ChangeSummary != "Rollback to revision 1" {
		t.Errorf("default change_summary = %q", resp.Revision.ChangeSummary)
	}
	if resp.Snapshot == nil {
		t.Fatal("response missing snapshot")
	}
	if resp.Snapshot.Pipeline == nil || resp.Snapshot.Pipeline.Name != "pipe-v1" {
		t.Errorf("snapshot pipeline name = %v, want pipe-v1", resp.Snapshot.Pipeline)
	}

	// Live tables must reflect the rolled-back state: pipeline name
	// is pipe-v1 and stages are stages A (one filter).
	livePipe, err := f.srv.store.Pipelines.Get(ctx, storage.DefaultTenantID, f.pipe.ID)
	if err != nil {
		t.Fatalf("get live pipeline: %v", err)
	}
	if livePipe.Name != "pipe-v1" {
		t.Errorf("live pipeline name = %q, want pipe-v1 (write-through must reach live table)", livePipe.Name)
	}
	liveStages, err := f.srv.store.Stages.ListByPipeline(ctx, storage.DefaultTenantID, f.pipe.ID)
	if err != nil {
		t.Fatalf("list live stages: %v", err)
	}
	if len(liveStages) != 1 {
		t.Fatalf("live stages count = %d, want 1 (rev 1's stage)", len(liveStages))
	}
	if liveStages[0].StageType != "filter" {
		t.Errorf("live stage type = %q, want filter (rev 1)", liveStages[0].StageType)
	}
}

// TestRollback_ReloadTriggered exercises the post-apply
// reloadPipelines() kick. There is no direct counting hook, so we
// confirm the proxy assertion the spec allows: the live tables
// reflect what reload would pick up. The hot-reload runs in a
// goroutine; reading the live tables after the response is the
// race-safe surrogate (Reload's job is just to pick up that state).
func TestRollback_ReloadTriggered(t *testing.T) {
	f := setupRollbackDeployFixture(t)
	rev1 := f.seedRevisionWithSnapshot(t, "pipe-v1", stageA(), "")
	_ = f.seedRevisionWithSnapshot(t, "pipe-v2", stageB(), "")

	rec := f.post(t,
		fmt.Sprintf("/api/v1/pipelines/%s/revisions/%d/rollback",
			f.pipe.ID, rev1.RevisionNumber),
		"")
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body)
	}
	// The reload kick is non-blocking — we don't try to assert it
	// observably fired (no test hook). What we CAN assert: the live
	// tables now match the rolled-back snapshot, which is the data
	// the next Reload would pick up.
	ctx := context.Background()
	livePipe, err := f.srv.store.Pipelines.Get(ctx, storage.DefaultTenantID, f.pipe.ID)
	if err != nil {
		t.Fatalf("get live pipeline: %v", err)
	}
	if livePipe.Name != "pipe-v1" {
		t.Errorf("live name = %q, want pipe-v1", livePipe.Name)
	}
}

// TestRollback_CrossTenant — tenant B cannot rollback tenant A's
// pipeline; 404 (never 403) per the cross-tenant convention.
func TestRollback_CrossTenant(t *testing.T) {
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
	snap := storage.PipelineSnapshot{
		Pipeline:      &storage.Pipeline{ID: pipeA.ID, Name: "pa-v1"},
		SchemaVersion: 1,
	}
	bytes, _ := json.Marshal(snap)
	rev := &storage.PipelineRevision{
		PipelineID:     pipeA.ID,
		Snapshot:       string(bytes),
		SnapshotHash:   "h1",
		AuthorSub:      "alice",
		AuthorUsername: "alice",
	}
	if err := srv.store.PipelineRevisions.Create(ctx, "tenant-a", rev); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	path := fmt.Sprintf("/api/v1/pipelines/%s/revisions/%d/rollback",
		pipeA.ID, rev.RevisionNumber)
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("X-Test-Tenant", "tenant-b")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant rollback: %d, want 404 (never 403)", rec.Code)
	}
}

// TestRollback_UnknownRevision — a positive integer that doesn't map
// to any revision is 404, not 400.
func TestRollback_UnknownRevision(t *testing.T) {
	f := setupRollbackDeployFixture(t)
	f.seedRevisionWithSnapshot(t, "pipe-v1", stageA(), "")
	rec := f.post(t,
		fmt.Sprintf("/api/v1/pipelines/%s/revisions/9999/rollback", f.pipe.ID),
		"")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown rev: %d, want 404 (body=%s)", rec.Code, rec.Body)
	}
}

// TestRollback_BadRevisionNumber — non-numeric, zero, and negative
// values must surface as 400 before any DB work.
func TestRollback_BadRevisionNumber(t *testing.T) {
	f := setupRollbackDeployFixture(t)
	f.seedRevisionWithSnapshot(t, "pipe-v1", stageA(), "")
	for _, bad := range []string{"abc", "-1", "0"} {
		t.Run("rev="+bad, func(t *testing.T) {
			rec := f.post(t,
				fmt.Sprintf("/api/v1/pipelines/%s/revisions/%s/rollback", f.pipe.ID, bad),
				"")
			if rec.Code != http.StatusBadRequest {
				t.Errorf("rev=%s: %d, want 400", bad, rec.Code)
			}
		})
	}
}

// TestRollback_CustomChangeSummary — explicit body.change_summary
// must override the default "Rollback to revision N".
func TestRollback_CustomChangeSummary(t *testing.T) {
	f := setupRollbackDeployFixture(t)
	rev1 := f.seedRevisionWithSnapshot(t, "pipe-v1", stageA(), "")

	rec := f.post(t,
		fmt.Sprintf("/api/v1/pipelines/%s/revisions/%d/rollback",
			f.pipe.ID, rev1.RevisionNumber),
		`{"change_summary":"Emergency rollback - prod incident"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body)
	}
	resp := decodeApply(t, rec.Body.Bytes())
	if resp.Revision.ChangeSummary != "Emergency rollback - prod incident" {
		t.Errorf("change_summary = %q, want override", resp.Revision.ChangeSummary)
	}
}

// TestRollback_ViewerForbidden — the route-level RequireSession is
// the only gate against unauthenticated callers; gatePipeline adds
// the operator-or-better check. A viewer (via the tenant switcher's
// X-Test-Role header) must be rejected 403.
func TestRollback_ViewerForbidden(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	srv.auth.SetTenantResolver(tenantSwitcher{
		defaultTenant: storage.DefaultTenantID, defaultRole: "viewer"})

	ctx := context.Background()
	// Membership keeps the default-tenant resolver happy if it ever
	// short-circuits; the X-Test-Role header is what actually drives
	// the role for this test.
	_ = srv.store.Memberships.Upsert(ctx, &storage.Membership{
		TenantID: storage.DefaultTenantID, UserSub: "alice",
		Username: "alice", Role: storage.RoleViewer})

	conn := &storage.Connection{Name: "vsrc", Type: "rabbitmq", URL: "amqp://x"}
	if err := srv.store.Connections.Create(ctx, storage.DefaultTenantID, conn); err != nil {
		t.Fatal(err)
	}
	pipe := &storage.Pipeline{Name: "vpipe", SourceID: conn.ID,
		DestinationID: conn.ID, OutputFormat: "same", Enabled: false}
	if err := srv.store.Pipelines.Create(ctx, storage.DefaultTenantID, pipe); err != nil {
		t.Fatal(err)
	}
	snap := storage.PipelineSnapshot{
		Pipeline:      &storage.Pipeline{ID: pipe.ID, Name: "vpipe-v1"},
		SchemaVersion: 1,
	}
	bytes, _ := json.Marshal(snap)
	rev := &storage.PipelineRevision{
		PipelineID: pipe.ID, Snapshot: string(bytes),
		SnapshotHash:   "vh1",
		AuthorSub:      "alice",
		AuthorUsername: "alice",
	}
	if err := srv.store.PipelineRevisions.Create(ctx, storage.DefaultTenantID, rev); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	path := fmt.Sprintf("/api/v1/pipelines/%s/revisions/%d/rollback",
		pipe.ID, rev.RevisionNumber)
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("X-Test-Role", "viewer")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer rollback: %d, want 403", rec.Code)
	}
}

// TestRollback_WriteThroughFailureDoesNotMutate exercises the
// transactional contract: if the write-through fails mid-apply, the
// live tables must be untouched. We force the failure by dropping
// the stages table after the live state is established — the
// Pipeline UPDATE inside the tx would succeed but the stages
// ReplaceForPipelineTx would error, triggering rollback. The live
// pipeline name must therefore stay at its pre-rollback value.
func TestRollback_WriteThroughFailureDoesNotMutate(t *testing.T) {
	f := setupRollbackDeployFixture(t)
	ctx := context.Background()
	rev1 := f.seedRevisionWithSnapshot(t, "pipe-v1", stageA(), "")

	// Drop the stages table so any INSERT inside applyRevisionLive
	// errors after the pipeline UPDATE has already happened in the
	// same tx. SQLite rolls the whole tx back, so the pipeline name
	// must not change.
	if _, err := f.srv.store.DB.ExecContext(ctx, `DROP TABLE stages`); err != nil {
		t.Fatalf("drop stages: %v", err)
	}

	rec := f.post(t,
		fmt.Sprintf("/api/v1/pipelines/%s/revisions/%d/rollback",
			f.pipe.ID, rev1.RevisionNumber),
		"")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("rollback with broken stages table: %d, want 500 (body=%s)", rec.Code, rec.Body)
	}

	// Live pipeline name should still be "rbd-pipe" (the original
	// fixture name), NOT "pipe-v1" (the rolled-back snapshot's name).
	livePipe, err := f.srv.store.Pipelines.Get(ctx, storage.DefaultTenantID, f.pipe.ID)
	if err != nil {
		t.Fatalf("get live pipeline: %v", err)
	}
	if livePipe.Name != "rbd-pipe" {
		t.Errorf("live name = %q, want unchanged %q (failed tx must roll back)",
			livePipe.Name, "rbd-pipe")
	}
}

// ─── Deploy (Task 6) ────────────────────────────────────────────────

// TestDeploy_HappyPath — rev 1 deployed, rev 2 undeployed; POST
// /deploy with revision_number=2 → rev 2 marked deployed, live
// tables match rev 2's snapshot, NO new revision created (deploy
// promotes; it doesn't fork).
func TestDeploy_HappyPath(t *testing.T) {
	f := setupRollbackDeployFixture(t)
	ctx := context.Background()

	rev1 := f.seedRevisionWithSnapshot(t, "pipe-v1", stageA(), "")
	if err := f.srv.store.PipelineRevisions.MarkDeployed(ctx,
		storage.DefaultTenantID, f.pipe.ID, rev1.RevisionNumber, "req-1"); err != nil {
		t.Fatalf("mark rev1 deployed: %v", err)
	}
	rev2 := f.seedRevisionWithSnapshot(t, "pipe-v2", stageB(), "")
	if rev2.DeployedAt != nil {
		t.Fatalf("seeded rev2 should NOT be deployed before /deploy fires")
	}

	body := fmt.Sprintf(`{"revision_number":%d}`, rev2.RevisionNumber)
	rec := f.post(t, "/api/v1/pipelines/"+f.pipe.ID+"/deploy", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body)
	}
	resp := decodeApply(t, rec.Body.Bytes())
	if resp.Revision == nil {
		t.Fatal("response missing revision")
	}
	if resp.Revision.RevisionNumber != rev2.RevisionNumber {
		t.Errorf("response rev = %d, want %d (deploy must return the deployed rev, not a new one)",
			resp.Revision.RevisionNumber, rev2.RevisionNumber)
	}
	if resp.Revision.DeployedAt == nil {
		t.Error("deployed_at should be non-NULL after deploy")
	}

	// Live state matches rev 2's snapshot.
	livePipe, err := f.srv.store.Pipelines.Get(ctx, storage.DefaultTenantID, f.pipe.ID)
	if err != nil {
		t.Fatalf("get live pipeline: %v", err)
	}
	if livePipe.Name != "pipe-v2" {
		t.Errorf("live name = %q, want pipe-v2", livePipe.Name)
	}
	liveStages, err := f.srv.store.Stages.ListByPipeline(ctx, storage.DefaultTenantID, f.pipe.ID)
	if err != nil {
		t.Fatalf("list stages: %v", err)
	}
	if len(liveStages) != 2 {
		t.Errorf("live stages count = %d, want 2 (rev 2 has two stages)", len(liveStages))
	}
}

// TestDeploy_ApproverGate — when pipelines.requires_approval=true,
// POST without `approver` is 409; with approver=alice it succeeds.
func TestDeploy_ApproverGate(t *testing.T) {
	f := setupRollbackDeployFixture(t)
	ctx := context.Background()
	rev1 := f.seedRevisionWithSnapshot(t, "pipe-v1", stageA(), "")

	// Flip requires_approval=true directly via the live table — no
	// Wave-1 API surfaces this column.
	if _, err := f.srv.store.DB.ExecContext(ctx,
		`UPDATE pipelines SET requires_approval = 1 WHERE id = ?`,
		f.pipe.ID); err != nil {
		t.Fatalf("flip requires_approval: %v", err)
	}

	// Without approver → 409.
	body := fmt.Sprintf(`{"revision_number":%d}`, rev1.RevisionNumber)
	rec := f.post(t, "/api/v1/pipelines/"+f.pipe.ID+"/deploy", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("deploy without approver: %d, want 409 (body=%s)", rec.Code, rec.Body)
	}
	var errBody map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
	if errBody["error"] == "" {
		t.Errorf("expected error envelope, got %s", rec.Body)
	}

	// With approver → 200.
	body = fmt.Sprintf(`{"revision_number":%d,"approver":"alice"}`, rev1.RevisionNumber)
	rec = f.post(t, "/api/v1/pipelines/"+f.pipe.ID+"/deploy", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy with approver: %d, want 200 (body=%s)", rec.Code, rec.Body)
	}
}

// TestDeploy_UnknownRevision — revision_number not found → 404.
func TestDeploy_UnknownRevision(t *testing.T) {
	f := setupRollbackDeployFixture(t)
	body := `{"revision_number":9999}`
	rec := f.post(t, "/api/v1/pipelines/"+f.pipe.ID+"/deploy", body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("deploy unknown rev: %d, want 404", rec.Code)
	}
}

// TestDeploy_BadBody — missing revision_number / non-positive value
// surfaces as 400 before any DB work.
func TestDeploy_BadBody(t *testing.T) {
	f := setupRollbackDeployFixture(t)
	cases := []struct {
		name string
		body string
	}{
		{"empty_body", `{}`},
		{"zero_rev", `{"revision_number":0}`},
		{"negative_rev", `{"revision_number":-1}`},
		{"not_json", `not-json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.post(t, "/api/v1/pipelines/"+f.pipe.ID+"/deploy", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: %d, want 400 (body=%s)", tc.name, rec.Code, rec.Body)
			}
		})
	}
}

// TestDeploy_CrossTenant — tenant B cannot deploy tenant A's
// pipeline; 404 (never 403) per cross-tenant convention.
func TestDeploy_CrossTenant(t *testing.T) {
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
	snap := storage.PipelineSnapshot{
		Pipeline:      &storage.Pipeline{ID: pipeA.ID, Name: "pa-v1"},
		SchemaVersion: 1,
	}
	bytes, _ := json.Marshal(snap)
	rev := &storage.PipelineRevision{
		PipelineID: pipeA.ID, Snapshot: string(bytes),
		SnapshotHash:   "h1",
		AuthorSub:      "alice",
		AuthorUsername: "alice",
	}
	if err := srv.store.PipelineRevisions.Create(ctx, "tenant-a", rev); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	body := fmt.Sprintf(`{"revision_number":%d}`, rev.RevisionNumber)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/pipelines/"+pipeA.ID+"/deploy",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Tenant", "tenant-b")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant deploy: %d, want 404 (never 403)", rec.Code)
	}
}

// TestDeploy_NoNewRevisionCreated — count revision rows before/after
// deploy; assert they're equal. Deploy promotes the existing row;
// it must not fork the history like /rollback does.
func TestDeploy_NoNewRevisionCreated(t *testing.T) {
	f := setupRollbackDeployFixture(t)
	ctx := context.Background()
	_ = f.seedRevisionWithSnapshot(t, "pipe-v1", stageA(), "")
	rev2 := f.seedRevisionWithSnapshot(t, "pipe-v2", stageB(), "")

	_, totalBefore, err := f.srv.store.PipelineRevisions.List(ctx,
		storage.DefaultTenantID, f.pipe.ID, 100, 0)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}

	body := fmt.Sprintf(`{"revision_number":%d}`, rev2.RevisionNumber)
	rec := f.post(t, "/api/v1/pipelines/"+f.pipe.ID+"/deploy", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body)
	}

	_, totalAfter, err := f.srv.store.PipelineRevisions.List(ctx,
		storage.DefaultTenantID, f.pipe.ID, 100, 0)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if totalAfter != totalBefore {
		t.Errorf("revision count went from %d to %d; deploy must not create a new row",
			totalBefore, totalAfter)
	}
}
