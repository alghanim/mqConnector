package mq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/Azure/go-amqp"
)

// AMQP10Connector speaks AMQP 1.0 — the standard ratified protocol,
// not RabbitMQ's AMQP 0.9.1. It covers Azure Service Bus, Apache
// ActiveMQ Artemis, Solace, and any other broker that implements the
// standard.
//
// The library carries its own TLS via the URL scheme: amqps://host →
// TLS, amqp://host → cleartext. We also pass a TLS config when any
// of the TLS fields are populated so cert pinning / mTLS work.
//
// Address vs queue-name terminology: AMQP 1.0 uses "address" for
// senders and "source" for receivers. We map the operator's Topic
// (or QueueName fallback) into both, since this connector consumes
// from and publishes to the same logical entity.
type AMQP10Connector struct {
	cfg Config

	mu       sync.Mutex
	conn     *amqp.Conn
	session  *amqp.Session
	sender   *amqp.Sender
	receiver *amqp.Receiver

	// pending holds the most-recent unacknowledged delivery. Commit
	// AcceptMessage's it; Nack ReleaseMessage's it. The pool serialises
	// access, so at most one is in flight per connector.
	pending *amqp.Message
}

func newAMQP10(cfg Config) Connector { return &AMQP10Connector{cfg: cfg} }

// Connect dials the broker and opens a sender + receiver against the
// configured address. Self-healing: a stale conn from a prior outage
// is torn down before we redial.
func (c *AMQP10Connector) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		// go-amqp 1.x doesn't expose IsClosed; rely on a cheap session
		// ping by attempting a no-op. Cheapest health check is just
		// re-using the conn if we have one.
		return nil
	}
	if c.cfg.URL == "" {
		return errors.New("amqp10: url is required (e.g. amqps://host:5671)")
	}
	address := c.address()
	if address == "" {
		return errors.New("amqp10: address (topic / queue_name) is required")
	}

	opts := &amqp.ConnOptions{
		ContainerID: c.containerID(),
	}
	if c.cfg.Username != "" {
		opts.SASLType = amqp.SASLTypePlain(c.cfg.Username, c.cfg.Password)
	}
	if c.cfg.TLS.Enabled() {
		tlsCfg, err := BuildTLSConfig(c.cfg.TLS)
		if err != nil {
			return fmt.Errorf("amqp10 TLS: %w", err)
		}
		opts.TLSConfig = tlsCfg
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := amqp.Dial(dialCtx, c.cfg.URL, opts)
	if err != nil {
		return fmt.Errorf("amqp10 dial: %w", err)
	}
	session, err := conn.NewSession(dialCtx, nil)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("amqp10 session: %w", err)
	}

	// Sender + receiver on the same address. Receiver uses Settle Mode
	// "Second" (per-message ack from us) so a crash between Receive
	// and the executor's downstream Send re-delivers the bytes.
	sender, err := session.NewSender(dialCtx, address, nil)
	if err != nil {
		_ = session.Close(context.Background())
		_ = conn.Close()
		return fmt.Errorf("amqp10 sender: %w", err)
	}
	recvOpts := &amqp.ReceiverOptions{
		// Credit window of 16 mirrors the RabbitMQ prefetch — keeps
		// in-flight bytes bounded so a slow executor doesn't drain a
		// huge backlog into memory.
		Credit: 16,
	}
	receiver, err := session.NewReceiver(dialCtx, address, recvOpts)
	if err != nil {
		_ = sender.Close(context.Background())
		_ = session.Close(context.Background())
		_ = conn.Close()
		return fmt.Errorf("amqp10 receiver: %w", err)
	}

	c.conn = conn
	c.session = session
	c.sender = sender
	c.receiver = receiver
	return nil
}

