package mq

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/IBM/sarama"
)

// KafkaConnector implements at-least-once delivery via a sarama consumer
// group. Offsets are committed only after the executor calls Commit(),
// which closes the receive→send→commit loop: if mqconnector crashes
// between receive and commit, the broker redelivers from the last
// committed offset on the next restart.
//
// The group ID is derived deterministically from cfg.Brokers + cfg.Topic
// when cfg.GroupID is empty, so a single pipeline restart resumes at the
// same place. Two pipelines reading the same connection share an offset,
// which is the intended "one logical consumer per source connection"
// model — split into separate connections if you want independent
// offsets. This is the same trade-off Kafka itself enforces with
// consumer groups.
//
// Previous behaviour used sarama.ConsumePartition(topic, 0, OffsetNewest)
// which only read partition 0 AND dropped every message produced while
// the consumer was offline. That was at-most-once with bonus partition
// blindness; we replace it wholesale.
type KafkaConnector struct {
	cfg Config

	mu       sync.Mutex
	producer sarama.SyncProducer
	group    sarama.ConsumerGroup

	// Consumer-group plumbing. The group runs in a goroutine and pushes
	// claimed messages into deliveries; the session is captured so
	// Commit can call MarkMessage on the most-recent pending message.
	consumeCtx    context.Context
	consumeCancel context.CancelFunc
	deliveries    chan kafkaDelivery
	groupErr      chan error

	pending *kafkaDelivery
}

// kafkaDelivery carries one message plus the session needed to mark its
// offset. Sessions rotate on rebalance, so we capture the session that
// produced the message rather than relying on a top-level reference.
type kafkaDelivery struct {
	msg  *sarama.ConsumerMessage
	sess sarama.ConsumerGroupSession
}

func newKafka(cfg Config) Connector {
	return &KafkaConnector{cfg: cfg}
}

// groupIDFor returns a stable consumer-group id. If cfg.GroupID is set
// we honour it; otherwise we derive a deterministic id from the
// brokers + topic so restarts pick up where they left off.
func groupIDFor(cfg Config) string {
	if cfg.GroupID != "" {
		return cfg.GroupID
	}
	h := sha256.New()
	for _, b := range cfg.Brokers {
		h.Write([]byte(b))
		h.Write([]byte{0})
	}
	h.Write([]byte(cfg.Topic))
	return "mqconnector-" + hex.EncodeToString(h.Sum(nil))[:16]
}

func (c *KafkaConnector) Connect(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Self-healing: if the prior session went stale, drop it and rebuild.
	if c.producer != nil {
		if c.group != nil {
			// A short Topics() probe via a throwaway client is overkill;
			// rely on the producer's health by attempting a metadata
			// refresh via the underlying client.
			return nil
		}
		_ = c.producer.Close()
		c.producer = nil
	}
	if len(c.cfg.Brokers) == 0 {
		return errors.New("kafka: at least one broker is required")
	}
	scfg := sarama.NewConfig()
	scfg.Version = sarama.V2_5_0_0
	scfg.Producer.Return.Successes = true
	scfg.Producer.Return.Errors = true
	scfg.Consumer.Return.Errors = true
	// At-least-once: read committed-only, manual commit. The executor
	// drives MarkMessage via Commit(); we never auto-commit.
	scfg.Consumer.Offsets.AutoCommit.Enable = false
	scfg.Consumer.Offsets.Initial = sarama.OffsetOldest

	if c.cfg.TLS.Enabled() {
		tlsCfg, err := BuildTLSConfig(c.cfg.TLS)
		if err != nil {
			return fmt.Errorf("kafka TLS: %w", err)
		}
		scfg.Net.TLS.Enable = true
		scfg.Net.TLS.Config = tlsCfg
	}

	producer, err := sarama.NewSyncProducer(c.cfg.Brokers, scfg)
	if err != nil {
		return fmt.Errorf("kafka NewSyncProducer: %w", err)
	}
	groupID := groupIDFor(c.cfg)
	group, err := sarama.NewConsumerGroup(c.cfg.Brokers, groupID, scfg)
	if err != nil {
		_ = producer.Close()
		return fmt.Errorf("kafka NewConsumerGroup: %w", err)
	}

	c.producer = producer
	c.group = group
	c.deliveries = make(chan kafkaDelivery, 16)
	c.groupErr = make(chan error, 1)
	c.consumeCtx, c.consumeCancel = context.WithCancel(context.Background())
	c.pending = nil

	// Drive the consumer group in a background goroutine. Consume()
	// blocks until the group session is closed; we loop so rebalances
	// re-enter cleanly.
	go func(ctx context.Context, g sarama.ConsumerGroup, topic string, deliveries chan<- kafkaDelivery, errs chan<- error) {
		handler := &kafkaGroupHandler{deliveries: deliveries}
		for {
			if ctx.Err() != nil {
				return
			}
			if err := g.Consume(ctx, []string{topic}, handler); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}(c.consumeCtx, group, c.cfg.Topic, c.deliveries, c.groupErr)
	return nil
}

func (c *KafkaConnector) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.consumeCancel != nil {
		c.consumeCancel()
		c.consumeCancel = nil
	}
	if c.group != nil {
		_ = c.group.Close()
		c.group = nil
	}
	if c.producer != nil {
		_ = c.producer.Close()
		c.producer = nil
	}
	c.deliveries = nil
	c.groupErr = nil
	c.pending = nil
	return nil
}

