//go:build integration

package mq

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// TestIntegration_NATS_CorePublishConsume drives a single message
// through a real NATS server (core NATS, not JetStream).
//
// Skipped by default. To run:
//
//	docker run -d --rm -p 4222:4222 nats:2
//	NATS_URL=nats://localhost:4222 \
//	  go test -tags integration -run TestIntegration_NATS ./internal/mq/...
func TestIntegration_NATS_CorePublishConsume(t *testing.T) {
	url := os.Getenv("NATS_URL")
	if url == "" {
		t.Skip("set NATS_URL to run; e.g. NATS_URL=nats://localhost:4222")
	}

	suffix := uuid.NewString()[:8]
	subject := "mqctest." + suffix
	cfg := Config{
		Type:  TypeNATS,
		URL:   url,
		Topic: subject,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sub, err := New(cfg)
	if err != nil {
		t.Fatalf("new sub: %v", err)
	}
	if err := sub.Connect(ctx); err != nil {
		t.Fatalf("sub connect: %v", err)
	}
	defer sub.Disconnect()

	// Core NATS doesn't queue messages for late subscribers. Give the
	// SUBSCRIBE a moment to land at the server before the publisher
	// fires.
	time.Sleep(100 * time.Millisecond)

	pub, err := New(cfg)
	if err != nil {
		t.Fatalf("new pub: %v", err)
	}
	if err := pub.Connect(ctx); err != nil {
		t.Fatalf("pub connect: %v", err)
	}
	defer pub.Disconnect()

	payload := []byte(fmt.Sprintf(`{"id":%q,"ts":%d}`, suffix, time.Now().UnixNano()))
	if err := pub.SendMessage(ctx, payload); err != nil {
		t.Fatalf("send: %v", err)
	}

	got, err := sub.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch:\n got  %s\n want %s", got, payload)
	}
}

// TestIntegration_NATS_JetStreamPullAck drives a message through
// JetStream — the durable, acked path. Catches differences in the
// connector's PullSubscribe / Ack flow vs core NATS.
//
// To run:
//
//	docker run -d --rm -p 4222:4222 nats:2 -js
//	# create the stream once via the nats CLI or `nats stream add`
//	NATS_URL=nats://localhost:4222 NATS_STREAM=MQCTEST \
//	  go test -tags integration -run TestIntegration_NATS_JetStream \
//	  ./internal/mq/...
func TestIntegration_NATS_JetStreamPullAck(t *testing.T) {
	url := os.Getenv("NATS_URL")
	stream := os.Getenv("NATS_STREAM")
	if url == "" || stream == "" {
		t.Skip("set NATS_URL and NATS_STREAM to run")
	}

	suffix := uuid.NewString()[:8]
	subject := "mqctest." + suffix
	cfg := Config{
		Type:         TypeNATS,
		URL:          url,
		Topic:        subject,
		StreamName:   stream,
		ConsumerName: "c-" + suffix,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Self-bootstrap: ensure the JetStream stream exists and covers
	// our test subject pattern. This lets CI bring up a vanilla NATS
	// container (no pre-seeded streams) and have the test wire its
	// own fixtures without a fragile out-of-band CLI step.
	if err := ensureJetStreamStream(url, stream, "mqctest.>"); err != nil {
		t.Fatalf("ensure stream: %v", err)
	}

	sub, err := New(cfg)
	if err != nil {
		t.Fatalf("new sub: %v", err)
	}
	if err := sub.Connect(ctx); err != nil {
		t.Fatalf("sub connect: %v", err)
	}
	defer sub.Disconnect()

	pub, err := New(cfg)
	if err != nil {
		t.Fatalf("new pub: %v", err)
	}
	if err := pub.Connect(ctx); err != nil {
		t.Fatalf("pub connect: %v", err)
	}
	defer pub.Disconnect()

	payload := []byte(fmt.Sprintf(`{"id":%q}`, suffix))
	if err := pub.SendMessage(ctx, payload); err != nil {
		t.Fatalf("send: %v", err)
	}

	got, err := sub.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch:\n got  %s\n want %s", got, payload)
	}
}

// ensureJetStreamStream creates the named stream with the given
// subject filter if it doesn't already exist. Idempotent — a second
// call with the same name is a no-op. Memory storage keeps CI fast
// and side-effect-free.
func ensureJetStreamStream(url, name, subjectPattern string) error {
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}
	_, err = js.StreamInfo(name)
	if err == nil {
		return nil
	}
	if !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("stream info: %w", err)
	}
	_, err = js.AddStream(&nats.StreamConfig{
		Name:      name,
		Subjects:  []string{subjectPattern},
		Storage:   nats.MemoryStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
	})
	if err != nil {
		return fmt.Errorf("add stream: %w", err)
	}
	return nil
}
