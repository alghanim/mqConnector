//go:build integration

package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"mqConnector/internal/dlq"
	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// TestIntegration_RabbitMQ_FilterRoundTrip drives a message end-to-end
// through a real RabbitMQ broker.
//
// Skipped by default. To run:
//
//	docker compose up -d rabbitmq
//	RABBIT_URL=amqp://mqc:mqc-dev@localhost:5672 \
//	  go test -tags integration -run TestIntegration_RabbitMQ \
//	  ./internal/pipeline/...
//
// The test:
//   - Declares two transient queues (src.* / dst.* with a unique suffix so
//     parallel runs don't collide and a stuck previous run doesn't leak
//     into this one).
//   - Stands up the same Manager + storage + pool the binary uses, with
//     ONE filter stage that strips "secret".
//   - Publishes 3 messages directly via AMQP to the source queue, then
//     reads from the destination queue and asserts each one arrived
//     with "secret" stripped.
//
// The point of the test is to catch wire-format regressions that the
// in-memory connector can't see — message-property handling, ack
// semantics, etc.
func TestIntegration_RabbitMQ_FilterRoundTrip(t *testing.T) {
	url := os.Getenv("RABBIT_URL")
	if url == "" {
		t.Skip("set RABBIT_URL to run; e.g. RABBIT_URL=amqp://mqc:mqc-dev@localhost:5672")
	}

	// Unique queue suffix so concurrent test runs don't see each other's
	// messages. RabbitMQ auto-deletes are not used so the pipeline can
	// still drain them after the connector closes between calls.
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	srcQ := "mqc-test-src-" + suffix
	dstQ := "mqc-test-dst-" + suffix
	t.Cleanup(func() { cleanupRabbitQueues(t, url, srcQ, dstQ) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Storage + connections + a single-filter pipeline.
	dsn := "file:" + filepath.Join(t.TempDir(), "rab.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	store, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: url, QueueName: srcQ}
	dst := &storage.Connection{Name: "dst", Type: "rabbitmq", URL: url, QueueName: dstQ}
	if err := store.Connections.Create(ctx, storage.DefaultTenantID, src); err != nil {
		t.Fatal(err)
	}
	if err := store.Connections.Create(ctx, storage.DefaultTenantID, dst); err != nil {
		t.Fatal(err)
	}
	pipe := &storage.Pipeline{Name: "integ", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	if err := store.Pipelines.Create(ctx, storage.DefaultTenantID, pipe); err != nil {
		t.Fatal(err)
	}
	stages := []*storage.Stage{
		{StageOrder: 1, StageType: "filter", StageConfig: `{"paths":["secret"]}`, Enabled: true},
	}
	if err := store.Stages.ReplaceForPipeline(ctx, storage.DefaultTenantID, pipe.ID, stages); err != nil {
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

	// Publish messages directly via AMQP. Declare both queues up front
	// (durable=true, matching what RabbitMQConnector.Connect declares) so
	// the consumer side doesn't race the pipeline's lazy QueueDeclare.
	//
	// We intentionally do NOT `defer conn.Close()`: amqp091's channel
	// cancel can block under load and we don't want test-process exit to
	// wait on broker round-trips. The test process tear-down releases the
	// fd cleanly enough for CI purposes.
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("amqp dial: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("amqp channel: %v", err)
	}
	for _, q := range []string{srcQ, dstQ} {
		if _, err := ch.QueueDeclare(q, true, false, false, false, nil); err != nil {
			t.Fatalf("declare %s: %v", q, err)
		}
	}

	// Open the destination consumer BEFORE publishing, on a separate
	// channel from the publisher. Mixing publish + consume on one channel
	// can starve deliveries in busy brokers, and we want the consumer
	// ready so we don't race the pipeline draining src.
	consumeCh, err := conn.Channel()
	if err != nil {
		t.Fatalf("amqp consume channel: %v", err)
	}
	// Same reasoning as above: no defer Close on this channel either.
	dsts, err := consumeCh.Consume(dstQ, "", true, false, false, false, nil)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}

	for _, body := range []string{
		`{"id":"a","secret":"s1","kept":1}`,
		`{"id":"b","secret":"s2","kept":2}`,
		`{"id":"c","secret":"s3","kept":3}`,
	} {
		if err := ch.PublishWithContext(ctx, "", srcQ, false, false, amqp.Publishing{
			ContentType:  "application/json",
			Body:         []byte(body),
			DeliveryMode: amqp.Persistent,
		}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	// Drain dst. Expect all three messages: the rabbit connector now
	// uses a single long-lived consumer with manual ack, so messages
	// flow in order at the rate the executor processes them.
	got := 0
	deadline := time.After(15 * time.Second)
	for got < 3 {
		select {
		case d, ok := <-dsts:
			if !ok {
				t.Fatal("dst consumer closed")
			}
			body := string(d.Body)
			if contains(body, `"secret"`) {
				t.Errorf("message %s still carries secret: %s", d.MessageId, body)
			}
			got++
		case <-deadline:
			t.Fatalf("timed out waiting for messages; got %d/3", got)
		}
	}
	t.Logf("delivered %d/3 messages within the deadline", got)
}

// contains avoids pulling strings into the import set for one call.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// cleanupRabbitQueues best-effort deletes the test queues. Errors are logged
// but not fatal — the test has already exercised the codepath.
func cleanupRabbitQueues(t *testing.T, url string, queues ...string) {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Logf("cleanup dial: %v", err)
		return
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Logf("cleanup channel: %v", err)
		return
	}
	defer ch.Close()
	for _, q := range queues {
		if _, err := ch.QueueDelete(q, false, false, true); err != nil {
			t.Logf("cleanup delete %s: %v", q, err)
		}
	}
}
