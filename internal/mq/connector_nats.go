package mq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSConnector speaks NATS core when no StreamName is configured and
// NATS JetStream when one is. JetStream is the recommended path for
// production: durable, replayable, at-least-once semantics. Core NATS
// is fire-and-forget — useful for ephemeral / low-stakes streams.
//
// Subject vs topic vs queue-name: NATS uses "subject" terminology, but
// our Config carries the value as either Topic (preferred) or
// QueueName (fallback) so operators can reuse the same input across
// broker types in the connection-form.
//
// Subscription model:
//   • Core NATS: nats.Subscribe — the callback fires on each msg,
//     same channel-buffered drain pattern as the MQTT connector.
//   • JetStream: durable consumer via JS.PullSubscribe so message
//     redelivery survives a worker restart. ReceiveMessage Fetch()es
//     one msg + Acks after the caller has it.
type NATSConnector struct {
	cfg Config

	mu     sync.Mutex
	nc     *nats.Conn
	sub    *nats.Subscription   // core NATS subscription, if used
	js     nats.JetStreamContext // JetStream, if used
	pullSub *nats.Subscription  // JetStream pull-subscribe, if used

	deliveries chan []byte // buffered drain for core NATS deliveries

	// pendingJS holds the most-recent JetStream message awaiting an
	// explicit Ack/Nak via Commit/Nack. The pool serialises receives,
	// so at most one is pending per connector.
	pendingJS *nats.Msg
}

func newNATS(cfg Config) Connector { return &NATSConnector{cfg: cfg} }

// Connect dials the server and sets up the subscription. JetStream is
// chosen iff StreamName is non-empty; otherwise we use core NATS.
func (c *NATSConnector) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nc != nil && c.nc.IsConnected() {
		return nil
	}
	if c.nc != nil {
		c.nc.Close()
		c.nc = nil
	}

	if c.cfg.URL == "" {
		return errors.New("nats: url is required (e.g. nats://host:4222)")
	}
	subject := c.subject()
	if subject == "" {
		return errors.New("nats: subject (topic / queue_name) is required")
	}

	opts := []nats.Option{
		nats.Name(c.clientName()),
		nats.Timeout(10 * time.Second),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1), // reconnect forever; the pipeline self-heal handles longer outages
	}
	if c.cfg.Username != "" || c.cfg.Password != "" {
		opts = append(opts, nats.UserInfo(c.cfg.Username, c.cfg.Password))
	}
	if c.cfg.TLS.Enabled() {
		tlsCfg, err := BuildTLSConfig(c.cfg.TLS)
		if err != nil {
			return fmt.Errorf("nats TLS: %w", err)
		}
		opts = append(opts, nats.Secure(tlsCfg))
	}

	nc, err := nats.Connect(c.cfg.URL, opts...)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}

	// Branch: JetStream if a stream name is set, otherwise core NATS.
	if strings.TrimSpace(c.cfg.StreamName) != "" {
		js, err := nc.JetStream()
		if err != nil {
			nc.Close()
			return fmt.Errorf("nats jetstream context: %w", err)
		}
		// Durable consumer name — if blank, generate one tied to our
		// client name so restarts re-attach to the same consumer.
		durable := c.cfg.ConsumerName
		if durable == "" {
			durable = c.clientName()
		}
		// Bind to existing stream + consumer. JetStream lazily creates
		// the consumer when it doesn't exist; operators who want
		// explicit lifecycle should pre-create via `nats consumer
		// add`. The pull-subscribe model gives us per-message Ack
		// control which the executor's at-least-once posture needs.
		sub, err := js.PullSubscribe(subject, durable,
			nats.BindStream(c.cfg.StreamName),
		)
		if err != nil {
			nc.Close()
			return fmt.Errorf("nats jetstream subscribe: %w", err)
		}
		c.pullSub = sub
		c.js = js
	} else {
		// Core NATS — async subscribe + buffered drain.
		c.deliveries = make(chan []byte, 256)
		sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
			select {
			case c.deliveries <- msg.Data:
			default:
				// Drop on full buffer — same posture as MQTT.
			}
		})
		if err != nil {
			nc.Close()
			return fmt.Errorf("nats subscribe: %w", err)
		}
		c.sub = sub
	}

	c.nc = nc
	_ = ctx
	return nil
}

func (c *NATSConnector) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sub != nil {
		_ = c.sub.Unsubscribe()
		c.sub = nil
	}
	if c.pullSub != nil {
		_ = c.pullSub.Unsubscribe()
		c.pullSub = nil
	}
	c.js = nil
	if c.nc != nil {
		c.nc.Close()
		c.nc = nil
	}
	if c.deliveries != nil {
		close(c.deliveries)
		c.deliveries = nil
	}
	return nil
}

