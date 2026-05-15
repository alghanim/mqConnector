//go:build integration && ibmmq

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mqConnector/internal/dlq"
	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// TestIntegration_IBMMQ_FilterRoundTrip drives a message end-to-end
// through a real IBM MQ queue manager.
//
// Run requirements:
//   - linux/amd64 host with the IBM MQ Redistributable Client (the
//     project's ibmmq_dist/) available, or run inside the
//     mqconnector:ibmmq image which bundles it.
//   - CGO_ENABLED=1 + the ibmmq build tag.
//   - A reachable IBM MQ queue manager with two queues the test can
//     drain. Configure via env:
//
//        IBMMQ_INTEGRATION=1
//        IBMMQ_QM=QM1
//        IBMMQ_CONN_NAME=localhost(1414)
//        IBMMQ_CHANNEL=DEV.APP.SVRCONN
//        IBMMQ_USER=app
//        IBMMQ_PASS=passw0rd
//        IBMMQ_QUEUE_SRC=DEV.QUEUE.1
//        IBMMQ_QUEUE_DST=DEV.QUEUE.2
//
//   - go test -tags 'integration ibmmq' -run TestIntegration_IBMMQ \
//        ./internal/pipeline/...
//
// The test stages one filter that strips "secret", pushes a JSON
// message via the IBM connector's SendMessage, then pulls from the
// destination queue and asserts `secret` is gone — same shape as the
// RabbitMQ integration test.
//
// IBM MQ doesn't auto-clean queues; the test logs the number of
// messages it drained from each so a stuck previous run is obvious.
func TestIntegration_IBMMQ_FilterRoundTrip(t *testing.T) {
	if os.Getenv("IBMMQ_INTEGRATION") == "" {
		t.Skip("set IBMMQ_INTEGRATION=1 (plus IBMMQ_QM, IBMMQ_CONN_NAME, IBMMQ_CHANNEL, IBMMQ_USER, IBMMQ_PASS, IBMMQ_QUEUE_SRC, IBMMQ_QUEUE_DST) to run")
	}
	required := []string{
		"IBMMQ_QM", "IBMMQ_CONN_NAME", "IBMMQ_CHANNEL",
		"IBMMQ_USER", "IBMMQ_PASS",
		"IBMMQ_QUEUE_SRC", "IBMMQ_QUEUE_DST",
	}
	envs := map[string]string{}
	missing := []string{}
	for _, k := range required {
		v := os.Getenv(k)
		if v == "" {
			missing = append(missing, k)
		}
		envs[k] = v
	}
	if len(missing) > 0 {
		t.Fatalf("missing env vars: %s", strings.Join(missing, ", "))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dsn := "file:" + filepath.Join(t.TempDir(), "ibm.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	store, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	src := &storage.Connection{
		Name: "ibm-src", Type: "ibm",
		QueueManager: envs["IBMMQ_QM"],
		ConnName:     envs["IBMMQ_CONN_NAME"],
		Channel:      envs["IBMMQ_CHANNEL"],
		Username:     envs["IBMMQ_USER"],
		Password:     envs["IBMMQ_PASS"],
		QueueName:    envs["IBMMQ_QUEUE_SRC"],
	}
	dst := &storage.Connection{
		Name: "ibm-dst", Type: "ibm",
		QueueManager: envs["IBMMQ_QM"],
		ConnName:     envs["IBMMQ_CONN_NAME"],
		Channel:      envs["IBMMQ_CHANNEL"],
		Username:     envs["IBMMQ_USER"],
		Password:     envs["IBMMQ_PASS"],
		QueueName:    envs["IBMMQ_QUEUE_DST"],
	}
	if err := store.Connections.Create(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := store.Connections.Create(ctx, dst); err != nil {
		t.Fatal(err)
	}

	pipe := &storage.Pipeline{
		Name: "integ-ibm", SourceID: src.ID, DestinationID: dst.ID, Enabled: true,
	}
	if err := store.Pipelines.Create(ctx, pipe); err != nil {
		t.Fatal(err)
	}
	stages := []*storage.Stage{
		{StageOrder: 1, StageType: "filter", StageConfig: `{"paths":["secret"]}`, Enabled: true},
	}
	if err := store.Stages.ReplaceForPipeline(ctx, pipe.ID, stages); err != nil {
		t.Fatal(err)
	}

	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	t.Cleanup(pool.Close)

	mgr := NewManager(ctx, store, pool,
		metrics.New(),
		dlq.NewService(store, pool, dlq.Options{MaxRetries: 3, Logger: logging.New("error", "json")}),
		logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	t.Cleanup(mgr.Stop)

	// Publish directly through the same connector the pipeline uses, so
	// we don't depend on an external tool like `amqsput`. Drain any
	// stale messages from the destination first so a previous run can't
	// contaminate the assertion.
	drainQueue(t, ctx, pool, dst, 100*time.Millisecond)

	publisher, releaseP, err := pool.Get(ctx, "test-pub", src)
	if err != nil {
		t.Fatalf("get publisher: %v", err)
	}
	body := []byte(`{"id":"order-ibm","secret":"hush","keep":1}`)
	if err := publisher.SendMessage(ctx, body); err != nil {
		releaseP()
		t.Fatalf("send: %v", err)
	}
	releaseP()

	// Read from the destination. Generous deadline because IBM MQ can
	// be sluggish under default channel settings.
	consumer, releaseC, err := pool.Get(ctx, "test-con", dst)
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}
	defer releaseC()

	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("no message arrived at destination within deadline")
		default:
		}
		out, err := consumer.ReceiveMessage(ctx)
		if err != nil {
			// Empty get / timeout — keep waiting.
			time.Sleep(200 * time.Millisecond)
			continue
		}
		got := string(out)
		if strings.Contains(got, `"secret"`) {
			t.Errorf("destination still carries secret: %s", got)
		}
		if !strings.Contains(got, `"keep"`) {
			t.Errorf("destination missing keep field: %s", got)
		}
		t.Logf("delivered: %s", got)
		return
	}
}

// drainQueue swallows whatever is sitting on the queue so the assertion
// in the main test isn't fooled by leftover messages from a previous run.
func drainQueue(t *testing.T, ctx context.Context, pool *mq.Pool, conn *storage.Connection, perAttempt time.Duration) {
	t.Helper()
	c, release, err := pool.Get(ctx, "drain-"+conn.ID, conn)
	if err != nil {
		t.Logf("drain: get connector: %v (ignoring)", err)
		return
	}
	defer release()
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	count := 0
	for {
		select {
		case <-dctx.Done():
			if count > 0 {
				t.Logf("drained %d stale messages from %s", count, conn.QueueName)
			}
			return
		default:
		}
		_, err := c.ReceiveMessage(dctx)
		if err != nil {
			time.Sleep(perAttempt)
			if dctx.Err() != nil {
				return
			}
			continue
		}
		count++
	}
}
