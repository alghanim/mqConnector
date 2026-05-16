package mq

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/IBM/sarama"
)

type KafkaConnector struct {
	cfg               Config
	mu                sync.Mutex
	producer          sarama.SyncProducer
	consumer          sarama.Consumer
	partitionConsumer sarama.PartitionConsumer
}

func newKafka(cfg Config) Connector {
	return &KafkaConnector{cfg: cfg}
}

func (c *KafkaConnector) Connect(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Self-healing: if a previous send/recv failed and we never tore the
	// producer/consumer down, the broker is likely gone but our handles
	// are still cached. Probe via Topics() — if it errors, drop the
	// handles and re-build from scratch on the same call.
	if c.producer != nil {
		if c.consumer != nil {
			if _, err := c.consumer.Topics(); err == nil {
				return nil
			}
		}
		// Stale handles. Tear them down and fall through.
		if c.partitionConsumer != nil {
			_ = c.partitionConsumer.Close()
			c.partitionConsumer = nil
		}
		if c.consumer != nil {
			_ = c.consumer.Close()
			c.consumer = nil
		}
		_ = c.producer.Close()
		c.producer = nil
	}
	if len(c.cfg.Brokers) == 0 {
		return errors.New("kafka: at least one broker is required")
	}
	cfg := sarama.NewConfig()
	cfg.Producer.Return.Successes = true
	cfg.Producer.Return.Errors = true
	cfg.Consumer.Return.Errors = true

	producer, err := sarama.NewSyncProducer(c.cfg.Brokers, cfg)
	if err != nil {
		return fmt.Errorf("kafka NewSyncProducer: %w", err)
	}
	consumer, err := sarama.NewConsumer(c.cfg.Brokers, cfg)
	if err != nil {
		_ = producer.Close()
		return fmt.Errorf("kafka NewConsumer: %w", err)
	}
	pc, err := consumer.ConsumePartition(c.cfg.Topic, 0, sarama.OffsetNewest)
	if err != nil {
		_ = consumer.Close()
		_ = producer.Close()
		return fmt.Errorf("kafka ConsumePartition: %w", err)
	}
	c.producer = producer
	c.consumer = consumer
	c.partitionConsumer = pc
	return nil
}

func (c *KafkaConnector) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.partitionConsumer != nil {
		_ = c.partitionConsumer.Close()
		c.partitionConsumer = nil
	}
	if c.consumer != nil {
		_ = c.consumer.Close()
		c.consumer = nil
	}
	if c.producer != nil {
		_ = c.producer.Close()
		c.producer = nil
	}
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
	pc := c.partitionConsumer
	c.mu.Unlock()
	if pc == nil {
		return nil, errors.New("kafka: partition consumer not initialised")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg, ok := <-pc.Messages():
		if !ok {
			return nil, errors.New("kafka: partition consumer closed")
		}
		return msg.Value, nil
	case err := <-pc.Errors():
		if err == nil {
			return nil, errors.New("kafka: error channel closed")
		}
		return nil, err.Err
	}
}

func (c *KafkaConnector) Ping(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.consumer == nil {
		return errors.New("kafka: not connected")
	}
	if _, err := c.consumer.Topics(); err != nil {
		return fmt.Errorf("kafka Topics: %w", err)
	}
	return nil
}
