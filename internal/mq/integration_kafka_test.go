//go:build integration

package mq

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/google/uuid"
)

// TestIntegration_Kafka_PublishConsume_RoundTrip drives a single
// message through a real Kafka broker. Covers the basic happy path:
// connect → publish → receive → commit. Without this, all of the
// Kafka-specific code (consumer group, offset commit, partition
// claim) is exercised only by sarama's own unit tests.
//
// Skipped by default. To run:
//
//	docker compose -f docker-compose.ci.yml up -d kafka
//	KAFKA_BROKERS=localhost:9092 \
//	  go test -tags integration -run TestIntegration_Kafka ./internal/mq/...
func TestIntegration_Kafka_PublishConsume_RoundTrip(t *testing.T) {
	brokers := os.Getenv("KAFKA_BROKERS")
	if brokers == "" {
		t.Skip("set KAFKA_BROKERS to run; e.g. KAFKA_BROKERS=localhost:9092")
	}

	topic := "mqctest-" + uuid.NewString()[:8]
	t.Cleanup(func() { deleteKafkaTopic(t, brokers, topic) })

	// Pre-create the topic so the consumer group doesn't race the
	// auto-create on first publish.
	createKafkaTopic(t, brokers, topic, 1)

	cfg := Config{
		Type:    TypeKafka,
		Brokers: strings.Split(brokers, ","),
		Topic:   topic,
		GroupID: "mqctest-" + uuid.NewString()[:8],
		// Tests publish messages and expect to receive them in the
		// same run, so the fresh consumer group must attach at
		// OffsetOldest. Production deployments leave this empty
		// for the upgrade-safe OffsetNewest default.
		InitialOffset: "oldest",
	}
	conn, err := New(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conn.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Disconnect() })

	// Receive runs in a goroutine because the consumer group has to
	// rebalance and claim the partition before any deliveries arrive;
	// the publisher and the consumer race naturally otherwise.
	body := []byte(`{"id":"kafka-roundtrip","value":42}`)
	got := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		msg, err := conn.ReceiveMessage(ctx)
		if err != nil {
			errCh <- err
			return
		}
		got <- msg
	}()

	// Give the consumer a moment to claim the partition. Without this
	// the publish races the consumer-group join and the message can
	// land before the group has committed any offset to track.
	time.Sleep(2 * time.Second)
	if err := conn.SendMessage(ctx, body); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case m := <-got:
		if string(m) != string(body) {
			t.Errorf("body mismatch: got %q want %q", m, body)
		}
		if err := conn.Commit(ctx); err != nil {
			t.Errorf("commit: %v", err)
		}
	case err := <-errCh:
		t.Fatalf("receive error: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

// TestIntegration_Kafka_AtLeastOnce_ResumesAfterRestart proves the
// durability contract end to end against a real broker: produce N
// messages, consume + commit only the first M, simulate a crash
// (Disconnect without committing the rest), reconnect with the SAME
// consumer group, and verify the broker redelivers the uncommitted
// tail. Loss == 0 is the property we care about; duplicates of the
// uncommitted tail are expected and acceptable (at-least-once).
//
// This is the test that would have caught the original
// "ConsumePartition + OffsetNewest" bug: that code path silently
// dropped every message produced while the consumer was offline.
func TestIntegration_Kafka_AtLeastOnce_ResumesAfterRestart(t *testing.T) {
	brokers := os.Getenv("KAFKA_BROKERS")
	if brokers == "" {
		t.Skip("set KAFKA_BROKERS to run; e.g. KAFKA_BROKERS=localhost:9092")
	}

	topic := "mqctest-resume-" + uuid.NewString()[:8]
	t.Cleanup(func() { deleteKafkaTopic(t, brokers, topic) })
	createKafkaTopic(t, brokers, topic, 1)

	groupID := "mqctest-group-" + uuid.NewString()[:8]
	cfg := Config{
		Type:    TypeKafka,
		Brokers: strings.Split(brokers, ","),
		Topic:   topic,
		GroupID: groupID,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Producer phase: publish 5 messages using a throwaway sender so
	// the consumer-group offsets aren't contaminated by sends.
	producer := newKafkaProducer(t, brokers)
	defer producer.Close()
	for i := 0; i < 5; i++ {
		body := []byte(fmt.Sprintf(`{"seq":%d}`, i))
		if _, _, err := producer.SendMessage(&sarama.ProducerMessage{
			Topic: topic,
			Value: sarama.ByteEncoder(body),
		}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// First consumer: receive + commit the first 3 messages, then
	// Disconnect WITHOUT committing the last 2.
	conn := newConnect(t, ctx, cfg)
	seen := readN(t, ctx, conn, 3)
	if len(seen) != 3 {
		t.Fatalf("first consumer: want 3 messages, got %d", len(seen))
	}
	if err := conn.Commit(ctx); err != nil {
		t.Fatalf("commit first batch: %v", err)
	}
	// Receive but do NOT commit — simulate "received the message but
	// the executor crashed before the downstream send completed."
	if _, err := conn.ReceiveMessage(ctx); err != nil {
		t.Fatalf("receive uncommitted msg 4: %v", err)
	}
	if _, err := conn.ReceiveMessage(ctx); err != nil {
		t.Fatalf("receive uncommitted msg 5: %v", err)
	}
	// Tear down WITHOUT committing the last two — the broker must
	// redeliver these on the next group session.
	_ = conn.Disconnect()

	// Second consumer: same group, expect the broker to deliver the
	// uncommitted tail. The committed prefix MUST NOT replay.
	// Sarama's offset-commit is async; wait briefly so the first
	// consumer's commits land before we re-join the group.
	time.Sleep(2 * time.Second)
	conn2 := newConnect(t, ctx, cfg)
	defer conn2.Disconnect()
	tail := readUntilQuiet(t, ctx, conn2, 5*time.Second)
	if len(tail) < 2 {
		t.Fatalf("expected redelivery of >= 2 uncommitted messages, got %d (bodies=%v)",
			len(tail), tail)
	}
	// And — crucially — none of the COMMITTED messages should be in
	// the tail. seq 0,1,2 were committed; seq 3,4 were not.
	for _, m := range tail {
		if strings.Contains(m, `"seq":0`) || strings.Contains(m, `"seq":1`) || strings.Contains(m, `"seq":2`) {
			t.Errorf("committed message replayed: %s", m)
		}
	}
	for _, m := range tail {
		_ = conn2.Commit(ctx)
		_ = m
	}
}

// ─── helpers ────────────────────────────────────────────────────────────

func newConnect(t *testing.T, ctx context.Context, cfg Config) Connector {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

// readN blocks until it has received exactly n messages or the test
// times out via ctx. Each Receive call returns the *next* delivery,
// so we just loop. The caller is responsible for Commit / Disconnect.
func readN(t *testing.T, ctx context.Context, c Connector, n int) []string {
	t.Helper()
	out := make([]string, 0, n)
	for len(out) < n {
		body, err := c.ReceiveMessage(ctx)
		if err != nil {
			t.Fatalf("ReceiveMessage: %v", err)
		}
		out = append(out, string(body))
	}
	return out
}

// readUntilQuiet reads as many messages as arrive within idle. Used
// to drain "however many are available" without knowing N up front.
func readUntilQuiet(t *testing.T, ctx context.Context, c Connector, idle time.Duration) []string {
	t.Helper()
	var out []string
	for {
		// Per-call deadline so the loop exits once the broker stops
		// sending; the outer ctx is the upper bound.
		callCtx, cancel := context.WithTimeout(ctx, idle)
		body, err := c.ReceiveMessage(callCtx)
		cancel()
		if err != nil {
			return out
		}
		out = append(out, string(body))
	}
}

func newKafkaProducer(t *testing.T, brokers string) sarama.SyncProducer {
	t.Helper()
	cfg := sarama.NewConfig()
	cfg.Producer.Return.Successes = true
	cfg.Producer.Return.Errors = true
	p, err := sarama.NewSyncProducer(strings.Split(brokers, ","), cfg)
	if err != nil {
		t.Fatalf("producer: %v", err)
	}
	return p
}

func createKafkaTopic(t *testing.T, brokers, topic string, partitions int32) {
	t.Helper()
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V2_5_0_0
	admin, err := sarama.NewClusterAdmin(strings.Split(brokers, ","), cfg)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	defer admin.Close()
	err = admin.CreateTopic(topic, &sarama.TopicDetail{
		NumPartitions:     partitions,
		ReplicationFactor: 1,
	}, false)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create topic: %v", err)
	}
}

func deleteKafkaTopic(t *testing.T, brokers, topic string) {
	t.Helper()
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V2_5_0_0
	admin, err := sarama.NewClusterAdmin(strings.Split(brokers, ","), cfg)
	if err != nil {
		t.Logf("admin cleanup: %v", err)
		return
	}
	defer admin.Close()
	if err := admin.DeleteTopic(topic); err != nil {
		t.Logf("delete topic %s: %v", topic, err)
	}
}
