package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mqConnector/internal/ai"
)

// openAITestStore mirrors the test-store pattern used elsewhere in
// this package: tempdir SQLite, full migration stack, returns the
// caller a Close-on-cleanup Store. Kept local so it doesn't conflict
// with similarly-named helpers in storage_test.go.
func openAITestStore(t *testing.T) *Store {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "ai.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	s, err := Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAIAuditRepo_InsertAndGet(t *testing.T) {
	s := openAITestStore(t)
	row := &AIAuditRow{
		At:          time.Now().UTC(),
		Feature:     "dlq_cluster_naming",
		CallerSub:   "user-1",
		TenantID:    "tenant-A",
		PromptHash:  "abcd1234abcd1234",
		Model:       "qwen2.5",
		Endpoint:    "http://llm.internal/v1",
		TokensIn:    50,
		TokensOut:   20,
		LatencyMs:   123,
		Outcome:     "ok",
		ResultIDRef: "fp-deadbeef",
	}
	if err := s.AIAudit.Insert(context.Background(), row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if row.ID == "" {
		t.Error("ID should be assigned by Insert")
	}
	got, err := s.AIAudit.Get(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Feature != "dlq_cluster_naming" || got.Outcome != "ok" || got.ResultIDRef != "fp-deadbeef" {
		t.Errorf("Get returned mismatched row: %+v", got)
	}
}

func TestAIAuditRepo_List_FiltersAndOrders(t *testing.T) {
	s := openAITestStore(t)
	ctx := context.Background()
	base := time.Now().UTC()
	rows := []*AIAuditRow{
		{Feature: "dlq_cluster_naming", TenantID: "A", Outcome: "ok", At: base.Add(-3 * time.Hour), Model: "m", PromptHash: "h"},
		{Feature: "dlq_cluster_naming", TenantID: "A", Outcome: "error", At: base.Add(-2 * time.Hour), Model: "m", PromptHash: "h"},
		{Feature: "explain_why_summary", TenantID: "A", Outcome: "ok", At: base.Add(-1 * time.Hour), Model: "m", PromptHash: "h"},
		{Feature: "dlq_cluster_naming", TenantID: "B", Outcome: "ok", At: base.Add(-30 * time.Minute), Model: "m", PromptHash: "h"},
	}
	for _, r := range rows {
		if err := s.AIAudit.Insert(ctx, r); err != nil {
			t.Fatalf("seed insert: %v", err)
		}
	}
	// Scope to tenant A → 3 rows, newest-first.
	listed, total, err := s.AIAudit.List(ctx, AIAuditFilter{TenantID: "A"}, 50, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 || len(listed) != 3 {
		t.Fatalf("tenant A: total=%d len=%d, want 3/3", total, len(listed))
	}
	if listed[0].Feature != "explain_why_summary" {
		t.Errorf("newest first violated: top row feature=%q", listed[0].Feature)
	}
	// Filter by feature on top of tenant.
	listed, total, err = s.AIAudit.List(ctx, AIAuditFilter{TenantID: "A", Feature: "dlq_cluster_naming"}, 50, 0)
	if err != nil {
		t.Fatalf("List feature: %v", err)
	}
	if total != 2 || len(listed) != 2 {
		t.Errorf("filter feature: total=%d len=%d, want 2/2", total, len(listed))
	}
	// Filter by outcome.
	listed, total, _ = s.AIAudit.List(ctx, AIAuditFilter{TenantID: "A", Outcome: "ok"}, 50, 0)
	if total != 2 {
		t.Errorf("outcome=ok rows in A: total=%d, want 2", total)
	}
}

// TestAIAuditRepo_LogIsBestEffort confirms the Log wrapper drops
// errors silently. The contract is critical: a degraded DB must never
// break the request path.
func TestAIAuditRepo_LogIsBestEffort(t *testing.T) {
	s := openAITestStore(t)
	// Force a violation: Outcome="" → Insert returns an error;
	// Log must swallow it without panicking. Deliberately drive
	// through Log to exercise the wrapper.
	s.AIAudit.Log(context.Background(), ai.AuditRow{
		Feature:  ai.CapDLQClusterNaming,
		Outcome:  "", // empty → Insert errors
		Model:    "m",
		Endpoint: "x",
	})
	// If we reached here without panicking the contract held.
}

// TestAIAuditRepo_LogPersistsViaInterface confirms that calling
// Log() through the ai.AuditLogger interface (the production path)
// inserts a row. Same test verifies the compile-time interface
// satisfaction is the same shape callers actually see.
func TestAIAuditRepo_LogPersistsViaInterface(t *testing.T) {
	s := openAITestStore(t)
	var logger ai.AuditLogger = s.AIAudit
	logger.Log(context.Background(), ai.AuditRow{
		Feature:    ai.CapDLQClusterNaming,
		TenantID:   "T1",
		PromptHash: "deadbeefdeadbeef",
		Model:      "m",
		Endpoint:   "x",
		Outcome:    "ok",
	})
	rows, total, err := s.AIAudit.List(context.Background(), AIAuditFilter{TenantID: "T1"}, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("want 1 row in T1, got total=%d len=%d", total, len(rows))
	}
	if rows[0].Feature != "dlq_cluster_naming" {
		t.Errorf("feature = %q, want dlq_cluster_naming", rows[0].Feature)
	}
}
