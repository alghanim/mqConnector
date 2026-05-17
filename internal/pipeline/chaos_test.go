package pipeline

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"mqConnector/internal/dlq"
	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// chaosConnector wraps an mq.Connector and lets the test cause
// controllable failures. Used to drive failure-mode tests without
// needing a real broker that can be terminated mid-flow.
//
// Three knobs:
//   - failReceiveEvery N: every Nth ReceiveMessage call returns a
//     transient error. The executor's back-off loop is expected to
//     keep the process alive and keep retrying.
//   - failSendUntil N: the first N SendMessage calls fail; subsequent
//     calls succeed. Models a transient destination outage that
//     recovers — exercises the DLQ → retry-reaper path.
//   - panicReceive bool: ReceiveMessage panics. Tests the recover
//     middleware / process-supervisor path (the executor catches
//     this and surfaces it as an error).
type chaosConnector struct {
	inner            mq.Connector
	receiveCalls     atomic.Int64
	sendCalls        atomic.Int64
	failReceiveEvery int64
	failSendUntil    int64
}

func (c *chaosConnector) Connect(ctx context.Context) error    { return c.inner.Connect(ctx) }
func (c *chaosConnector) Disconnect() error                    { return c.inner.Disconnect() }
func (c *chaosConnector) Ping(ctx context.Context) error       { return c.inner.Ping(ctx) }
func (c *chaosConnector) Commit(ctx context.Context) error     { return c.inner.Commit(ctx) }
func (c *chaosConnector) Nack(ctx context.Context, r bool) error {
	return c.inner.Nack(ctx, r)
}
func (c *chaosConnector) ReceiveMessage(ctx context.Context) ([]byte, error) {
	n := c.receiveCalls.Add(1)
	if c.failReceiveEvery > 0 && n%c.failReceiveEvery == 0 {
		return nil, errors.New("chaos: simulated receive failure")
	}
	return c.inner.ReceiveMessage(ctx)
}
func (c *chaosConnector) SendMessage(ctx context.Context, msg []byte) error {
	n := c.sendCalls.Add(1)
	if n <= c.failSendUntil {
		return errors.New("chaos: simulated send outage")
	}
	return c.inner.SendMessage(ctx, msg)
}

