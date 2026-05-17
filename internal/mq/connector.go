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
	TypeMQTT     Type = "mqtt"
	TypeNATS     Type = "nats"
	TypeAMQP10   Type = "amqp10"
)

// Sentinel errors returned across the package.
var (
	// ErrUnsupported is returned when a connector is requested for a type
	// the current build does not support (e.g. IBM MQ in a non-ibmmq build).
	ErrUnsupported = errors.New("mq: unsupported connector type for this build")
	// ErrNotConnected is returned when an operation is invoked on a Connector
	// before Connect succeeded, or after Disconnect.
	ErrNotConnected = errors.New("mq: not connected")
)

// ParseType normalises a string into a Type. Unknown strings return an error.
func ParseType(s string) (Type, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ibm", "ibmmq", "ibm_mq":
		return TypeIBM, nil
	// "amqp" is intentionally kept on RabbitMQ for backward compat —
	// AMQP 1.0 is a *different protocol* despite the shared name, so
	// we use explicit "amqp10" / "amqp1.0" aliases for it below.
	case "rabbit", "rabbitmq", "amqp":
		return TypeRabbitMQ, nil
	case "kafka":
		return TypeKafka, nil
	case "mqtt":
		return TypeMQTT, nil
	case "nats", "jetstream":
		return TypeNATS, nil
	case "amqp10", "amqp1", "amqp1.0":
		return TypeAMQP10, nil
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

	// RabbitMQ / NATS / MQTT / AMQP 1.0
	URL string // amqp[s]://user:pass@host/ | nats://… | tcp://… | amqps://…

	// Kafka
	Brokers []string
	Topic   string
	// GroupID overrides the auto-derived consumer-group id. Leave empty
	// to let the connector hash brokers+topic into a stable group; set
	// explicitly when two pipelines on the same Kafka source need
	// independent offsets.
	GroupID string

	// MQTT
	ClientID string // unique per broker; auto-generated if blank
	QoS      int    // 0 | 1 | 2 — defaults to 0

	// NATS / JetStream
	StreamName   string // bound stream for JetStream subscribe
	ConsumerName string // durable consumer (recommended)

	// TLS / mTLS to the broker. TLS is enabled if any of the
	// CA/Cert/Key paths are set, or if InsecureSkipVerify is true (the
	// latter is dev-only — production deploys leave it false). Loaded
	// at dial time so a rotated cert takes effect on the next reconnect.
	TLS TLSConfig
}

// TLSConfig wraps the per-connection TLS material. Loaded into a
// *tls.Config by BuildTLSConfig at dial time.
type TLSConfig struct {
	CAFile             string
	CertFile           string
	KeyFile            string
	InsecureSkipVerify bool
}

// Enabled reports whether the operator configured any TLS knob. The
// dialers check this before constructing a tls.Config.
func (t TLSConfig) Enabled() bool {
	return t.CAFile != "" || t.CertFile != "" || t.KeyFile != "" || t.InsecureSkipVerify
}

// Connector is the unified interface every concrete MQ implementation must
// satisfy. Implementations are NOT required to be goroutine-safe for
// SendMessage/ReceiveMessage; the pool serialises access.
type Connector interface {
	Connect(ctx context.Context) error
	Disconnect() error
	SendMessage(ctx context.Context, message []byte) error
	// ReceiveMessage blocks until a message is available, ctx is cancelled,
	// or an error occurs. The connector tracks the delivery internally so
	// the caller can later acknowledge or nack it via Commit / Nack.
	ReceiveMessage(ctx context.Context) ([]byte, error)
	// Commit acknowledges the most-recent delivery returned by
	// ReceiveMessage. This is the at-least-once guarantee: by holding
	// the ack until after the pipeline's downstream send (or DLQ push)
	// has succeeded, a crash mid-flight causes the source broker to
	// redeliver instead of silently dropping the message.
	//
	// Brokers that don't support explicit acknowledgement (NATS Core,
	// MQTT QoS 0) implement this as a no-op. Brokers that auto-ack at
	// the protocol level (paho MQTT with QoS 1/2 + auto-ack callback)
	// also no-op. Brokers that need an explicit commit hook implement
	// it here: RabbitMQ d.Ack, Kafka offset MarkMessage, JetStream
	// msg.Ack, AMQP 1.0 receiver.AcceptMessage, IBM MQ MQCMIT.
	Commit(ctx context.Context) error
	// Nack rolls back the most-recent delivery. Optional — only brokers
	// that distinguish "ack me / requeue me" implement it meaningfully;
	// the rest fall back to no-op. requeue=true asks the broker to put
	// the message back on the head of the queue; false routes it to
	// the broker's own DLX / poison-message handling where supported.
	Nack(ctx context.Context, requeue bool) error
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
	case TypeMQTT:
		return newMQTT(cfg), nil
	case TypeNATS:
		return newNATS(cfg), nil
	case TypeAMQP10:
		return newAMQP10(cfg), nil
	default:
		return nil, fmt.Errorf("mq: unknown type %q", cfg.Type)
	}
}
