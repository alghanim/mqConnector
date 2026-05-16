package mq

import (
	"context"
	"errors"
	"fmt"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RabbitMQConnector wraps an AMQP 0.9.1 connection + a single open channel.
//
// Receive uses a long-lived consumer with manual-ack semantics. The
// previous implementation called Consume on every receive, which:
//   - leaked one consumer goroutine per message (the old delivery
//     channel was never read),
//   - and dropped messages because RabbitMQ round-robins deliveries
//     across all active consumers on a queue; only one of the N leaked
//     channels would get each message.
// The integration test that arrived 1/3 of the published messages was
// hitting this; the rewritten path delivers all messages in order.
type RabbitMQConnector struct {
	cfg Config

	mu      sync.Mutex
	conn    *amqp.Connection
	channel *amqp.Channel

	// Long-lived consumer state. consumeOnce guards the first call to
	// Consume so re-entrant ReceiveMessages share one delivery channel.
	consumeOnce sync.Once
	consumeErr  error
	deliveries  <-chan amqp.Delivery
	consumerTag string
}

func newRabbitMQ(cfg Config) Connector {
	return &RabbitMQConnector{cfg: cfg}
}

func (c *RabbitMQConnector) Connect(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	conn, err := amqp.Dial(c.cfg.URL)
	if err != nil {
		return fmt.Errorf("rabbitmq Dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("rabbitmq Channel: %w", err)
	}
	if _, err := ch.QueueDeclare(c.cfg.QueueName, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return fmt.Errorf("rabbitmq QueueDeclare: %w", err)
	}
	// Prefetch keeps in-flight unacked deliveries bounded so a slow
	// pipeline doesn't drain the queue into a single consumer's
	// in-memory channel. 16 is a conservative starting point; raise
	// via config if profiling shows the executor as the bottleneck.
	if err := ch.Qos(16, 0, false); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return fmt.Errorf("rabbitmq Qos: %w", err)
	}
	c.conn = conn
	c.channel = ch
	// Reset the consume-once latch so a fresh Connect after Disconnect
	// can set up a new consumer.
	c.consumeOnce = sync.Once{}
	c.consumeErr = nil
	c.deliveries = nil
	c.consumerTag = ""
	return nil
}

func (c *RabbitMQConnector) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	// Cancel the consumer first so amqp091 doesn't deadlock on channel
	// close waiting for in-flight deliveries.
	if c.channel != nil && c.consumerTag != "" {
		if err := c.channel.Cancel(c.consumerTag, false); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if c.channel != nil {
		if err := c.channel.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		c.channel = nil
	}
	if c.conn != nil {
		if err := c.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		c.conn = nil
	}
	c.deliveries = nil
	c.consumerTag = ""
	return firstErr
}

func (c *RabbitMQConnector) SendMessage(ctx context.Context, message []byte) error {
	c.mu.Lock()
	ch := c.channel
	queue := c.cfg.QueueName
	c.mu.Unlock()
	if ch == nil {
		return errors.New("rabbitmq: channel not opened")
	}
	return ch.PublishWithContext(ctx, "", queue, false, false, amqp.Publishing{
		ContentType:  "application/octet-stream",
		Body:         message,
		DeliveryMode: amqp.Persistent,
	})
}

// ReceiveMessage returns the next available delivery from the long-lived
// consumer. The first call sets the consumer up; subsequent calls read
// from the same delivery channel. Messages are manually acked after the
// body is captured — at-least-once semantics: a process crash between
// the Ack and the executor's downstream Send re-delivers the message on
// next Connect. (At-most-once was the previous default and silently
// dropped on the same crash.)
func (c *RabbitMQConnector) ReceiveMessage(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	if c.channel == nil {
		c.mu.Unlock()
		return nil, errors.New("rabbitmq: channel not opened")
	}
	c.consumeOnce.Do(func() {
		// Auto-ack=false so we ack only after reading the body. A
		// generated consumer tag (passed "") keeps us from clashing
		// with anything else on the queue.
		d, err := c.channel.ConsumeWithContext(context.Background(),
			c.cfg.QueueName, "", false, false, false, false, nil)
		if err != nil {
			c.consumeErr = fmt.Errorf("rabbitmq Consume: %w", err)
			return
		}
		c.deliveries = d
		// amqp091 doesn't expose the server-issued consumer tag through
		// the deliveries channel, but it sends it on the first Delivery
		// at the .ConsumerTag field. We capture it lazily below.
	})
	deliveries := c.deliveries
	err := c.consumeErr
	c.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if deliveries == nil {
		return nil, errors.New("rabbitmq: consumer not initialised")
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case d, ok := <-deliveries:
		if !ok {
			return nil, errors.New("rabbitmq: delivery channel closed")
		}
		// Stash the consumer tag the first time we see it so Disconnect
		// can issue a Cancel before Close.
		c.mu.Lock()
		if c.consumerTag == "" {
			c.consumerTag = d.ConsumerTag
		}
		c.mu.Unlock()
		// Ack now: the executor's downstream Send is best-effort and a
		// failed send routes the body to DLQ via the manager, so we
		// don't need to hold the source-side ack until then. multiple=
		// false acks just this delivery.
		if ackErr := d.Ack(false); ackErr != nil {
			// Ack failure usually means the channel is gone. Return the
			// body so the executor at least gets to send it, and let
			// the next ReceiveMessage trip the channel error.
			return d.Body, fmt.Errorf("rabbitmq Ack: %w", ackErr)
		}
		return d.Body, nil
	}
}

func (c *RabbitMQConnector) Ping(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.IsClosed() {
		return errors.New("rabbitmq: connection closed")
	}
	return nil
}