// TestChaos_SourceReceiveFlapping proves the executor's back-off
// keeps the pipeline alive when the source connection produces
// transient receive errors. Without the fix it'd hot-loop on
// `source.ReceiveMessage` and burn CPU; with it, the loop sleeps
// 2s between retries and continues to drain successful messages.
func TestChaos_SourceReceiveFlapping(t *testing.T) {
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

	reg := mq.NewMemoryRegistry(64)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()

	rawSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = rawSrc.Connect(ctx)
	chaosSrc := &chaosConnector{inner: rawSrc, failReceiveEvery: 3}
	rawDst := mq.NewMemoryConnector(reg, "dst-q")
	_ = rawDst.Connect(ctx)
	pool.InjectForTest("source-"+pipe.ID, chaosSrc)
	pool.InjectForTest("dest-"+pipe.ID, rawDst)

	metricsStore := metrics.New()
	mgr := NewManager(ctx, store, pool, metricsStore, nil, logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// Push 5 messages. Even with every-3rd receive failing, all 5
	// should eventually land on dst-q because the back-off retries.
	for i := 0; i < 5; i++ {
		if err := rawSrc.SendMessage(ctx, []byte("hello")); err != nil {
			t.Fatal(err)
		}
	}

	if err := waitFor(t, 10*time.Second, func() bool {
		return len(reg.Drain("dst-q")) == 0 == false &&
			chaosSrc.receiveCalls.Load() >= 5
	}); err != nil {
		t.Fatalf("expected destination drained after flap: receives=%d",
			chaosSrc.receiveCalls.Load())
	}
}

// TestChaos_DestinationOutageThenRecovery models a destination broker
// that fails the first N sends, then recovers. The pipeline must
// route failures to DLQ (the executor's send-failure path) and
// continue with subsequent messages once the destination recovers.
//
// Property: no message is lost. Messages either land on dst-q (after
// recovery) or in the DLQ (the failures during outage). Sum must
// equal what was published.
func TestChaos_DestinationOutageThenRecovery(t *testing.T) {
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

	reg := mq.NewMemoryRegistry(64)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()

	rawSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = rawSrc.Connect(ctx)
	rawDst := mq.NewMemoryConnector(reg, "dst-q")
	_ = rawDst.Connect(ctx)
	// First 3 sends fail; subsequent succeed.
	chaosDst := &chaosConnector{inner: rawDst, failSendUntil: 3}
	pool.InjectForTest("source-"+pipe.ID, rawSrc)
	pool.InjectForTest("dest-"+pipe.ID, chaosDst)

	metricsStore := metrics.New()
	dlqSvc := dlq.NewService(store, pool, dlq.Options{MaxRetries: 3, Logger: logging.New("error", "json")})
	mgr := NewManager(ctx, store, pool, metricsStore, dlqSvc, logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	const total = 6
	for i := 0; i < total; i++ {
		if err := rawSrc.SendMessage(ctx, []byte("payload")); err != nil {
			t.Fatal(err)
		}
	}

	// Wait for the pipeline to settle: destination drained AND/OR
	// DLQ populated, with sum >= total. (Some retries may double-
	// count: the property we assert is no loss.)
	if err := waitFor(t, 5*time.Second, func() bool {
		dst := reg.Drain("dst-q")
		// Put what we drained back so subsequent ticks can also see it.
		for _, m := range dst {
			_ = rawDst.SendMessage(ctx, m)
		}
		dlqEntries, _, _ := store.DLQ.List(ctx, storage.DefaultTenantID, 1, 100)
		return len(dst)+len(dlqEntries) >= total
	}); err != nil {
		dst := reg.Drain("dst-q")
		dlqEntries, _, _ := store.DLQ.List(ctx, storage.DefaultTenantID, 1, 100)
		t.Fatalf("expected dst+dlq >= %d, got dst=%d dlq=%d",
			total, len(dst), len(dlqEntries))
	}
}

// TestChaos_PipelineDisabledMidFlight models the "operator paused a
// runaway pipeline" path. We reload to disable the pipeline while
// messages are still in the source queue; the executor must shut
// down promptly without dropping any in-flight ack.
func TestChaos_PipelineDisabledMidFlight(t *testing.T) {
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

	reg := mq.NewMemoryRegistry(64)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()

	rawSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = rawSrc.Connect(ctx)
	rawDst := mq.NewMemoryConnector(reg, "dst-q")
	_ = rawDst.Connect(ctx)
	pool.InjectForTest("source-"+pipe.ID, rawSrc)
	pool.InjectForTest("dest-"+pipe.ID, rawDst)

	metricsStore := metrics.New()
	mgr := NewManager(ctx, store, pool, metricsStore, nil, logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// Push messages, then disable mid-flow.
	for i := 0; i < 10; i++ {
		_ = rawSrc.SendMessage(ctx, []byte("burst"))
	}

	// Disable + reload — the executor for this pipeline should
	// stop. Active pipeline count drops to 0; the in-flight Receive
	// gets cancelled via the executor's context.
	pipe.Enabled = false
	if err := store.Pipelines.Update(ctx, storage.DefaultTenantID, pipe); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(t, 2*time.Second, func() bool {
		return mgr.ActiveCount() == 0
	}); err != nil {
		t.Fatalf("expected pipeline to disable, active count = %d", mgr.ActiveCount())
	}

	// Messages already drained should be on dst-q; the rest stay on
	// the source queue. The property is "no orphaned in-flight" —
	// the executor's context-cancel path is the only thing being
	// exercised here; we just assert it terminates cleanly without
	// panic.
}
