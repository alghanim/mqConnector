//go:build integration

package mq

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestIntegration_AMQP10_PublishConsume drives a single message through
// a real AMQP 1.0 broker. Works against ActiveMQ Artemis, ActiveMQ
// Classic, Solace, Azure Service Bus.
//
// Skipped by default. To run with Artemis:
//
//	docker run -d --rm -p 5672:5672 \
//	  -e AMQ_USER=mqc -e AMQ_PASSWORD=mqc-dev \
//	  apache/activemq-artemis:latest-alpine
//	AMQP10_URL=amqp://mqc:mqc-dev@localhost:5672 \
//	  go test -tags integration -run TestIntegration_AMQP10 \
//	  ./internal/mq/...
func TestIntegration_AMQP10_PublishConsume(t *testing.T) {
	url := os.Getenv("AMQP10_URL")
	if url == "" {
		t.Skip("set AMQP10_URL to run; e.g. AMQP10_URL=amqp://user:pass@localhost:5672")
	}

	suffix := uuid.NewString()[:8]
	address := "mqctest." + suffix
	cfg := Config{
		Type:  TypeAMQP10,
		URL:   url,
		Topic: address,
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
