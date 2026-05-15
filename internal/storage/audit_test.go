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
	list, total, err := s.Audit.List(ctx, AuditFilter{}, 1, 10)
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

	list, total, _ := s.Audit.List(ctx, AuditFilter{Actor: "alice"}, 1, 10)
	if total != 1 || list[0].Actor != "alice" {
		t.Errorf("actor filter failed: total=%d list=%v", total, list)
	}
}

func TestAudit_ResourcePrefixFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.Audit.Insert(ctx, &AuditEntry{Resource: "/api/v1/connections/abc", Action: "PUT"})
	_ = s.Audit.Insert(ctx, &AuditEntry{Resource: "/api/v1/pipelines/xyz", Action: "PUT"})

	list, total, _ := s.Audit.List(ctx, AuditFilter{Resource: "/api/v1/connections"}, 1, 10)
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
	list, total, _ := s.Audit.List(ctx, AuditFilter{Since: &cutoff}, 1, 10)
	if total != 1 || !list[0].At.After(cutoff) {
		t.Errorf("since filter failed: %d entries, first.At=%v", total, list[0].At)
	}
}
