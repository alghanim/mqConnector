package storage

import (
	"context"
	"testing"
	"time"
)

func TestAudit_InsertAndList(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_ = s.Audit.Insert(ctx, &AuditEntry{
			Actor:    "alice",
			ActorSub: "u1",
			Action:   "POST",
			Resource: "/api/v1/connections",
			Status:   201,
		})
	}
	list, total, err := s.Audit.List(ctx, DefaultTenantID, AuditFilter{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(list) != 3 {
		t.Errorf("expected 3 entries, got total=%d len=%d", total, len(list))
	}
}

func TestAudit_ActorFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.Audit.Insert(ctx, &AuditEntry{Actor: "alice", Action: "POST", Resource: "/api/v1/connections"})
	_ = s.Audit.Insert(ctx, &AuditEntry{Actor: "bob", Action: "POST", Resource: "/api/v1/connections"})

	list, total, _ := s.Audit.List(ctx, DefaultTenantID, AuditFilter{Actor: "alice"}, 1, 10)
	if total != 1 || list[0].Actor != "alice" {
		t.Errorf("actor filter failed: total=%d list=%v", total, list)
	}
}

func TestAudit_ResourcePrefixFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.Audit.Insert(ctx, &AuditEntry{Resource: "/api/v1/connections/abc", Action: "PUT"})
	_ = s.Audit.Insert(ctx, &AuditEntry{Resource: "/api/v1/pipelines/xyz", Action: "PUT"})

	list, total, _ := s.Audit.List(ctx, DefaultTenantID, AuditFilter{Resource: "/api/v1/connections"}, 1, 10)
	if total != 1 || list[0].Resource != "/api/v1/connections/abc" {
		t.Errorf("resource prefix filter failed: %+v", list)
	}
}

func TestAudit_TimeRangeFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-time.Hour).UTC()
	now := time.Now().UTC()

	_ = s.Audit.Insert(ctx, &AuditEntry{At: old, Action: "POST", Resource: "/x"})
	_ = s.Audit.Insert(ctx, &AuditEntry{At: now, Action: "POST", Resource: "/x"})

	cutoff := time.Now().Add(-30 * time.Minute).UTC()
	list, total, _ := s.Audit.List(ctx, DefaultTenantID, AuditFilter{Since: &cutoff}, 1, 10)
	if total != 1 || !list[0].At.After(cutoff) {
		t.Errorf("since filter failed: %d entries, first.At=%v", total, list[0].At)
	}
}

// TestAudit_ChainOK confirms a fresh chain of inserts verifies cleanly.
// Each insert should pick up the prior row's hash as prev_hash and
// compute its own hash off (prev || canonical(row)).
func TestAudit_ChainOK(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := s.Audit.Insert(ctx, &AuditEntry{
			Actor: "alice", Action: "POST", Resource: "/api/v1/connections", Status: 201,
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	statuses, err := s.Audit.Verify(ctx, DefaultTenantID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Status != "ok" || statuses[0].Checked != 5 {
		t.Fatalf("expected one OK chain of 5; got %+v", statuses)
	}
}

// TestAudit_ChainBrokenByMutation simulates direct DB tampering and
// confirms Verify pinpoints the mutated row.
func TestAudit_ChainBrokenByMutation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		_ = s.Audit.Insert(ctx, &AuditEntry{
			Actor: "alice", Action: "POST", Resource: "/api/v1/connections", Status: 201,
		})
	}
	// Pick the 2nd-inserted row (ORDER BY at ASC) and mutate its
	// resource — the recomputed hash should no longer match the stored
	// hash, so Verify must flag it.
	var victimID string
	if err := s.DB.QueryRow(`SELECT id FROM audit_log WHERE tenant_id = ? ORDER BY at ASC LIMIT 1 OFFSET 1`,
		DefaultTenantID).Scan(&victimID); err != nil {
		t.Fatalf("pick victim: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE audit_log SET resource = ? WHERE id = ?`,
		"/api/v1/EVIL", victimID); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	statuses, err := s.Audit.Verify(ctx, DefaultTenantID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("expected one chain status, got %d", len(statuses))
	}
	if statuses[0].Status != "broken" {
		t.Fatalf("expected status=broken, got %q", statuses[0].Status)
	}
	if statuses[0].FirstBrokenID != victimID {
		t.Fatalf("expected first broken id=%s, got %s", victimID, statuses[0].FirstBrokenID)
	}
}

// TestAudit_Diff round-trips before/after JSON.
func TestAudit_Diff(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	e := &AuditEntry{Actor: "alice", Action: "PUT", Resource: "/api/v1/connections/x"}
	if err := s.Audit.Insert(ctx, e); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.Audit.SaveDiff(ctx, e.ID, `{"name":"old"}`, `{"name":"new"}`); err != nil {
		t.Fatalf("save diff: %v", err)
	}
	got, err := s.Audit.GetDiff(ctx, e.ID)
	if err != nil {
		t.Fatalf("get diff: %v", err)
	}
	if got.Before != `{"name":"old"}` || got.After != `{"name":"new"}` {
		t.Fatalf("diff round-trip mismatch: %+v", got)
	}
}
