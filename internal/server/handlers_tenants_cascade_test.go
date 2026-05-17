package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mqConnector/internal/storage"
)

// TestDeleteTenant_CascadeWipesEverything — DELETE ?cascade=true must
// purge every per-tenant resource in one transaction. The HTTP layer
// adds tenant-owner authorization on top of the storage-layer Purge
// test; this one drives the full request path.
func TestDeleteTenant_CascadeWipesEverything(t *testing.T) {
	h, srv, _ := newTestServer(t)
	ctx := context.Background()

	// Create a non-default tenant and make alice an owner of it.
	const tid = "22222222-2222-2222-2222-222222222222"
	if err := srv.store.Tenants.Create(ctx, &storage.Tenant{
		ID: tid, Slug: "victim", Name: "Victim",
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.Memberships.Upsert(ctx, &storage.Membership{
		TenantID: tid, UserSub: "alice", Username: "alice", Role: storage.RoleOwner,
	}); err != nil {
		t.Fatal(err)
	}
	// Seed a connection + pipeline in the victim tenant.
	conn := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x", QueueName: "q"}
	if err := srv.store.Connections.Create(ctx, tid, conn); err != nil {
		t.Fatal(err)
	}
	pipe := &storage.Pipeline{Name: "p", SourceID: conn.ID, DestinationID: conn.ID}
	if err := srv.store.Pipelines.Create(ctx, tid, pipe); err != nil {
		t.Fatal(err)
	}

	cookie := loginCookie(t, h, "alice", "wonderland")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/tenants/"+tid+"?cascade=true", nil)
	attachSession(req, cookie)
	// The HTTP handler resolves tenancy via the mqc_active_tenant
	// cookie; set it to the victim so isOwnerOf finds the membership.
	req.AddCookie(&http.Cookie{Name: "mqc_active_tenant", Value: tid})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"mode":"cascade"`) {
		t.Errorf("expected mode=cascade in response, got %s", rec.Body.String())
	}

	// Verify the cascade actually ran via the storage layer.
	if _, err := srv.store.Tenants.Get(ctx, tid); err != storage.ErrNotFound {
		t.Errorf("expected tenant gone, got %v", err)
	}
	rows, _ := srv.store.Connections.List(ctx, tid)
	if len(rows) != 0 {
		t.Errorf("expected connections wiped, got %d", len(rows))
	}
}

// TestDeleteTenant_DefaultRefused — DELETE /tenants/<default>
// regardless of cascade must fail. The default tenant is the
// legacy-row backfill target; deleting it would orphan every
// pre-multi-tenant row in the database.
func TestDeleteTenant_DefaultRefused(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/tenants/"+storage.DefaultTenantID+"?cascade=true", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for default tenant, got %d", rec.Code)
	}
}

// TestDeleteTenant_NonOwnerForbidden — only owners of the target
// tenant can purge it. A regular member (operator role) gets 403.
func TestDeleteTenant_NonOwnerForbidden(t *testing.T) {
	h, srv, _ := newTestServer(t)
	ctx := context.Background()
	const tid = "33333333-3333-3333-3333-333333333333"
	if err := srv.store.Tenants.Create(ctx, &storage.Tenant{
		ID: tid, Slug: "shielded", Name: "Shielded",
	}); err != nil {
		t.Fatal(err)
	}
	// Alice is only an operator here, not an owner.
	if err := srv.store.Memberships.Upsert(ctx, &storage.Membership{
		TenantID: tid, UserSub: "alice", Username: "alice", Role: storage.RoleOperator,
	}); err != nil {
		t.Fatal(err)
	}

	cookie := loginCookie(t, h, "alice", "wonderland")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/tenants/"+tid+"?cascade=true", nil)
	attachSession(req, cookie)
	req.AddCookie(&http.Cookie{Name: "mqc_active_tenant", Value: tid})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-owner, got %d", rec.Code)
	}
}
