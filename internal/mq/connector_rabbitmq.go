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

	// pendingDelivery holds the delivery returned by the last
	// ReceiveMessage call. Commit acks it; Nack nacks it. The pool
	// serialises ReceiveMessage on a given connector, so at most one
	// delivery is pending at any time.
	pendingDelivery *amqp.Delivery
}

func newRabbitMQ(cfg Config) Connector {
	return &RabbitMQConnector{cfg: cfg}
}

func (c *RabbitMQConnector) Connect(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Self-healing: if the broker dropped under us and we never tore the
	// connector down, c.conn is non-nil but useless. Recognise that here
	// and fall through to a fresh dial instead of silently no-op'ing.
	if c.conn != nil {
		if !c.conn.IsClosed() {
			return nil
		}
		// Reap the dead handles so the close-paths below stay correct.
		_ = c.conn.Close()
		c.conn = nil
		c.channel = nil
		c.consumeOnce = sync.Once{}
		c.consumeErr = nil
		c.deliveries = nil
		c.consumerTag = ""
		c.pendingDelivery = nil
	}
	// When the operator has configured TLS material, dial through
	// DialConfig so we can attach a custom *tls.Config (CA roots +
	// optional client cert for mTLS). amqp091 honours TLSClientConfig
	// for amqps:// URLs and ignores it for amqp:// — so this is safe
	// even on plaintext connections; the operator just won't see it
	// in effect unless they switch the scheme.
	var conn *amqp.Connection
	var err error
	if c.cfg.TLS.Enabled() {
		tlsCfg, err2 := BuildTLSConfig(c.cfg.TLS)
		if err2 != nil {
			return fmt.Errorf("rabbitmq TLS: %w", err2)
		}
		conn, err = amqp.DialConfig(c.cfg.URL, amqp.Config{TLSClientConfig: tlsCfg})
	} else {
		conn, err = amqp.Dial(c.cfg.URL)
	}
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
	c.pendingDelivery = nil
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
		// Hold the delivery until the executor calls Commit() (after a
		// successful downstream send) or Nack() (on failure routed to
		// the broker's own DLX). This is the at-least-once guarantee:
		// a crash between receive and Commit causes the broker to
		// redeliver instead of silently dropping the message. The pool
		// serialises access to one connection, so at most one delivery
		// is pending per RabbitMQConnector at a time.
		c.pendingDelivery = &d
		c.mu.Unlock()
		return d.Body, nil
	}
}

// Commit acks the most-recent delivery returned by ReceiveMessage. The
// executor calls this after a successful downstream send OR after a
// successful DLQ push — in both cases the message is "handled" and
// the broker can forget it.
func (c *RabbitMQConnector) Commit(_ context.Context) error {
	c.mu.Lock()
	d := c.pendingDelivery
	c.pendingDelivery = nil
	c.mu.Unlock()
	if d == nil {
		return nil // nothing to commit — Commit called twice or before any receive
	}
	if err := d.Ack(false); err != nil {
		return fmt.Errorf("rabbitmq Ack: %w", err)
	}
	return nil
}

// Nack rolls back the most-recent delivery. requeue=true puts it back
// at the head of the queue for another consumer; requeue=false sends
// it to the broker's configured dead-letter exchange (if any). The
// executor's own DLQ already covers the application-level case, so
// the natural pairing is: send-succeeded → Commit; pipeline-stage-error
// → Commit (already DLQ'd internally); broker-side-broken → don't call
// either, let the redelivery cycle replay it next time.
func (c *RabbitMQConnector) Nack(_ context.Context, requeue bool) error {
	c.mu.Lock()
	d := c.pendingDelivery
	c.pendingDelivery = nil
	c.mu.Unlock()
	if d == nil {
		return nil
	}
	if err := d.Nack(false, requeue); err != nil {
		return fmt.Errorf("rabbitmq Nack: %w", err)
	}
	return nil
}

func (c *RabbitMQConnector) Ping(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.IsClosed() {
		return errors.New("rabbitmq: connection closed")
	}
	return nil
}

// Depth implements DepthReporter via QueueDeclare (passive=true via the
// passive flag on declare). The amqp091 client returns Messages on the
// Queue struct — that's the broker-side ready-message count for the
// queue we're consuming. Cost is one round-trip per call so callers
// should rate-limit (the executor samples every ~30s).
func (c *RabbitMQConnector) Depth(_ context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.channel == nil {
		return -1, errors.New("rabbitmq: not connected")
	}
	q, err := c.channel.QueueDeclarePassive(c.cfg.QueueName, true, false, false, false, nil)
	if err != nil {
		return -1, fmt.Errorf("rabbitmq queue inspect: %w", err)
	}
	return int64(q.Messages), nil
}