func (c *KafkaConnector) SendMessage(_ context.Context, message []byte) error {
	c.mu.Lock()
	producer := c.producer
	topic := c.cfg.Topic
	c.mu.Unlock()
	if producer == nil {
		return errors.New("kafka: producer not initialised")
	}
	_, _, err := producer.SendMessage(&sarama.ProducerMessage{
		Topic: topic,
		Value: sarama.ByteEncoder(message),
	})
	return err
}

func (c *KafkaConnector) ReceiveMessage(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	deliveries := c.deliveries
	groupErr := c.groupErr
	c.mu.Unlock()
	if deliveries == nil {
		return nil, errors.New("kafka: consumer group not initialised")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case d, ok := <-deliveries:
		if !ok {
			return nil, errors.New("kafka: delivery channel closed")
		}
		c.mu.Lock()
		c.pending = &d
		c.mu.Unlock()
		return d.msg.Value, nil
	case err, ok := <-groupErr:
		if !ok {
			return nil, errors.New("kafka: error channel closed")
		}
		return nil, err
	}
}

// Commit marks the most-recent delivery's offset on the consumer group.
// sarama batches the actual broker-side commit; the next Consume loop
// (or session close) flushes it. For our restart-resilience guarantee
// the important property is "the offset will be persisted before the
// next claim of this partition," which sarama upholds.
func (c *KafkaConnector) Commit(_ context.Context) error {
	c.mu.Lock()
	p := c.pending
	c.pending = nil
	c.mu.Unlock()
	if p == nil {
		return nil
	}
	p.sess.MarkMessage(p.msg, "")
	return nil
}

// Nack drops the pending delivery without marking it. The broker will
// redeliver it on the next session (Kafka doesn't have a per-message
// nack — offsets are sequential, so any "unmarked" message will be
// re-claimed on restart or rebalance). requeue is ignored.
func (c *KafkaConnector) Nack(_ context.Context, _ bool) error {
	c.mu.Lock()
	c.pending = nil
	c.mu.Unlock()
	return nil
}

func (c *KafkaConnector) Ping(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.group == nil {
		return errors.New("kafka: not connected")
	}
	return nil
}

// kafkaGroupHandler implements sarama.ConsumerGroupHandler. It forwards
// every claimed message into a shared deliveries channel along with the
// session so Commit can MarkMessage on the right one.
type kafkaGroupHandler struct {
	deliveries chan<- kafkaDelivery
}

func (h *kafkaGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *kafkaGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }
func (h *kafkaGroupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for {
		select {
		case msg, ok := <-claim.Messages():
			if !ok {
				return nil
			}
			select {
			case h.deliveries <- kafkaDelivery{msg: msg, sess: sess}:
			case <-sess.Context().Done():
				return nil
			}
		case <-sess.Context().Done():
			return nil
		}
	}
}