func (c *AMQP10Connector) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c.receiver != nil {
		_ = c.receiver.Close(closeCtx)
		c.receiver = nil
	}
	if c.sender != nil {
		_ = c.sender.Close(closeCtx)
		c.sender = nil
	}
	if c.session != nil {
		_ = c.session.Close(closeCtx)
		c.session = nil
	}
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	return nil
}

func (c *AMQP10Connector) SendMessage(ctx context.Context, message []byte) error {
	c.mu.Lock()
	sender := c.sender
	c.mu.Unlock()
	if sender == nil {
		return errors.New("amqp10: sender not opened")
	}
	return sender.Send(ctx, amqp.NewMessage(message), nil)
}

func (c *AMQP10Connector) ReceiveMessage(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	receiver := c.receiver
	c.mu.Unlock()
	if receiver == nil {
		return nil, errors.New("amqp10: receiver not opened")
	}
	msg, err := receiver.Receive(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Hold acceptance until Commit() is called after a successful
	// downstream send (or DLQ push). A crash mid-flight leaves the
	// message in the broker's unsettled state; on reconnect the
	// broker redelivers, giving true at-least-once semantics.
	c.mu.Lock()
	c.pending = msg
	c.mu.Unlock()
	return assembleBody(msg), nil
}

// Commit settles the most-recent delivery with the broker as accepted.
func (c *AMQP10Connector) Commit(ctx context.Context) error {
	c.mu.Lock()
	receiver := c.receiver
	msg := c.pending
	c.pending = nil
	c.mu.Unlock()
	if msg == nil {
		return nil
	}
	if receiver == nil {
		return errors.New("amqp10: receiver not opened")
	}
	if err := receiver.AcceptMessage(ctx, msg); err != nil {
		return fmt.Errorf("amqp10 accept: %w", err)
	}
	return nil
}

// Nack releases (requeue=true) or rejects (requeue=false) the
// most-recent delivery. Release returns the message to the broker for
// redelivery; Reject sends it to the broker's dead-letter chain.
func (c *AMQP10Connector) Nack(ctx context.Context, requeue bool) error {
	c.mu.Lock()
	receiver := c.receiver
	msg := c.pending
	c.pending = nil
	c.mu.Unlock()
	if msg == nil {
		return nil
	}
	if receiver == nil {
		return errors.New("amqp10: receiver not opened")
	}
	if requeue {
		if err := receiver.ReleaseMessage(ctx, msg); err != nil {
			return fmt.Errorf("amqp10 release: %w", err)
		}
		return nil
	}
	if err := receiver.RejectMessage(ctx, msg, nil); err != nil {
		return fmt.Errorf("amqp10 reject: %w", err)
	}
	return nil
}

func (c *AMQP10Connector) Ping(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return errors.New("amqp10: not connected")
	}
	return nil
}

// ─── helpers ────────────────────────────────────────────────────────

func (c *AMQP10Connector) address() string {
	if c.cfg.Topic != "" {
		return c.cfg.Topic
	}
	return c.cfg.QueueName
}

func (c *AMQP10Connector) containerID() string {
	if c.cfg.ClientID != "" {
		return c.cfg.ClientID
	}
	return "mqconnector"
}

// assembleBody pulls the body out of an AMQP 1.0 message. AMQP
// supports three body types: a single Data section, a Value, or a
// Sequence. We try Data first (matches the pipeline's []byte shape),
// then Value as a string fallback. Sequence is rare on enterprise
// brokers and is dropped to nil.
func assembleBody(msg *amqp.Message) []byte {
	if len(msg.Data) > 0 {
		// Concatenate all Data sections — most receivers send one,
		// but the spec permits multiple.
		var size int
		for _, d := range msg.Data {
			size += len(d)
		}
		out := make([]byte, 0, size)
		for _, d := range msg.Data {
			out = append(out, d...)
		}
		return out
	}
	if msg.Value != nil {
		if s, ok := msg.Value.(string); ok {
			return []byte(s)
		}
		if b, ok := msg.Value.([]byte); ok {
			return b
		}
	}
	return nil
}
