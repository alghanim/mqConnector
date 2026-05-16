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

// TestIntegration_MQTT_PublishConsume drives a single message through a
// real MQTT broker.
//
// Skipped by default. To run:
//
//	docker run -d --rm -p 1883:1883 eclipse-mosquitto:2 mosquitto -c /mosquitto-no-auth.conf
//	MQTT_URL=tcp://localhost:1883 \
//	  go test -tags integration -run TestIntegration_MQTT ./internal/mq/...
//
// The test:
//   - Stands up two connectors against the same broker (one acts as
//     publisher, one as subscriber) with a per-run unique client id +
//     topic suffix so parallel runs don't trample each other.
//   - Subscribes, publishes one message, asserts it arrived intact, then
//     closes both connections.
//
// The point is to catch wire-format regressions paho silently absorbs
// in the in-process delivery path — broker reconnect, QoS-1 PUBACK
// timing, retained-message handling.
func TestIntegration_MQTT_PublishConsume(t *testing.T) {
	url := os.Getenv("MQTT_URL")
	if url == "" {
		t.Skip("set MQTT_URL to run; e.g. MQTT_URL=tcp://localhost:1883")
	}

	suffix := uuid.NewString()[:8]
	topic := "mqctest/" + suffix
	cfg := Config{
		Type:  TypeMQTT,
		URL:   url,
		Topic: topic,
		QoS:   1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	subCfg := cfg
	subCfg.ClientID = "sub-" + suffix
	sub, err := New(subCfg)
	if err != nil {
		t.Fatalf("new sub: %v", err)
	}
	if err := sub.Connect(ctx); err != nil {
		t.Fatalf("sub connect: %v", err)
	}
	defer sub.Disconnect()

	// The subscriber's session needs to be established before the
	// publisher fires — MQTT brokers don't replay messages published
	// before SUBSCRIBE on a non-retained topic. Paho's Connect is
	// synchronous against the broker, so by the time New returned we
	// have a session; one extra small pause covers the SUBSCRIBE round
	// trip in the connector's bootstrap.
	time.Sleep(150 * time.Millisecond)

	pubCfg := cfg
	pubCfg.ClientID = "pub-" + suffix
	pub, err := New(pubCfg)
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
