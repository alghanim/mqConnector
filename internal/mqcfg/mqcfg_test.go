package mqcfg

import (
	"testing"

	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

func TestFrom_RabbitMQ(t *testing.T) {
	c := &storage.Connection{
		Name: "r", Type: "rabbitmq",
		URL: "amqp://x", QueueName: "events",
	}
	got := From(c)
	if got.Type != mq.TypeRabbitMQ {
		t.Errorf("type = %s", got.Type)
	}
	if got.URL != "amqp://x" || got.QueueName != "events" {
		t.Errorf("fields lost: %+v", got)
	}
}

func TestFrom_KafkaBrokersSplit(t *testing.T) {
	c := &storage.Connection{
		Name: "k", Type: "kafka",
		Brokers: "h1:9092, h2:9092 ,h3:9092",
		Topic:   "t",
	}
	got := From(c)
	if got.Type != mq.TypeKafka {
		t.Errorf("type = %s", got.Type)
	}
	if len(got.Brokers) != 3 {
		t.Fatalf("brokers = %v", got.Brokers)
	}
	for i, want := range []string{"h1:9092", "h2:9092", "h3:9092"} {
		if got.Brokers[i] != want {
			t.Errorf("brokers[%d] = %q, want %q", i, got.Brokers[i], want)
		}
	}
}

func TestFrom_UnknownType_LeavesEmpty(t *testing.T) {
	c := &storage.Connection{Name: "x", Type: "nope"}
	if got := From(c); got.Type != "" {
		t.Errorf("expected empty Type for unknown, got %q", got.Type)
	}
}
