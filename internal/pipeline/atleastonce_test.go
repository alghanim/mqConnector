package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mqConnector/internal/dlq"
	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// recordingConnector wraps an mq.Connector and counts Commit/Nack calls.
// Used by the at-least-once tests to assert the executor ack-discipline
// without needing a real broker — the contract is "Commit on handed-off
// success, Nack on both-failed."
type recordingConnector struct {
	inner mq.Connector

	commits atomic.Int64
	nacks   atomic.Int64

	sendOverride func(ctx context.Context, msg []byte) error
}

func (r *recordingConnector) Connect(ctx context.Context) error { return r.inner.Connect(ctx) }
func (r *recordingConnector) Disconnect() error                 { return r.inner.Disconnect() }
func (r *recordingConnector) Ping(ctx context.Context) error    { return r.inner.Ping(ctx) }
func (r *recordingConnector) ReceiveMessage(ctx context.Context) ([]byte, error) {
	return r.inner.ReceiveMessage(ctx)
}
func (r *recordingConnector) SendMessage(ctx context.Context, msg []byte) error {
	if r.sendOverride != nil {
		return r.sendOverride(ctx, msg)
	}
	return r.inner.SendMessage(ctx, msg)
}
func (r *recordingConnector) Commit(ctx context.Context) error {
	r.commits.Add(1)
	return r.inner.Commit(ctx)
}
func (r *recordingConnector) Nack(ctx context.Context, requeue bool) error {
	r.nacks.Add(1)
	return r.inner.Nack(ctx, requeue)
}

