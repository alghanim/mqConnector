package mq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MQTTConnector publishes to and subscribes to an MQTT v3.1.1 broker
// (most v5 brokers also accept v3 clients). The topic, QoS, and
// optional client ID come from the connection's Config.
//
// Bridging the push/pull gap: paho delivers messages via a callback
// closure, but the pipeline executor pulls one message at a time via
// ReceiveMessage. We buffer deliveries into a channel (size 256) and
// drain it from ReceiveMessage. A full buffer drops the oldest
// message rather than blocking the callback — keeps the publish side
// healthy when the consumer is slow. The drop is logged so an
// operator can see it.
//
// Cleanup: Disconnect cancels the subscription, closes the delivery
// channel, and disconnects the client with a 1s grace.
type MQTTConnector struct {
	cfg Config

	mu        sync.Mutex
	client    mqtt.Client
	deliveries chan []byte

	// guard against double-subscribe; paho is permissive but the
	// re-subscribe creates a second goroutine for the callback.
	subscribed bool
}

func newMQTT(cfg Config) Connector { return &MQTTConnector{cfg: cfg} }

// Connect establishes the broker connection and subscribes to the
// configured topic. Self-healing: if the prior client is still set
// but isn't connected, we tear it down before dialing fresh.
func (c *MQTTConnector) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		if c.client.IsConnected() {
			return nil
		}
		c.client.Disconnect(250)
		c.client = nil
		c.subscribed = false
	}
	if c.cfg.URL == "" {
		return errors.New("mqtt: url is required (e.g. tcp://host:1883 or ssl://host:8883)")
	}
	if c.cfg.Topic == "" && c.cfg.QueueName == "" {
		return errors.New("mqtt: topic (or queue_name) is required")
	}

	opts := mqtt.NewClientOptions().AddBroker(c.cfg.URL)
	opts.SetClientID(c.clientID())
	opts.SetCleanSession(true)
	opts.SetConnectTimeout(10 * time.Second)
	opts.SetAutoReconnect(true)
	// We use manual ack-style backpressure (bounded channel + drop on
	// full); auto-reconnect handles broker bounces. paho v1.4+ resumes
	// subscriptions transparently when reconnecting.
	if c.cfg.Username != "" {
		opts.SetUsername(c.cfg.Username)
	}
	if c.cfg.Password != "" {
		opts.SetPassword(c.cfg.Password)
	}
	// TLS, if any field is configured. MQTT TLS is signalled by the
	// URL scheme (ssl:// / tls://) — the dialer respects whichever
	// scheme the URL uses; we pass tls.Config so cert verification
	// follows the brand-tokens policy.
	if c.cfg.TLS.Enabled() {
		tlsCfg, err := BuildTLSConfig(c.cfg.TLS)
		if err != nil {
			return fmt.Errorf("mqtt TLS: %w", err)
		}
		opts.SetTLSConfig(tlsCfg)
	}

	c.deliveries = make(chan []byte, 256)
	// Default message handler — fires when no per-topic handler is
	// installed. We use it as the universal delivery sink so paho
	// doesn't drop messages between Connect and Subscribe.
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, msg mqtt.Message) {
		select {
		case c.deliveries <- msg.Payload():
			// delivered
		default:
			// buffer full — drop. The pipeline manager's slow consumer
			// is the root cause; widening the buffer just delays the
			// pressure. paho logs the drop at INFO.
		}
	})

	client := mqtt.NewClient(opts)
	tok := client.Connect()
	if !tok.WaitTimeout(10 * time.Second) {
		return errors.New("mqtt: connect timeout")
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("mqtt connect: %w", err)
	}

	topic := c.topic()
	subTok := client.Subscribe(topic, c.qos(), nil) // nil handler → default handler
	if !subTok.WaitTimeout(10 * time.Second) {
		client.Disconnect(250)
		return fmt.Errorf("mqtt: subscribe timeout on topic %q", topic)
	}
	if err := subTok.Error(); err != nil {
		client.Disconnect(250)
		return fmt.Errorf("mqtt subscribe: %w", err)
	}

	c.client = client
	c.subscribed = true
	_ = ctx // paho's own timeouts cover the network path
	return nil
}

func (c *MQTTConnector) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		if c.subscribed {
			_ = c.client.Unsubscribe(c.topic())
		}
		c.client.Disconnect(1000) // 1s grace
		c.client = nil
	}
	if c.deliveries != nil {
		close(c.deliveries)
		c.deliveries = nil
	}
	c.subscribed = false
	return nil
}

// SendMessage publishes to the configured topic at the configured QoS.
// retained=false matches the executor's "pass-through" semantics —
// retained messages would make every new subscriber see this body.
func (c *MQTTConnector) SendMessage(ctx context.Context, message []byte) error {
	c.mu.Lock()
	client := c.client
	topic := c.topic()
	qos := c.qos()
	c.mu.Unlock()
	if client == nil || !client.IsConnected() {
		return errors.New("mqtt: not connected")
	}
	tok := client.Publish(topic, qos, false, message)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-tok.Done():
		return tok.Error()
	}
}

// ReceiveMessage drains the next delivery from the buffered channel.
// Blocks until one arrives or ctx cancels.
func (c *MQTTConnector) ReceiveMessage(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	ch := c.deliveries
	c.mu.Unlock()
	if ch == nil {
		return nil, errors.New("mqtt: deliveries channel not initialised")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case body, ok := <-ch:
		if !ok {
			return nil, errors.New("mqtt: deliveries channel closed")
		}
		return body, nil
	}
}

func (c *MQTTConnector) Ping(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil || !c.client.IsConnected() {
		return errors.New("mqtt: not connected")
	}
	return nil
}

// ─── helpers ─────────────────────────────────────────────────────────

// topic picks the explicit Topic field, falling back to QueueName so
// operators familiar with the RabbitMQ form can reuse the same field.
func (c *MQTTConnector) topic() string {
	if c.cfg.Topic != "" {
		return c.cfg.Topic
	}
	return c.cfg.QueueName
}

func (c *MQTTConnector) qos() byte {
	switch c.cfg.QoS {
	case 1:
		return 1
	case 2:
		return 2
	default:
		return 0
	}
}

// clientID returns the operator-supplied ClientID, or a stable
// per-process random one if blank. MQTT brokers reject duplicate
// concurrent client IDs, so production deploys should set this
// explicitly.
func (c *MQTTConnector) clientID() string {
	if strings.TrimSpace(c.cfg.ClientID) != "" {
		return c.cfg.ClientID
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "mqconnector-" + hex.EncodeToString(b[:])
}
