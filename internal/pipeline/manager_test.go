package pipeline

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

func openStore(t *testing.T) *storage.Store {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "mgr.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	s, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestManager_Reload_StartsEnabledPipelinesOnly(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()

	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x", QueueName: "src-q"}
	dst := &storage.Connection{Name: "dst", Type: "rabbitmq", URL: "amqp://x", QueueName: "dst-q"}
	_ = store.Connections.Create(ctx, src)
	_ = store.Connections.Create(ctx, dst)

	enabled := &storage.Pipeline{Name: "enabled-pipe", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	disabled := &storage.Pipeline{Name: "disabled-pipe", SourceID: src.ID, DestinationID: dst.ID, Enabled: false}
	_ = store.Pipelines.Create(ctx, enabled)
	_ = store.Pipelines.Create(ctx, disabled)

	// Preseat memory connectors in the pool so the executor doesn't try to
	// dial a real broker.
	reg := mq.NewMemoryRegistry(8)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()
	memSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = memSrc.Connect(ctx)
	memDst := mq.NewMemoryConnector(reg, "dst-q")
	_ = memDst.Connect(ctx)
	pool.InjectForTest("source-"+enabled.ID, memSrc)
	pool.InjectForTest("dest-"+enabled.ID, memDst)

	mgr := NewManager(ctx, store, pool, metrics.New(), nil, logging.New("error", "json"))
	started, err := mgr.Reload(ctx)
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if started != 1 {
		t.Errorf("started = %d, want 1 (only enabled pipeline)", started)
	}
	if mgr.ActiveCount() != 1 {
		t.Errorf("ActiveCount = %d, want 1", mgr.ActiveCount())
	}

	mgr.Stop()
	if _, err := mgr.Reload(ctx); err == nil {
		t.Error("Reload after Stop should error")
	}
}

func TestToMQConfig_HandlesBrokersList(t *testing.T) {
	c := &storage.Connection{
		Name: "k", Type: "kafka",
		Brokers: "host1:9092, host2:9092",
		Topic:   "events",
	}
	cfg := ToMQConfig(c)
	if cfg.Type != mq.TypeKafka {
		t.Errorf("type = %s", cfg.Type)
	}
	if len(cfg.Brokers) != 2 {
		t.Fatalf("brokers = %v", cfg.Brokers)
	}
	if cfg.Brokers[0] != "host1:9092" || cfg.Brokers[1] != "host2:9092" {
		t.Errorf("brokers parsed wrong: %v", cfg.Brokers)
	}
}

func TestToMQConfig_IBM(t *testing.T) {
	c := &storage.Connection{
		Name: "ibm-prod", Type: "ibm",
		QueueManager: "QM1", ConnName: "host(1414)", Channel: "DEV.SVRCONN",
		Username: "admin", Password: "p", QueueName: "DEV.Q.1",
	}
	cfg := ToMQConfig(c)
	if cfg.Type != mq.TypeIBM {
		t.Errorf("type = %s", cfg.Type)
	}
	if cfg.QueueManager != "QM1" || cfg.ConnName != "host(1414)" {
		t.Errorf("ibm fields lost: %+v", cfg)
	}
}
