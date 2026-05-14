package dlq

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// openTestStore opens a fresh SQLite store backed by a tempdir. Mirrors the
// helper in internal/storage/storage_test.go.
func openTestStore(t *testing.T) *storage.Store {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "dlq-test.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	s, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDLQ_Push_PersistsEntry(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()

	svc := NewService(store, pool, Options{MaxRetries: 3})

	err := svc.Push(context.Background(), storage.DLQEntry{
		PipelineID:  "",
		SourceQueue: "SRC",
		OriginalMsg: []byte("payload"),
		ErrorReason: "validation failed",
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	list, total, err := svc.List(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected one DLQ entry, got total=%d len=%d", total, len(list))
	}
	if list[0].ErrorReason != "validation failed" {
		t.Errorf("reason mismatch: %q", list[0].ErrorReason)
	}
}

func TestDLQ_MaxRetries(t *testing.T) {
	svc := NewService(openTestStore(t), mq.NewPool(mq.PoolOptions{}), Options{MaxRetries: 7})
	if svc.MaxRetries() != 7 {
		t.Errorf("MaxRetries = %d, want 7", svc.MaxRetries())
	}
	// Default applies.
	def := NewService(openTestStore(t), mq.NewPool(mq.PoolOptions{}), Options{})
	if def.MaxRetries() != 3 {
		t.Errorf("default MaxRetries = %d, want 3", def.MaxRetries())
	}
}

func TestDLQ_Retry_FailsOnMissingEntry(t *testing.T) {
	svc := NewService(openTestStore(t), mq.NewPool(mq.PoolOptions{}), Options{})
	defer mq.NewPool(mq.PoolOptions{}).Close()

	err := svc.Retry(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent retry")
	}
}

func TestDLQ_Retry_FailsAtMaxRetries(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()
	svc := NewService(store, pool, Options{MaxRetries: 1})

	// Seed an entry already at retry_count = 1 (== max).
	ctx := context.Background()
	e := &storage.DLQEntry{
		PipelineID:  "",
		SourceQueue: "SRC",
		OriginalMsg: []byte("x"),
		ErrorReason: "boom",
		RetryCount:  1,
	}
	if err := store.DLQ.Insert(ctx, e); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := svc.Retry(ctx, e.ID)
	if !errors.Is(err, ErrMaxRetries) {
		t.Errorf("expected ErrMaxRetries, got %v", err)
	}
}

func TestDLQ_Retry_RepublishesViaMemoryConnector(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()
	svc := NewService(store, pool, Options{MaxRetries: 3})

	ctx := context.Background()

	// 1) Storage shape: pipeline + connections
	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x", QueueName: "src-q"}
	dst := &storage.Connection{Name: "dst", Type: "rabbitmq", URL: "amqp://x", QueueName: "dst-q"}
	if err := store.Connections.Create(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := store.Connections.Create(ctx, dst); err != nil {
		t.Fatal(err)
	}
	pipe := &storage.Pipeline{Name: "p1", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	if err := store.Pipelines.Create(ctx, pipe); err != nil {
		t.Fatal(err)
	}

	// 2) Seed a DLQ entry to retry.
	entry := &storage.DLQEntry{
		PipelineID:  pipe.ID,
		SourceQueue: src.QueueName,
		OriginalMsg: []byte(`{"orig":true}`),
		ErrorReason: "test failure",
	}
	if err := store.DLQ.Insert(ctx, entry); err != nil {
		t.Fatalf("seed dlq: %v", err)
	}

	// 3) Pre-seat a MemoryConnector into the pool keyed exactly as the retry
	//    path uses ("dlq-retry-" + entry.ID). This lets us assert republish
	//    without needing a real RabbitMQ.
	reg := mq.NewMemoryRegistry(8)
	mem := mq.NewMemoryConnector(reg, "dst-q")
	if err := mem.Connect(ctx); err != nil {
		t.Fatalf("mem Connect: %v", err)
	}
	pool.InjectForTest("dlq-retry-"+entry.ID, mem)

	if err := svc.Retry(ctx, entry.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	got := reg.Drain("dst-q")
	if len(got) != 1 || string(got[0]) != `{"orig":true}` {
		t.Errorf("republished payload mismatch: %v", got)
	}

	// retry_count should have been incremented.
	reloaded, _ := store.DLQ.Get(ctx, entry.ID)
	if reloaded.RetryCount != 1 {
		t.Errorf("retry_count = %d, want 1", reloaded.RetryCount)
	}
}

func TestDLQ_Delete(t *testing.T) {
	store := openTestStore(t)
	svc := NewService(store, mq.NewPool(mq.PoolOptions{}), Options{})

	ctx := context.Background()
	e := &storage.DLQEntry{OriginalMsg: []byte("x"), ErrorReason: "y"}
	_ = store.DLQ.Insert(ctx, e)

	if err := svc.Delete(ctx, e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.DLQ.Get(ctx, e.ID); err == nil {
		t.Error("entry should be gone")
	}
}
