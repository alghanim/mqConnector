package mq

import (
	"fmt"

	"github.com/IBM/sarama"
)

type KafkaConnector struct {
	brokers           []string
	topic             string
	producer          sarama.SyncProducer
	consumer          sarama.Consumer
	partitionConsumer sarama.PartitionConsumer
}

func (c *KafkaConnector) Connect() error {
	config := sarama.NewConfig()
	config.Producer.Return.Successes = true
	config.Consumer.Return.Errors = true

	// Initialize producer
	producer, err := sarama.NewSyncProducer(c.brokers, config)
	if err != nil {
		return fmt.Errorf("failed to create Kafka producer: %v", err)
	}
	c.producer = producer

	// Initialize consumer
	consumer, err := sarama.NewConsumer(c.brokers, nil)
	if err != nil {
		return fmt.Errorf("failed to create Kafka consumer: %v", err)
	}
	c.consumer = consumer

	// Initialize partition consumer
	partitionConsumer, err := c.consumer.ConsumePartition(c.topic, 0, sarama.OffsetNewest)
	if err != nil {
		return fmt.Errorf("failed to start partition consumer: %v", err)
	}
	c.partitionConsumer = partitionConsumer

	return nil
}

func (c *KafkaConnector) Disconnect() error {
	if c.producer != nil {
		c.producer.Close()
	}
	if c.partitionConsumer != nil {
		c.partitionConsumer.Close()
	}
	if c.consumer != nil {
		c.consumer.Close()
	}
	return nil
}

func (c *KafkaConnector) SendMessage(message []byte) error {
	if c.producer == nil {
		return fmt.Errorf("producer not initialized")
	}

	msg := &sarama.ProducerMessage{
		Topic: c.topic,
		Value: sarama.ByteEncoder(message),
	}
	_, _, err := c.producer.SendMessage(msg)
	if err != nil {
		return fmt.Errorf("failed to send message: %v", err)
	}
	return nil
}

func (c *KafkaConnector) ReceiveMessage() ([]byte, error) {
	if c.partitionConsumer == nil {
		return nil, fmt.Errorf("partition consumer not initialized")
	}

	msg := <-c.partitionConsumer.Messages()
	return msg.Value, nil
}
