package mq

import (
	"testing"
)

func TestNewMQConnector_IBM(t *testing.T) {
	config := map[string]string{
		"queueManager": "QM1",
		"connName":     "localhost(1414)",
		"channel":      "DEV.ADMIN.SVRCONN",
		"user":         "admin",
		"password":     "password",
		"queueName":    "DEV.QUEUE.1",
	}

	connector, err := NewMQConnector(IBM, config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ibm, ok := connector.(*IBMMQConnector)
	if !ok {
		t.Fatal("expected IBMMQConnector type")
	}
	if ibm.queueManager != "QM1" {
		t.Errorf("expected QM1, got %s", ibm.queueManager)
	}
	if ibm.queueName != "DEV.QUEUE.1" {
		t.Errorf("expected DEV.QUEUE.1, got %s", ibm.queueName)
	}
}

func TestNewMQConnector_RabbitMQ(t *testing.T) {
	config := map[string]string{
		"url":       "amqp://guest:guest@localhost:5672/",
		"queueName": "testQueue",
	}

	connector, err := NewMQConnector(RabbitMQ, config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rabbit, ok := connector.(*RabbitMQConnector)
	if !ok {
		t.Fatal("expected RabbitMQConnector type")
	}
	if rabbit.url != "amqp://guest:guest@localhost:5672/" {
		t.Errorf("unexpected url: %s", rabbit.url)
	}
}

func TestNewMQConnector_Kafka(t *testing.T) {
	config := map[string]string{
		"brokers": "localhost:9092",
		"topic":   "testTopic",
	}

	connector, err := NewMQConnector(Kafka, config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kafka, ok := connector.(*KafkaConnector)
	if !ok {
		t.Fatal("expected KafkaConnector type")
	}
	if kafka.topic != "testTopic" {
		t.Errorf("expected testTopic, got %s", kafka.topic)
	}
}

func TestNewMQConnector_Unsupported(t *testing.T) {
	_, err := NewMQConnector(QueueType(99), nil)
	if err == nil {
		t.Error("expected error for unsupported queue type")
	}
}

func TestGetQueueType(t *testing.T) {
	tests := []struct {
		input    string
		expected QueueType
	}{
		{"IBM", IBM},
		{"RabbitMQ", RabbitMQ},
		{"Kafka", Kafka},
	}

	for _, tt := range tests {
		result := GetQueueType(tt.input)
		if result != tt.expected {
			t.Errorf("GetQueueType(%s) = %d, want %d", tt.input, result, tt.expected)
		}
	}
}

func TestConnectorPool_GetAndRelease(t *testing.T) {
	pool := &ConnectorPool{}

	// Release nonexistent should not panic
	pool.Release("nonexistent")

	// ReleaseAll on empty should not panic
	pool.ReleaseAll()
}
