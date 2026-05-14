// Package mq abstracts over IBM MQ, RabbitMQ, and Kafka behind a single
// Connector interface. IBM MQ is gated behind the `ibmmq` build tag because
// it requires CGO and the IBM client distribution.
package mq

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Type is the discriminator for connector kinds.
type Type string

const (
	TypeIBM      Type = "ibm"
	TypeRabbitMQ Type = "rabbitmq"
	TypeKafka    Type = "kafka"
)

// ErrUnsupported is returned when a connector is requested for a type that the
// current build does not support (e.g. IBM MQ in a non-ibmmq build).
var ErrUnsupported = errors.New("mq: unsupported connector type for this build")

// ParseType normalises a string into a Type. Unknown strings return an error.
func ParseType(s string) (Type, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ibm", "ibmmq", "ibm_mq":
		return TypeIBM, nil
	case "rabbit", "rabbitmq", "amqp":
		return TypeRabbitMQ, nil
	case "kafka":
		return TypeKafka, nil
	default:
		return "", fmt.Errorf("mq: unknown type %q", s)
	}
}

// Config carries the per-connector parameters. Fields not relevant to the
// chosen type are ignored.
type Config struct {
	Type Type

	// IBM MQ
	QueueManager  string
	ConnName      string
	Channel       string
	Username      string
	Password      string
	QueueName     string
	IBMRecvBuffer int // max bytes per receive (defaults to 4MB if 0)

	// RabbitMQ
	URL string // amqp[s]://user:pass@host/

	// Kafka
	Brokers []string
	Topic   string
}

// Connector is the unified interface every concrete MQ implementation must
// satisfy. Implementations are NOT required to be goroutine-safe for
// SendMessage/ReceiveMessage; the pool serialises access.
type Connector interface {
	Connect(ctx context.Context) error
	Disconnect() error
	SendMessage(ctx context.Context, message []byte) error
	// ReceiveMessage blocks until a message is available, ctx is cancelled,
	// or an error occurs.
	ReceiveMessage(ctx context.Context) ([]byte, error)
	// Ping is used by the pool to check liveness. It must be safe to call
	// concurrently with SendMessage / ReceiveMessage.
	Ping(ctx context.Context) error
}

// New is the factory. The platform-specific IBM constructor lives in
// connector_ibm_on.go (with the `ibmmq` build tag) and connector_ibm_off.go.
func New(cfg Config) (Connector, error) {
	switch cfg.Type {
	case TypeRabbitMQ:
		return newRabbitMQ(cfg), nil
	case TypeKafka:
		return newKafka(cfg), nil
	case TypeIBM:
		return newIBM(cfg)
	default:
		return nil, fmt.Errorf("mq: unknown type %q", cfg.Type)
	}
}
