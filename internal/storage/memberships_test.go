package storage

import (
	"context"
	"testing"
)

// TestListByUser_AdoptsBootstrapRow covers the original adoption path:
// a row keyed by "bootstrap:<username>" is rewritten to the real sub on
// the first login. The next ListByUser sees the row directly under the
// real sub.
func TestListByUser_AdoptsBootstrapRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// Seed the bootstrap row the way the SimpleAuth bootstrap script
	// would in a fresh deployment.
	if err := s.Memberships.Upsert(ctx, &Membership{
		TenantID: DefaultTenantID,
		UserSub:  "bootstrap:admin",
		Username: "admin",
		Role:     RoleOwner,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// First login with the real SimpleAuth sub.
	got, err := s.Memberships.ListByUser(ctx, "real-sub-1", "admin")
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(got) != 1 || got[0].UserSub != "real-sub-1" {
		t.Fatalf("expected adoption to real-sub-1, got %+v", got)
	}
}

// TestListByUser_AdoptsStaleAdminSubOnDefaultTenant covers the
// SimpleAuth-restart case: an existing row sits at an OLD real sub for
// the bootstrap admin (owner of the default tenant). When the user
// logs back in with a NEW sub, that row gets adopted.
//
// This is the bug the local-dev workflow hit: SimpleAuth issues a
// fresh sub on every container rebuild, stranding the admin's tenants.
func TestListByUser_AdoptsStaleAdminSubOnDefaultTenant(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// Pretend an earlier session adopted the bootstrap row to "old-sub".
	if err := s.Memberships.Upsert(ctx, &Membership{
		TenantID: DefaultTenantID,
		UserSub:  "old-sub",
		Username: "admin",
		Role:     RoleOwner,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// New SimpleAuth issues a different sub for the same admin.
	got, err := s.Memberships.ListByUser(ctx, "new-sub", "admin")
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(got) != 1 || got[0].UserSub != "new-sub" {
		t.Fatalf("expected adoption to new-sub, got %+v", got)
	}
}

// TestListByUser_DoesNotStealRegularUserMembership is the security
// regression. Two regular users (neither owns the default tenant)
// share a display username — adoption MUST NOT move user-A's row to
// user-B's sub.
func TestListByUser_DoesNotStealRegularUserMembership(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// Create a non-default tenant to host the regular user.
	tnt := &Tenant{Slug: "team-x", Name: "Team X"}
	if err := s.Tenants.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	// User A is operator on team-x.
	if err := s.Memberships.Upsert(ctx, &Membership{
		TenantID: tnt.ID,
		UserSub:  "user-a-sub",
		Username: "alice",
		Role:     RoleOperator,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// User B logs in with a different sub but the same display name.
	// They should see ZERO memberships — A's row must not be adopted.
	got, err := s.Memberships.ListByUser(ctx, "user-b-sub", "alice")
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("collision-username adoption should not happen for non-admin rows: got %+v", got)
	}
	// User A should still see their row when they log back in.
	got, err = s.Memberships.ListByUser(ctx, "user-a-sub", "alice")
	if err != nil {
		t.Fatalf("ListByUser A: %v", err)
	}
	if len(got) != 1 || got[0].UserSub != "user-a-sub" {
		t.Errorf("user A's row was modified: %+v", got)
	}
}

// TestListByUser_AdoptsSecondaryTenantsForBootstrapAdmin covers the
// case where the bootstrap admin owns multiple tenants: after the
// default-tenant row is salvaged, any other rows for the same username
// also get adopted so the admin doesn't have to log in twice to
// see all of their tenants.
func TestListByUser_AdoptsSecondaryTenantsForBootstrapAdmin(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	other := &Tenant{Slug: "secondary", Name: "Secondary"}
	if err := s.Tenants.Create(ctx, other); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	// Two stale rows: admin owner of default + operator of secondary.
	if err := s.Memberships.Upsert(ctx, &Membership{
		TenantID: DefaultTenantID,
		UserSub:  "old-sub",
		Username: "admin",
		Role:     RoleOwner,
	}); err != nil {
		t.Fatalf("seed default: %v", err)
	}
	if err := s.Memberships.Upsert(ctx, &Membership{
		TenantID: other.ID,
		UserSub:  "old-sub",
		Username: "admin",
		Role:     RoleAdmin,
	}); err != nil {
		t.Fatalf("seed secondary: %v", err)
	}
	got, err := s.Memberships.ListByUser(ctx, "new-sub", "admin")
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 adopted rows, got %d: %+v", len(got), got)
	}
	for _, m := range got {
		if m.UserSub != "new-sub" {
			t.Errorf("row %s still on old sub: %+v", m.TenantID, m)
		}
	}
}
