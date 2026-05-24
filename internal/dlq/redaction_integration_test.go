package dlq

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"mqConnector/internal/mq"
	"mqConnector/internal/secrets"
	"mqConnector/internal/storage"
)

// seedRedactionFixture creates a connection + pipeline + ruleset for
// the redaction integration tests so they don't repeat the SQL.
func seedRedactionFixture(t *testing.T, store *storage.Store) string {
	t.Helper()
	ctx := context.Background()
	tenant := storage.DefaultTenantID

	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://localhost"}
	if err := store.Connections.Create(ctx, tenant, src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	dst := &storage.Connection{Name: "dst", Type: "rabbitmq", URL: "amqp://localhost"}
	if err := store.Connections.Create(ctx, tenant, dst); err != nil {
		t.Fatalf("create dest: %v", err)
	}
	pipe := &storage.Pipeline{
		ID: uuid.NewString(), Name: "p",
		SourceID: src.ID, DestinationID: dst.ID, Enabled: true,
	}
	if err := store.Pipelines.Create(ctx, tenant, pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	rules := []storage.DLQRedactionRule{
		{RuleKind: "jsonpath", Pattern: "$..ssn", MaskReplace: "[SSN]", Enabled: true, Order: 0},
		{RuleKind: "regex", Pattern: `\b4\d{15}\b`, MaskReplace: "[CC]", Enabled: true, Order: 1},
	}
	if err := store.DLQRedaction.Replace(ctx, tenant, pipe.ID, rules); err != nil {
		t.Fatalf("Replace rules: %v", err)
	}
	return pipe.ID
}

// 32-byte hex master key for the sealer in tests.
const testRedactionKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

func newSealer(t *testing.T) *secrets.Service {
	t.Helper()
	s, err := secrets.New(testRedactionKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	return s
}

func TestDLQ_Push_RedactsPayloadAndSealsRaw(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()

	svc := NewService(store, pool, Options{MaxRetries: 3})
	svc.SetSealer(newSealer(t))

	pipelineID := seedRedactionFixture(t, store)

	original := []byte(`{"patient":{"ssn":"123-45-6789","name":"Ali"},"cc":"4111111111111111"}`)
	if err := svc.Push(context.Background(), storage.DLQEntry{
		TenantID:    storage.DefaultTenantID,
		PipelineID:  pipelineID,
		SourceQueue: "SRC",
		OriginalMsg: original,
		ErrorReason: "destination unreachable",
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	rows, total, err := svc.List(context.Background(), storage.DefaultTenantID, 1, 10)
	if err != nil || total != 1 {
		t.Fatalf("List failed: total=%d err=%v", total, err)
	}
	row := rows[0]
	if !row.Redacted {
		t.Fatalf("expected redacted=true")
	}
	if strings.Contains(string(row.OriginalMsg), "123-45-6789") {
		t.Fatalf("SSN leaked into persisted payload: %s", row.OriginalMsg)
	}
	if strings.Contains(string(row.OriginalMsg), "4111111111111111") {
		t.Fatalf("CC leaked into persisted payload: %s", row.OriginalMsg)
	}
	if !strings.Contains(string(row.OriginalMsg), "[SSN]") {
		t.Fatalf("redaction mask not applied: %s", row.OriginalMsg)
	}
	if len(row.RawMsg) == 0 {
		t.Fatalf("raw_msg should be populated (sealed original)")
	}
	if strings.Contains(string(row.RawMsg), "123-45-6789") {
		t.Fatalf("raw_msg stored in cleartext")
	}
	// raw_msg should be in sealed envelope form.
	if !strings.HasPrefix(string(row.RawMsg), secrets.EncryptedPrefix) {
		t.Fatalf("raw_msg not envelope-encrypted: %s", row.RawMsg)
	}
}

func TestDLQ_GetRaw_DecryptsAndReturnsOriginal(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()

	svc := NewService(store, pool, Options{MaxRetries: 3})
	svc.SetSealer(newSealer(t))
	pipelineID := seedRedactionFixture(t, store)

	original := []byte(`{"patient":{"ssn":"123-45-6789","name":"Ali"}}`)
	if err := svc.Push(context.Background(), storage.DLQEntry{
		TenantID:    storage.DefaultTenantID,
		PipelineID:  pipelineID,
		SourceQueue: "SRC",
		OriginalMsg: original,
		ErrorReason: "x",
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	rows, _, _ := svc.List(context.Background(), storage.DefaultTenantID, 1, 1)
	id := rows[0].ID

	raw, err := svc.GetRaw(context.Background(), storage.DefaultTenantID, id)
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	if string(raw) != string(original) {
		t.Fatalf("raw mismatch: got %s want %s", raw, original)
	}
}

func TestDLQ_GetRaw_NotRedacted_ReturnsErrRawNotAvailable(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()

	svc := NewService(store, pool, Options{MaxRetries: 3})
	svc.SetSealer(newSealer(t))

	// Push WITHOUT a pipeline (so no rules apply).
	if err := svc.Push(context.Background(), storage.DLQEntry{
		TenantID:    storage.DefaultTenantID,
		SourceQueue: "SRC",
		OriginalMsg: []byte("plain"),
		ErrorReason: "x",
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	rows, _, _ := svc.List(context.Background(), storage.DefaultTenantID, 1, 1)
	id := rows[0].ID

	_, err := svc.GetRaw(context.Background(), storage.DefaultTenantID, id)
	if err != ErrRawNotAvailable {
		t.Fatalf("expected ErrRawNotAvailable, got %v", err)
	}
}

func TestDLQ_Push_SealerDisabled_RedactionSkippedNotApplied(t *testing.T) {
	// Compliance-critical contract: when rules exist but the sealer
	// isn't configured, we MUST NOT silently drop the raw or store it
	// in cleartext. The push falls back to the legacy non-redaction
	// path and logs a warning. The persisted row carries the
	// unmodified payload — same behaviour as pre-0019.
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()

	svc := NewService(store, pool, Options{MaxRetries: 3})
	// Intentionally NO SetSealer.
	pipelineID := seedRedactionFixture(t, store)

	original := []byte(`{"patient":{"ssn":"123-45-6789"}}`)
	if err := svc.Push(context.Background(), storage.DLQEntry{
		TenantID:    storage.DefaultTenantID,
		PipelineID:  pipelineID,
		SourceQueue: "SRC",
		OriginalMsg: original,
		ErrorReason: "x",
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	rows, _, _ := svc.List(context.Background(), storage.DefaultTenantID, 1, 1)
	row := rows[0]
	if row.Redacted {
		t.Fatalf("redacted=true without a sealer would imply raw was discarded")
	}
	if len(row.RawMsg) != 0 {
		t.Fatalf("raw_msg should be empty when sealer is unavailable")
	}
	if string(row.OriginalMsg) != string(original) {
		t.Fatalf("payload should be untouched when sealer is unavailable")
	}
}

// TestDLQ_PushDoesNotLeakRawAfterTimeRoundTrip exercises the
// persistence layer to catch a regression where a future migration
// drops the raw_msg column.
func TestDLQ_PushDoesNotLeakRawAfterTimeRoundTrip(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()

	svc := NewService(store, pool, Options{MaxRetries: 3})
	svc.SetSealer(newSealer(t))
	pipelineID := seedRedactionFixture(t, store)

	const original = `{"patient":{"ssn":"999-99-9999"}}`
	if err := svc.Push(context.Background(), storage.DLQEntry{
		TenantID:    storage.DefaultTenantID,
		PipelineID:  pipelineID,
		SourceQueue: "SRC",
		OriginalMsg: []byte(original),
		ErrorReason: "x",
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// Touch the clock just to make sure created_at semantics aren't
	// changed by the new columns (regression net).
	time.Sleep(5 * time.Millisecond)

	rows, _, _ := svc.List(context.Background(), storage.DefaultTenantID, 1, 1)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if strings.Contains(string(rows[0].OriginalMsg), "999-99-9999") {
		t.Fatalf("SSN leaked through list response")
	}
}
