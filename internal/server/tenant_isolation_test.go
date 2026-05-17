package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// The cross-tenant isolation suite. These tests are the contract a
// penetration tester evaluates: a user authenticated to tenant A
// cannot read or mutate any resource owned by tenant B, regardless
// of role.
//
// Strategy:
//   - Two tenants, A and B, each with their own connection +
//     pipeline.
//   - The test server's auth setup installs a fixedTenantResolver
//     that swaps tenants based on a header (X-Test-Tenant), so a
//     single test can act as A then as B without re-logging-in.
//   - Every API surface is tried both directions: A cannot see B's
//     resources, B cannot see A's.

// tenantSwitcher implements auth.TenantResolver and lets the test
// pretend to be in a specific tenant by setting X-Test-Tenant.
type tenantSwitcher struct {
	defaultTenant string
	defaultRole   string
}

func (t tenantSwitcher) Resolve(r *http.Request, _ any) (auth.TenantClaim, bool) {
	tid := r.Header.Get("X-Test-Tenant")
	if tid == "" {
		tid = t.defaultTenant
	}
	role := r.Header.Get("X-Test-Role")
	if role == "" {
		role = t.defaultRole
	}
	return auth.TenantClaim{TenantID: tid, Role: role}, true
}

// setupIsolation creates two tenants with one connection + one
// pipeline each. Returns the handler, ids, and a logged-in cookie.
type isolationFixture struct {
	h       http.Handler
	srv     *Server
	cookie  *http.Cookie
	tenantA string
	tenantB string
	connA   string
	connB   string
	pipeA   string
	pipeB   string
}

func setupIsolation(t *testing.T) isolationFixture {
	t.Helper()
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	// Override the resolver — the test wants explicit per-request
	// control over which tenant alice is acting as.
	srv.auth.SetTenantResolver(tenantSwitcher{defaultTenant: "tenant-a", defaultRole: "owner"})

	ctx := context.Background()
	for _, tid := range []string{"tenant-a", "tenant-b"} {
		_ = srv.store.Tenants.Create(ctx, &storage.Tenant{
			ID: tid, Slug: tid, Name: tid, Status: "active",
		})
		// Alice owns both tenants. The isolation we're testing is
		// tenant-scoping, not authz — even an owner cannot read
		// cross-tenant when acting as a single tenant.
		_ = srv.store.Memberships.Upsert(ctx, &storage.Membership{
			TenantID: tid, UserSub: "alice", Username: "alice", Role: storage.RoleOwner,
		})
	}

	// Seed resources via the storage layer so we control tenant
	// placement exactly (the HTTP layer is what we're testing).
	connA := &storage.Connection{Name: "rabbit-a", Type: "rabbitmq", URL: "amqp://a"}
	connB := &storage.Connection{Name: "rabbit-b", Type: "rabbitmq", URL: "amqp://b"}
	if err := srv.store.Connections.Create(ctx, "tenant-a", connA); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.Connections.Create(ctx, "tenant-b", connB); err != nil {
		t.Fatal(err)
	}
	pipeA := &storage.Pipeline{Name: "pipe-a", SourceID: connA.ID, DestinationID: connA.ID, Enabled: false}
	pipeB := &storage.Pipeline{Name: "pipe-b", SourceID: connB.ID, DestinationID: connB.ID, Enabled: false}
	if err := srv.store.Pipelines.Create(ctx, "tenant-a", pipeA); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.Pipelines.Create(ctx, "tenant-b", pipeB); err != nil {
		t.Fatal(err)
	}

	return isolationFixture{
		h: h, srv: srv, cookie: cookie,
		tenantA: "tenant-a", tenantB: "tenant-b",
		connA: connA.ID, connB: connB.ID,
		pipeA: pipeA.ID, pipeB: pipeB.ID,
	}
}

func (f isolationFixture) do(t *testing.T, asTenant, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	var b *strings.Reader
	if body != "" {
		b = strings.NewReader(body)
	} else {
		b = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, b)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Test-Tenant", asTenant)
	attachSession(req, f.cookie)
	f.h.ServeHTTP(rec, req)
	return rec
}

// ─── connections ──────────────────────────────────────────────────

func TestIsolation_Connections_ListOnlyOwnTenant(t *testing.T) {
	f := setupIsolation(t)
	rec := f.do(t, "tenant-a", http.MethodGet, "/api/v1/connections", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), f.connB) {
		t.Errorf("tenant-a list leaked tenant-b connection id %s; body=%s", f.connB, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), f.connA) {
		t.Errorf("tenant-a list missing its own connection id %s; body=%s", f.connA, rec.Body)
	}
}

func TestIsolation_Connections_GetOtherTenantIs404(t *testing.T) {
	f := setupIsolation(t)
	rec := f.do(t, "tenant-a", http.MethodGet, "/api/v1/connections/"+f.connB, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for cross-tenant Get, got %d", rec.Code)
	}
}