// TestAtLeastOnce_CommitOnSuccess proves the happy path: when the
// downstream send succeeds, the executor calls source.Commit so the
// broker stops redelivering. Without this, a restart would replay
// already-delivered messages — at-least-once degrades to "at-least-
// twice on any reload."
func TestAtLeastOnce_CommitOnSuccess(t *testing.T) {
	store := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x", QueueName: "src-q"}
	dst := &storage.Connection{Name: "dst", Type: "rabbitmq", URL: "amqp://x", QueueName: "dst-q"}
	if err := store.Connections.Create(ctx, storage.DefaultTenantID, src); err != nil {
		t.Fatal(err)
	}
	if err := store.Connections.Create(ctx, storage.DefaultTenantID, dst); err != nil {
		t.Fatal(err)
	}
	pipe := &storage.Pipeline{Name: "p", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	if err := store.Pipelines.Create(ctx, storage.DefaultTenantID, pipe); err != nil {
		t.Fatal(err)
	}

	reg := mq.NewMemoryRegistry(16)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()

	rawSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = rawSrc.Connect(ctx)
	recSrc := &recordingConnector{inner: rawSrc}
	rawDst := mq.NewMemoryConnector(reg, "dst-q")
	_ = rawDst.Connect(ctx)
	pool.InjectForTest("source-"+pipe.ID, recSrc)
	pool.InjectForTest("dest-"+pipe.ID, rawDst)

	metricsStore := metrics.New()
	mgr := NewManager(ctx, store, pool, metricsStore, nil, logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// Push one message; expect it to land on dst-q and the source to
	// receive exactly one Commit.
	if err := rawSrc.SendMessage(ctx, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(t, time.Second, func() bool {
		return recSrc.commits.Load() >= 1
	}); err != nil {
		t.Fatalf("expected source.Commit to be called: commits=%d nacks=%d",
			recSrc.commits.Load(), recSrc.nacks.Load())
	}
	if got := recSrc.nacks.Load(); got != 0 {
		t.Fatalf("did not expect any Nack on the happy path: got %d", got)
	}
}

// TestAtLeastOnce_CommitWhenDLQAccepts proves the "stage / send fails
// but DLQ accepts the bytes" path: the executor commits the source
// because the message is now owned by the DLQ table. If we didn't,
// the broker would redeliver and we'd DLQ-double-push every restart.
func TestAtLeastOnce_CommitWhenDLQAccepts(t *testing.T) {
	store := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x", QueueName: "src-q"}
	dst := &storage.Connection{Name: "dst", Type: "rabbitmq", URL: "amqp://x", QueueName: "dst-q"}
	if err := store.Connections.Create(ctx, storage.DefaultTenantID, src); err != nil {
		t.Fatal(err)
	}
	if err := store.Connections.Create(ctx, storage.DefaultTenantID, dst); err != nil {
		t.Fatal(err)
	}
	pipe := &storage.Pipeline{Name: "p", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	if err := store.Pipelines.Create(ctx, storage.DefaultTenantID, pipe); err != nil {
		t.Fatal(err)
	}

	reg := mq.NewMemoryRegistry(16)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()

	rawSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = rawSrc.Connect(ctx)
	recSrc := &recordingConnector{inner: rawSrc}
	rawDst := mq.NewMemoryConnector(reg, "dst-q")
	_ = rawDst.Connect(ctx)
	// Force every destination send to fail. The DLQ should soak up the
	// message and the source should still see a Commit.
	recDst := &recordingConnector{
		inner:        rawDst,
		sendOverride: func(_ context.Context, _ []byte) error { return errors.New("simulated dest outage") },
	}
	pool.InjectForTest("source-"+pipe.ID, recSrc)
	pool.InjectForTest("dest-"+pipe.ID, recDst)

	metricsStore := metrics.New()
	dlqSvc := dlq.NewService(store, pool, dlq.Options{MaxRetries: 3, Logger: logging.New("error", "json")})
	mgr := NewManager(ctx, store, pool, metricsStore, dlqSvc, logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	if err := rawSrc.SendMessage(ctx, []byte("stuck")); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(t, time.Second, func() bool {
		dlqList, _, _ := store.DLQ.List(ctx, storage.DefaultTenantID, 1, 10)
		return len(dlqList) >= 1 && recSrc.commits.Load() >= 1
	}); err != nil {
		dlqList, _, _ := store.DLQ.List(ctx, storage.DefaultTenantID, 1, 10)
		t.Fatalf("expected DLQ+Commit: dlq=%d commits=%d nacks=%d",
			len(dlqList), recSrc.commits.Load(), recSrc.nacks.Load())
	}
	if got := recSrc.nacks.Load(); got != 0 {
		t.Fatalf("did not expect Nack when DLQ accepted: got %d", got)
	}
}

// TestAtLeastOnce_NackWhenDLQDisabled proves the worst-case path:
// downstream send fails AND the DLQ pathway is unavailable. The
// executor must Nack so the source broker redelivers — anything else
// silently drops the message.
func TestAtLeastOnce_NackWhenDLQDisabled(t *testing.T) {
	store := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x", QueueName: "src-q"}
	dst := &storage.Connection{Name: "dst", Type: "rabbitmq", URL: "amqp://x", QueueName: "dst-q"}
	if err := store.Connections.Create(ctx, storage.DefaultTenantID, src); err != nil {
		t.Fatal(err)
	}
	if err := store.Connections.Create(ctx, storage.DefaultTenantID, dst); err != nil {
		t.Fatal(err)
	}
	pipe := &storage.Pipeline{Name: "p", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	if err := store.Pipelines.Create(ctx, storage.DefaultTenantID, pipe); err != nil {
		t.Fatal(err)
	}

	reg := mq.NewMemoryRegistry(16)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()

	rawSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = rawSrc.Connect(ctx)
	recSrc := &recordingConnector{inner: rawSrc}
	rawDst := mq.NewMemoryConnector(reg, "dst-q")
	_ = rawDst.Connect(ctx)
	recDst := &recordingConnector{
		inner:        rawDst,
		sendOverride: func(_ context.Context, _ []byte) error { return errors.New("simulated dest outage") },
	}
	pool.InjectForTest("source-"+pipe.ID, recSrc)
	pool.InjectForTest("dest-"+pipe.ID, recDst)

	// NewManager with a nil DLQ — executor.finalize should pick Nack
	// because "dlq disabled" counts as a failed handoff.
	metricsStore := metrics.New()
	mgr := NewManager(ctx, store, pool, metricsStore, nil, logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	if err := rawSrc.SendMessage(ctx, []byte("stranded")); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(t, time.Second, func() bool {
		return recSrc.nacks.Load() >= 1
	}); err != nil {
		t.Fatalf("expected source.Nack when DLQ disabled: commits=%d nacks=%d",
			recSrc.commits.Load(), recSrc.nacks.Load())
	}
}

// guard against the unused-var lint when this file is otherwise the only
// importer of sync.
var _ = sync.Mutex{}