// SendMessage publishes to the subject. Under JetStream, PublishMsg
// waits for the broker's ack; under core NATS, Publish returns as
// soon as the bytes are flushed (no broker durability).
func (c *NATSConnector) SendMessage(ctx context.Context, message []byte) error {
	c.mu.Lock()
	nc := c.nc
	js := c.js
	subject := c.subject()
	c.mu.Unlock()
	if nc == nil || !nc.IsConnected() {
		return errors.New("nats: not connected")
	}
	if js != nil {
		_, err := js.PublishMsg(&nats.Msg{Subject: subject, Data: message}, nats.Context(ctx))
		return err
	}
	if err := nc.Publish(subject, message); err != nil {
		return err
	}
	// FlushTimeout makes Publish synchronous-ish — without it, a
	// crash between Publish and the next loop iteration could lose
	// the in-flight bytes. With JetStream the broker ack already
	// gives us this guarantee.
	return nc.FlushTimeout(5 * time.Second)
}

// ReceiveMessage returns the next delivery. JetStream uses a pull
// Fetch (1 msg, 5s timeout) and Acks after capture so a crash
// between Fetch and the executor's downstream send triggers a
// redelivery on the next Fetch.
func (c *NATSConnector) ReceiveMessage(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	pull := c.pullSub
	ch := c.deliveries
	c.mu.Unlock()

	if pull != nil {
		// JetStream pull. nats.Context binds the fetch to ctx so
		// shutdown unwinds cleanly.
		msgs, err := pull.Fetch(1, nats.Context(ctx))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
				return nil, ctx.Err()
			}
			return nil, err
		}
		if len(msgs) == 0 {
			return nil, errors.New("nats: empty fetch")
		}
		m := msgs[0]
		// Hold the Ack until the executor calls Commit() after a
		// successful downstream send (or DLQ push). A crash mid-flight
		// causes JetStream to redeliver after the consumer's AckWait
		// expires, giving us true at-least-once.
		c.mu.Lock()
		c.pendingJS = m
		c.mu.Unlock()
		return m.Data, nil
	}
	if ch != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case body, ok := <-ch:
			if !ok {
				return nil, errors.New("nats: deliveries channel closed")
			}
			return body, nil
		}
	}
	return nil, errors.New("nats: not subscribed")
}

// Commit acks the most-recent JetStream message. Core NATS has no
// per-message acknowledgement (fire-and-forget protocol), so Commit
// is a no-op there — operators using core NATS as a source accept
// the broker's at-most-once semantics by definition.
func (c *NATSConnector) Commit(_ context.Context) error {
	c.mu.Lock()
	m := c.pendingJS
	c.pendingJS = nil
	c.mu.Unlock()
	if m == nil {
		return nil
	}
	if err := m.Ack(); err != nil {
		return fmt.Errorf("nats jetstream Ack: %w", err)
	}
	return nil
}

// Nack rolls back the most-recent JetStream message so the broker
// redelivers it after AckWait expires (or immediately, via Nak). Core
// NATS no-ops for the same reason as Commit.
func (c *NATSConnector) Nack(_ context.Context, requeue bool) error {
	c.mu.Lock()
	m := c.pendingJS
	c.pendingJS = nil
	c.mu.Unlock()
	if m == nil {
		return nil
	}
	// requeue=true → immediate Nak (broker redelivers on the next
	// pull). requeue=false → Term, which tells JetStream to NOT
	// redeliver (the executor's own DLQ already covered this case).
	if requeue {
		if err := m.Nak(); err != nil {
			return fmt.Errorf("nats jetstream Nak: %w", err)
		}
	} else {
		if err := m.Term(); err != nil {
			return fmt.Errorf("nats jetstream Term: %w", err)
		}
	}
	return nil
}

func (c *NATSConnector) Ping(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nc == nil || !c.nc.IsConnected() {
		return errors.New("nats: not connected")
	}
	return nil
}

// subject prefers Topic; falls back to QueueName for form-field reuse.
func (c *NATSConnector) subject() string {
	if c.cfg.Topic != "" {
		return c.cfg.Topic
	}
	return c.cfg.QueueName
}

// clientName is the friendly name reported to the NATS server (shows
// up in `nats server list connections`). Uses ClientID when set;
// otherwise a stable default.
func (c *NATSConnector) clientName() string {
	if strings.TrimSpace(c.cfg.ClientID) != "" {
		return c.cfg.ClientID
	}
	return "mqconnector"
}