func TestIsolation_Connections_UpdateOtherTenantIs404(t *testing.T) {
	f := setupIsolation(t)
	body := `{"name":"renamed-by-alice","type":"rabbitmq","url":"amqp://stolen"}`
	rec := f.do(t, "tenant-a", http.MethodPut, "/api/v1/connections/"+f.connB, body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for cross-tenant Update, got %d", rec.Code)
	}
	// Verify the row in B is untouched.
	rec = f.do(t, "tenant-b", http.MethodGet, "/api/v1/connections/"+f.connB, "")
	if !strings.Contains(rec.Body.String(), `"name":"rabbit-b"`) {
		t.Errorf("tenant-b connection name was mutated: %s", rec.Body)
	}
}

func TestIsolation_Connections_DeleteOtherTenantIs404(t *testing.T) {
	f := setupIsolation(t)
	rec := f.do(t, "tenant-a", http.MethodDelete, "/api/v1/connections/"+f.connB, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for cross-tenant Delete, got %d", rec.Code)
	}
	// Verify still there.
	rec = f.do(t, "tenant-b", http.MethodGet, "/api/v1/connections/"+f.connB, "")
	if rec.Code != http.StatusOK {
		t.Errorf("tenant-b connection was deleted by cross-tenant call")
	}
}

// ─── pipelines ────────────────────────────────────────────────────

func TestIsolation_Pipelines_ListOnlyOwnTenant(t *testing.T) {
	f := setupIsolation(t)
	rec := f.do(t, "tenant-a", http.MethodGet, "/api/v1/pipelines", "")
	if strings.Contains(rec.Body.String(), f.pipeB) {
		t.Errorf("tenant-a pipelines list leaked tenant-b pipeline %s; body=%s", f.pipeB, rec.Body)
	}
}

func TestIsolation_Pipelines_CannotReferenceOtherTenantConnection(t *testing.T) {
	f := setupIsolation(t)
	// Acting as tenant-a, try to create a pipeline that references
	// connB (from tenant-b). The handler must refuse.
	body := `{"name":"steal","source_id":"` + f.connB + `","destination_id":"` + f.connB + `","output_format":"same"}`
	rec := f.do(t, "tenant-a", http.MethodPost, "/api/v1/pipelines", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for cross-tenant connection reference, got %d (%s)", rec.Code, rec.Body)
	}
}

func TestIsolation_Pipelines_ChildStagesCannotEscape(t *testing.T) {
	f := setupIsolation(t)
	// Tenant-a tries to replace stages on tenant-b's pipeline.
	body := `[{"stage_order":1,"stage_type":"filter","enabled":true}]`
	rec := f.do(t, "tenant-a", http.MethodPut, "/api/v1/pipelines/"+f.pipeB+"/stages", body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 on cross-tenant stage replace, got %d", rec.Code)
	}
}

// ─── DLQ ──────────────────────────────────────────────────────────

func TestIsolation_DLQ_ListOnlyOwnTenant(t *testing.T) {
	f := setupIsolation(t)
	// Seed DLQ entries in both tenants.
	for _, tid := range []string{"tenant-a", "tenant-b"} {
		req := f.do(t, tid, http.MethodGet, "/api/v1/dlq", "")
		if req.Code != http.StatusOK {
			t.Fatalf("dlq probe as %s: %d", tid, req.Code)
		}
	}
	// Seed via storage to control tenant placement.
	ctx := context.Background()
	_ = f.srv.store.DLQ.Insert(ctx, "tenant-a", &storage.DLQEntry{OriginalMsg: []byte("a-msg"), ErrorReason: "a-err"})
	_ = f.srv.store.DLQ.Insert(ctx, "tenant-b", &storage.DLQEntry{OriginalMsg: []byte("b-msg"), ErrorReason: "b-err"})

	rec := f.do(t, "tenant-a", http.MethodGet, "/api/v1/dlq", "")
	var listed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	if strings.Contains(rec.Body.String(), "b-err") {
		t.Errorf("DLQ list leaked tenant-b entry: %s", rec.Body)
	}
}

// ─── audit ────────────────────────────────────────────────────────

func TestIsolation_Audit_ListOnlyOwnTenant(t *testing.T) {
	f := setupIsolation(t)
	// Seed audit rows directly via storage so this test doesn't race
	// the audit middleware's async goroutine.
	ctx := context.Background()
	_ = f.srv.store.Audit.Insert(ctx, &storage.AuditEntry{
		TenantID: "tenant-a", Actor: "alice", Action: "POST",
		Resource: "/api/v1/connections", Status: 201,
	})
	_ = f.srv.store.Audit.Insert(ctx, &storage.AuditEntry{
		TenantID: "tenant-b", Actor: "alice", Action: "POST",
		Resource: "/api/v1/connections", Status: 201,
	})

	rec := f.do(t, "tenant-a", http.MethodGet, "/api/v1/audit", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit list: %d", rec.Code)
	}
	var resp struct {
		Total int                    `json:"total"`
		Items []*storage.AuditEntry  `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("tenant-a should see exactly 1 audit row, got %d", resp.Total)
	}
	for _, e := range resp.Items {
		if e.TenantID != "tenant-a" {
			t.Errorf("leaked audit row from tenant %s", e.TenantID)
		}
	}
}

