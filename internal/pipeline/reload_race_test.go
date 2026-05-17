package pipeline

import (
	"context"
	"testing"
	"time"

	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// slowCancelConnector wraps a Connector but adds a small delay between
// "ctx is cancelled" and "ReceiveMessage returns". That mirrors the real
// RabbitMQ AMQP connector, which has to talk to the broker to cancel
// the consumer before its delivery channel closes — opening a window
// where a new Register can be wiped by the old Unregister.
//
// The MemoryConnector exits cancel essentially instantly, so the
// Reload-race repro needs this wrapper to be deterministic.
type slowCancelConnector struct {
	inner mq.Connector
	delay time.Duration
}

func (s *slowCancelConnector) Connect(ctx context.Context) error    { return s.inner.Connect(ctx) }
func (s *slowCancelConnector) Disconnect() error                    { return s.inner.Disconnect() }
func (s *slowCancelConnector) SendMessage(ctx context.Context, msg []byte) error {
	return s.inner.SendMessage(ctx, msg)
}
func (s *slowCancelConnector) Ping(ctx context.Context) error    { return s.inner.Ping(ctx) }
func (s *slowCancelConnector) Commit(ctx context.Context) error  { return s.inner.Commit(ctx) }
func (s *slowCancelConnector) Nack(ctx context.Context, requeue bool) error {
	return s.inner.Nack(ctx, requeue)
}
func (s *slowCancelConnector) ReceiveMessage(ctx context.Context) ([]byte, error) {
	msg, err := s.inner.ReceiveMessage(ctx)
	// Hold the receive call for `delay` past the cancel so the next
	// stage of the executor's unwind (Run's deferred Unregister) gets
	// pushed past the new executor's Register.
	if ctx.Err() != nil && s.delay > 0 {
		time.Sleep(s.delay)
	}
	return msg, err
}

// TestManager_Reload_DoubleReload_PreservesMetricsRegistration is the
// repro for the live-deploy bug found at 03:12:53 in dev: after the
// gitops handler triggered a Reload, /api/health reported
// active_pipelines=0 even though no pipeline was logging "stopped".
//
// Root cause: when Reload restarts a pipeline, the OLD executor's Run()
// returns asynchronously and its deferred Metrics.Unregister(p.ID)
// races against the NEW executor's Metrics.Register(p.ID, …). If the
// old's Unregister fires LAST, it deletes the entry the new executor
// just inserted — leaving the pipeline running but invisible to /api/
// metrics and /api/health.
//
// Repro: spin up a pipeline, Reload, Reload again, and assert the
// metrics store still has the pipeline registered. Without the fix
// this fails reliably under the race because Reload tears down + spins
// up in tight succession.
func TestManager_Reload_DoubleReload_PreservesMetricsRegistration(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()

	src := &storage.Connection{Name: "race-src", Type: "rabbitmq", URL: "amqp://x", QueueName: "src-q"}
	dst := &storage.Connection{Name: "race-dst", Type: "rabbitmq", URL: "amqp://x", QueueName: "dst-q"}
	if err := store.Connections.Create(ctx, storage.DefaultTenantID, src); err != nil {
		t.Fatalf("create src: %v", err)
	}
	if err := store.Connections.Create(ctx, storage.DefaultTenantID, dst); err != nil {
		t.Fatalf("create dst: %v", err)
	}
	pipe := &storage.Pipeline{Name: "race-pipe", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	if err := store.Pipelines.Create(ctx, storage.DefaultTenantID, pipe); err != nil {
		t.Fatalf("create pipe: %v", err)
	}

	reg := mq.NewMemoryRegistry(8)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()
	// Wrap the source connector with a 50ms cancel delay — long enough
	// that without the Reload fix, the new executor's Register lands
	// before the old's Unregister and the race is observable.
	memSrc := &slowCancelConnector{
		inner: mq.NewMemoryConnector(reg, "src-q"),
		delay: 50 * time.Millisecond,
	}
	_ = memSrc.Connect(ctx)
	memDst := mq.NewMemoryConnector(reg, "dst-q")
	_ = memDst.Connect(ctx)
	pool.InjectForTest("source-"+pipe.ID, memSrc)
	pool.InjectForTest("dest-"+pipe.ID, memDst)

	metricsStore := metrics.New()
	mgr := NewManager(ctx, store, pool, metricsStore, nil, logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatalf("first Reload: %v", err)
	}
	// Reload returns before the executor goroutine schedules + runs
	// Register. Poll for up to 1s so we don't flake on slow CI.
	if err := waitFor(t, time.Second, func() bool {
		return len(metricsStore.Snapshot()) == 1
	}); err != nil {
		t.Fatalf("after first Reload: metrics never reached 1 entry (got %d): %v",
			len(metricsStore.Snapshot()), err)
	}

	// Rapid-fire reloads. Each one cancels the running executor and
	// starts a fresh one. Before the fix, the OLD executor's defer
	// Unregister(p.ID) could fire AFTER the NEW executor's
	// Register(p.ID) and leave the metrics map empty. Doing 20
	// reloads back-to-back gives the scheduler plenty of chances to
	// land in that order; the fix (Reload waits for handle.done
	// before spawning the replacement) closes the window.
	for i := 0; i < 20; i++ {
		if _, err := mgr.Reload(ctx); err != nil {
			t.Fatalf("Reload #%d: %v", i+2, err)
		}
		// Reload returns before the new executor's goroutine schedules
		// and calls Register. Poll briefly. If we land outside the poll
		// window with 0 entries, the OLD executor's defer wiped the
		// NEW Register — that's the race the fix closes.
		if err := waitFor(t, 500*time.Millisecond, func() bool {
			return len(metricsStore.Snapshot()) == 1
		}); err != nil {
			t.Fatalf("after Reload #%d: metrics has %d entries, want 1 "+
				"(old executor's defer wiped the new registration): %v",
				i+2, len(metricsStore.Snapshot()), err)
		}
	}

	mgr.Stop()
}
