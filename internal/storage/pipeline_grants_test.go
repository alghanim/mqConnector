package storage

import (
	"context"
	"errors"
	"testing"
)

func TestPipelineGrants_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	pid := seedPipelineForGrants(t, s, "pg-test")

	ctx := context.Background()
	if err := s.PipelineGrants.Set(ctx, pid, "user-1", RoleOperator); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.PipelineGrants.Get(ctx, pid, "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.Role != RoleOperator {
		t.Fatalf("Get role = %v, want operator", got)
	}
	// Upsert lifts role to admin.
	if err := s.PipelineGrants.Set(ctx, pid, "user-1", RoleAdmin); err != nil {
		t.Fatalf("Set (upgrade): %v", err)
	}
	got, _ = s.PipelineGrants.Get(ctx, pid, "user-1")
	if got.Role != RoleAdmin {
		t.Errorf("after upsert role = %q, want admin", got.Role)
	}
	// Delete clears.
	if err := s.PipelineGrants.Delete(ctx, pid, "user-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ = s.PipelineGrants.Get(ctx, pid, "user-1")
	if got != nil {
		t.Error("Get after Delete should return nil")
	}
	// Second delete returns ErrNotFound.
	err = s.PipelineGrants.Delete(ctx, pid, "user-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete err = %v, want ErrNotFound", err)
	}
}

func TestPipelineGrants_EffectiveRole_GrantEscalates(t *testing.T) {
	s := openTestStore(t)
	pid := seedPipelineForGrants(t, s, "pg-effective")
	ctx := context.Background()

	// Viewer on tenant with no grant → viewer on pipeline.
	role, err := s.PipelineGrants.EffectiveRole(ctx, pid, "u", RoleViewer)
	if err != nil || role != RoleViewer {
		t.Fatalf("no-grant viewer = %v / err %v, want viewer", role, err)
	}
	// Add an admin grant.
	if err := s.PipelineGrants.Set(ctx, pid, "u", RoleAdmin); err != nil {
		t.Fatalf("Set: %v", err)
	}
	role, _ = s.PipelineGrants.EffectiveRole(ctx, pid, "u", RoleViewer)
	if role != RoleAdmin {
		t.Errorf("viewer + admin grant = %q, want admin", role)
	}
}

func TestPipelineGrants_EffectiveRole_GrantDoesNotDemote(t *testing.T) {
	s := openTestStore(t)
	pid := seedPipelineForGrants(t, s, "pg-no-demote")
	ctx := context.Background()
	// Admin on tenant + viewer grant → still admin (grant can't demote).
	if err := s.PipelineGrants.Set(ctx, pid, "u", RoleViewer); err != nil {
		t.Fatalf("Set: %v", err)
	}
	role, _ := s.PipelineGrants.EffectiveRole(ctx, pid, "u", RoleAdmin)
	if role != RoleAdmin {
		t.Errorf("admin + viewer grant = %q, want admin (no demotion)", role)
	}
}

func TestPipelineGrants_ListPipelinesForUser(t *testing.T) {
	s := openTestStore(t)
	a := seedPipelineForGrants(t, s, "pg-list-a")
	b := seedPipelineForGrants(t, s, "pg-list-b")
	_ = seedPipelineForGrants(t, s, "pg-list-c") // user has no grant on c
	ctx := context.Background()
	_ = s.PipelineGrants.Set(ctx, a, "u", RoleOperator)
	_ = s.PipelineGrants.Set(ctx, b, "u", RoleAdmin)
	ids, err := s.PipelineGrants.ListPipelinesForUser(ctx, "u")
	if err != nil {
		t.Fatalf("ListPipelinesForUser: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d ids, want 2: %v", len(ids), ids)
	}
}

// seedPipelineForGrants creates a minimal pipeline row backed by two
// throwaway connections so the FK from pipeline_grants.pipeline_id
// resolves. Returns the pipeline ID.
func seedPipelineForGrants(t *testing.T, s *Store, name string) string {
	t.Helper()
	ctx := context.Background()
	src := &Connection{Name: name + "-src", Type: "rabbitmq", URL: "amqp://x", QueueName: "q"}
	if err := s.Connections.Create(ctx, DefaultTenantID, src); err != nil {
		t.Fatalf("Create src: %v", err)
	}
	dst := &Connection{Name: name + "-dst", Type: "rabbitmq", URL: "amqp://y", QueueName: "q"}
	if err := s.Connections.Create(ctx, DefaultTenantID, dst); err != nil {
		t.Fatalf("Create dst: %v", err)
	}
	p := &Pipeline{
		Name:          name,
		SourceID:      src.ID,
		DestinationID: dst.ID,
		Enabled:       true,
	}
	if err := s.Pipelines.Create(ctx, DefaultTenantID, p); err != nil {
		t.Fatalf("Create pipeline: %v", err)
	}
	return p.ID
}
