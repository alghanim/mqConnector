package mq

import (
	"context"
	"errors"
	"fmt"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

type RabbitMQConnector struct {
	cfg     Config
	mu      sync.Mutex
	conn    *amqp.Connection
	channel *amqp.Channel
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
	c.conn = conn
	c.channel = ch
	return nil
}

func (c *RabbitMQConnector) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	if c.channel != nil {
		if err := c.channel.Close(); err != nil {
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

func (c *RabbitMQConnector) ReceiveMessage(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	ch := c.channel
	queue := c.cfg.QueueName
	c.mu.Unlock()
	if ch == nil {
		return nil, errors.New("rabbitmq: channel not opened")
	}
	msgs, err := ch.ConsumeWithContext(ctx, queue, "", true, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq Consume: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg, ok := <-msgs:
		if !ok {
			return nil, errors.New("rabbitmq: delivery channel closed")
		}
		return msg.Body, nil
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
