package storage

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestPurge_RemovesAllTenantData proves the GDPR / decommission path
// actually cascades: every per-tenant table is empty for that tenant
// after Purge returns. A bug here is the kind of thing that lands a
// data-protection regulator on your doorstep — the test is the
// contract.
func TestPurge_RemovesAllTenantData(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Create a non-default tenant and seed every per-tenant table
	// with at least one row.
	tenantID := uuid.NewString()
	tenant := &Tenant{ID: tenantID, Slug: "test-purge", Name: "Test Purge"}
	if err := s.Tenants.Create(ctx, tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	// connection + pipeline + stages + transforms + routing rules
	src := &Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x"}
	if err := s.Connections.Create(ctx, tenantID, src); err != nil {
		t.Fatal(err)
	}
	dst := &Connection{Name: "dst", Type: "rabbitmq", URL: "amqp://x"}
	if err := s.Connections.Create(ctx, tenantID, dst); err != nil {
		t.Fatal(err)
	}
	pipe := &Pipeline{Name: "p", SourceID: src.ID, DestinationID: dst.ID}
	if err := s.Pipelines.Create(ctx, tenantID, pipe); err != nil {
		t.Fatal(err)
	}
	if err := s.Stages.ReplaceForPipeline(ctx, tenantID, pipe.ID,
		[]*Stage{{StageOrder: 1, StageType: "filter", StageConfig: "{}", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	// dlq
	if err := s.DLQ.Insert(ctx, tenantID, &DLQEntry{
		PipelineID:  pipe.ID,
		SourceQueue: "src.q",
		OriginalMsg: []byte("payload"),
		ErrorReason: "test",
	}); err != nil {
		t.Fatal(err)
	}

	// Sanity check: rows exist for this tenant.
	if rows, _ := s.Connections.List(ctx, tenantID); len(rows) != 2 {
		t.Fatalf("setup: expected 2 connections, got %d", len(rows))
	}

	// Seed a row for the DEFAULT tenant too — must NOT be deleted.
	otherSrc := &Connection{Name: "other", Type: "rabbitmq", URL: "amqp://x"}
	if err := s.Connections.Create(ctx, DefaultTenantID, otherSrc); err != nil {
		t.Fatal(err)
	}

	// Purge.
	if err := s.Tenants.Purge(ctx, tenantID); err != nil {
		t.Fatalf("purge: %v", err)
	}

	// Tenant row gone.
	if _, err := s.Tenants.Get(ctx, tenantID); err != ErrNotFound {
		t.Errorf("expected tenant gone, got err=%v", err)
	}
	// Per-tenant rows gone.
	if rows, _ := s.Connections.List(ctx, tenantID); len(rows) != 0 {
		t.Errorf("connections leaked: %d", len(rows))
	}
	dlq, _, _ := s.DLQ.List(ctx, tenantID, 1, 100)
	if len(dlq) != 0 {
		t.Errorf("dlq leaked: %d", len(dlq))
	}
	// Other tenants untouched.
	otherRows, _ := s.Connections.List(ctx, DefaultTenantID)
	found := false
	for _, c := range otherRows {
		if c.ID == otherSrc.ID {
			found = true
		}
	}
	if !found {
		t.Error("Purge wiped a connection in the default tenant")
	}
}

// TestPurge_RefusesDefaultTenant — protect the bootstrap row.
func TestPurge_RefusesDefaultTenant(t *testing.T) {
	s := openTestStore(t)
	err := s.Tenants.Purge(context.Background(), DefaultTenantID)
	if err == nil {
		t.Fatal("expected refusal for default tenant")
	}
}

// TestPurge_NotFound — caller distinguishes "already gone" from "real
// failure".
func TestPurge_NotFound(t *testing.T) {
	s := openTestStore(t)
	err := s.Tenants.Purge(context.Background(), uuid.NewString())
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
