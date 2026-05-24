package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// TestDedup_DuplicatePayloadSkipsSend proves the dedup gate
// short-circuits the outbound send for a byte-identical payload
// while still committing the source (so the broker stops
// redelivering).
func TestDedup_DuplicatePayloadSkipsSend(t *testing.T) {
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
	pipe := &storage.Pipeline{
		Name: "p", SourceID: src.ID, DestinationID: dst.ID, Enabled: true,
		DedupWindowSeconds: 60,
	}
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

	var sends atomic.Int64
	recDst := &recordingConnector{
		inner: rawDst,
		sendOverride: func(c context.Context, b []byte) error {
			sends.Add(1)
			return rawDst.SendMessage(c, b)
		},
	}
	pool.InjectForTest("source-"+pipe.ID, recSrc)
	pool.InjectForTest("dest-"+pipe.ID, recDst)

	metricsStore := metrics.New()
	mgr := NewManager(ctx, store, pool, metricsStore, nil, logging.New("error", "json"))
	mgr.SetDeduper(store.Dedup)
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// Push the same payload twice — dedup should let the first through
	// and skip the second.
	if err := rawSrc.SendMessage(ctx, []byte("same-payload")); err != nil {
		t.Fatal(err)
	}
	if err := rawSrc.SendMessage(ctx, []byte("same-payload")); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(t, 2*time.Second, func() bool {
		return recSrc.commits.Load() >= 2
	}); err != nil {
		t.Fatalf("expected 2 commits (both source messages handled): commits=%d nacks=%d",
			recSrc.commits.Load(), recSrc.nacks.Load())
	}

	if got := sends.Load(); got != 1 {
		t.Fatalf("expected exactly 1 destination send (second was dupe): got %d", got)
	}
	if got := recSrc.nacks.Load(); got != 0 {
		t.Fatalf("dedup hit must commit, never nack: got %d nacks", got)
	}
	// Metrics gauge should have recorded one dedup skip.
	snap := metricsStore.Snapshot()[pipe.ID]
	if snap.DedupSkipped != 1 {
		t.Fatalf("DedupSkipped = %d, want 1", snap.DedupSkipped)
	}
}

// TestDedup_DistinctPayloadsBothDelivered confirms the dedup window
// keys on payload bytes only — two different messages on the same
// pipeline both reach the destination.
func TestDedup_DistinctPayloadsBothDelivered(t *testing.T) {
	store := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x", QueueName: "src-q"}
	dst := &storage.Connection{Name: "dst", Type: "rabbitmq", URL: "amqp://x", QueueName: "dst-q"}
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, src)
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, dst)
	pipe := &storage.Pipeline{
		Name: "p", SourceID: src.ID, DestinationID: dst.ID, Enabled: true,
		DedupWindowSeconds: 60,
	}
	_ = store.Pipelines.Create(ctx, storage.DefaultTenantID, pipe)

	reg := mq.NewMemoryRegistry(16)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()
	rawSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = rawSrc.Connect(ctx)
	rawDst := mq.NewMemoryConnector(reg, "dst-q")
	_ = rawDst.Connect(ctx)
	var sends atomic.Int64
	recDst := &recordingConnector{
		inner: rawDst,
		sendOverride: func(c context.Context, b []byte) error {
			sends.Add(1)
			return rawDst.SendMessage(c, b)
		},
	}
	pool.InjectForTest("source-"+pipe.ID, &recordingConnector{inner: rawSrc})
	pool.InjectForTest("dest-"+pipe.ID, recDst)

	mgr := NewManager(ctx, store, pool, metrics.New(), nil, logging.New("error", "json"))
	mgr.SetDeduper(store.Dedup)
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	_ = rawSrc.SendMessage(ctx, []byte("a"))
	_ = rawSrc.SendMessage(ctx, []byte("b"))
	if err := waitFor(t, 2*time.Second, func() bool { return sends.Load() >= 2 }); err != nil {
		t.Fatalf("both distinct payloads should have been delivered, got %d", sends.Load())
	}
}

// TestDedup_WindowDisabledByDefault verifies the global at-least-once
// contract is preserved: a pipeline with DedupWindowSeconds = 0 sees
// every duplicate forwarded to the destination unchanged.
func TestDedup_WindowDisabledByDefault(t *testing.T) {
	store := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x", QueueName: "src-q"}
	dst := &storage.Connection{Name: "dst", Type: "rabbitmq", URL: "amqp://x", QueueName: "dst-q"}
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, src)
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, dst)
	pipe := &storage.Pipeline{
		Name: "p", SourceID: src.ID, DestinationID: dst.ID, Enabled: true,
		// DedupWindowSeconds intentionally zero.
	}
	_ = store.Pipelines.Create(ctx, storage.DefaultTenantID, pipe)

	reg := mq.NewMemoryRegistry(16)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()
	rawSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = rawSrc.Connect(ctx)
	rawDst := mq.NewMemoryConnector(reg, "dst-q")
	_ = rawDst.Connect(ctx)
	var sends atomic.Int64
	recDst := &recordingConnector{
		inner: rawDst,
		sendOverride: func(c context.Context, b []byte) error {
			sends.Add(1)
			return rawDst.SendMessage(c, b)
		},
	}
	pool.InjectForTest("source-"+pipe.ID, &recordingConnector{inner: rawSrc})
	pool.InjectForTest("dest-"+pipe.ID, recDst)

	mgr := NewManager(ctx, store, pool, metrics.New(), nil, logging.New("error", "json"))
	mgr.SetDeduper(store.Dedup) // deduper present but pipeline window is 0
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	_ = rawSrc.SendMessage(ctx, []byte("x"))
	_ = rawSrc.SendMessage(ctx, []byte("x"))
	if err := waitFor(t, 2*time.Second, func() bool { return sends.Load() >= 2 }); err != nil {
		t.Fatalf("with dedup off, both copies must be forwarded: got %d", sends.Load())
	}
}

func TestPayloadHash_StableAndDistinguishing(t *testing.T) {
	if payloadHash([]byte("a")) == payloadHash([]byte("b")) {
		t.Fatalf("distinct payloads must hash differently")
	}
	if payloadHash([]byte("a")) != payloadHash([]byte("a")) {
		t.Fatalf("hash must be stable for the same input")
	}
}
