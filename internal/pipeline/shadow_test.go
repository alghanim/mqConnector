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

// TestShadow_MirrorsPayloadAt100Percent proves the prod send still
// happens and a parallel send lands on the shadow destination when
// shadow_percent=100.
func TestShadow_MirrorsPayloadAt100Percent(t *testing.T) {
	store := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x", QueueName: "src-q"}
	prod := &storage.Connection{Name: "prod", Type: "rabbitmq", URL: "amqp://x", QueueName: "prod-q"}
	shadow := &storage.Connection{Name: "shadow", Type: "rabbitmq", URL: "amqp://x", QueueName: "shadow-q"}
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, src)
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, prod)
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, shadow)

	pipe := &storage.Pipeline{
		Name: "p", SourceID: src.ID, DestinationID: prod.ID, Enabled: true,
		ShadowDestinationID: shadow.ID,
		ShadowPercent:       100,
	}
	_ = store.Pipelines.Create(ctx, storage.DefaultTenantID, pipe)

	reg := mq.NewMemoryRegistry(16)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()
	rawSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = rawSrc.Connect(ctx)
	rawProd := mq.NewMemoryConnector(reg, "prod-q")
	_ = rawProd.Connect(ctx)
	rawShadow := mq.NewMemoryConnector(reg, "shadow-q")
	_ = rawShadow.Connect(ctx)

	var prodSends, shadowSends atomic.Int64
	pool.InjectForTest("source-"+pipe.ID, &recordingConnector{inner: rawSrc})
	pool.InjectForTest("dest-"+pipe.ID, &recordingConnector{
		inner: rawProd,
		sendOverride: func(c context.Context, b []byte) error {
			prodSends.Add(1)
			return rawProd.SendMessage(c, b)
		},
	})
	pool.InjectForTest("shadow-"+pipe.ID, &recordingConnector{
		inner: rawShadow,
		sendOverride: func(c context.Context, b []byte) error {
			shadowSends.Add(1)
			return rawShadow.SendMessage(c, b)
		},
	})

	metricsStore := metrics.New()
	mgr := NewManager(ctx, store, pool, metricsStore, nil, logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	for i := 0; i < 3; i++ {
		_ = rawSrc.SendMessage(ctx, []byte("payload"))
	}
	if err := waitFor(t, 2*time.Second, func() bool {
		return prodSends.Load() >= 3 && shadowSends.Load() >= 3
	}); err != nil {
		t.Fatalf("expected 3 prod + 3 shadow sends; prod=%d shadow=%d",
			prodSends.Load(), shadowSends.Load())
	}

	snap := metricsStore.Snapshot()[pipe.ID]
	if snap.ShadowSent < 3 {
		t.Fatalf("ShadowSent metric not incremented: got %d", snap.ShadowSent)
	}
	if snap.ShadowFailed != 0 {
		t.Fatalf("ShadowFailed should be 0 on success: got %d", snap.ShadowFailed)
	}
}

// TestShadow_FailureDoesNotAffectProd: a shadow send failure must
// NOT trip the prod ack-discipline.
func TestShadow_FailureDoesNotAffectProd(t *testing.T) {
	store := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x", QueueName: "src-q"}
	prod := &storage.Connection{Name: "prod", Type: "rabbitmq", URL: "amqp://x", QueueName: "prod-q"}
	shadow := &storage.Connection{Name: "shadow", Type: "rabbitmq", URL: "amqp://x", QueueName: "shadow-q"}
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, src)
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, prod)
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, shadow)

	pipe := &storage.Pipeline{
		Name: "p", SourceID: src.ID, DestinationID: prod.ID, Enabled: true,
		ShadowDestinationID: shadow.ID,
		ShadowPercent:       100,
	}
	_ = store.Pipelines.Create(ctx, storage.DefaultTenantID, pipe)

	reg := mq.NewMemoryRegistry(16)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()
	rawSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = rawSrc.Connect(ctx)
	recSrc := &recordingConnector{inner: rawSrc}
	rawProd := mq.NewMemoryConnector(reg, "prod-q")
	_ = rawProd.Connect(ctx)
	rawShadow := mq.NewMemoryConnector(reg, "shadow-q")
	_ = rawShadow.Connect(ctx)

	// Shadow always fails.
	pool.InjectForTest("source-"+pipe.ID, recSrc)
	pool.InjectForTest("dest-"+pipe.ID, &recordingConnector{inner: rawProd})
	pool.InjectForTest("shadow-"+pipe.ID, &recordingConnector{
		inner: rawShadow,
		sendOverride: func(_ context.Context, _ []byte) error {
			return errShadowSimulated
		},
	})

	metricsStore := metrics.New()
	mgr := NewManager(ctx, store, pool, metricsStore, nil, logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	_ = rawSrc.SendMessage(ctx, []byte("x"))
	// Prod commit should fire even though shadow failed.
	if err := waitFor(t, 2*time.Second, func() bool {
		return recSrc.commits.Load() >= 1
	}); err != nil {
		t.Fatalf("prod must commit despite shadow failure: commits=%d nacks=%d",
			recSrc.commits.Load(), recSrc.nacks.Load())
	}
	if recSrc.nacks.Load() != 0 {
		t.Fatalf("no Nack expected on shadow failure: got %d", recSrc.nacks.Load())
	}
	snap := metricsStore.Snapshot()[pipe.ID]
	if snap.ShadowFailed < 1 {
		t.Fatalf("ShadowFailed metric not incremented: %d", snap.ShadowFailed)
	}
}

func TestShadowSampleHit_PercentBounds(t *testing.T) {
	if shadowSampleHit([]byte("x"), 0) {
		t.Fatal("0% must never hit")
	}
	if !shadowSampleHit([]byte("x"), 100) {
		t.Fatal("100% must always hit")
	}
}

func TestShadowSampleHit_Deterministic(t *testing.T) {
	// Same payload bytes must always land the same sampling decision
	// so a redelivered message doesn't flip-flop on/off shadow.
	if shadowSampleHit([]byte("abcdefghij"), 50) != shadowSampleHit([]byte("abcdefghij"), 50) {
		t.Fatal("shadowSampleHit must be deterministic on payload")
	}
}

type simulatedShadowError struct{}

func (simulatedShadowError) Error() string { return "shadow simulated failure" }

var errShadowSimulated = simulatedShadowError{}
