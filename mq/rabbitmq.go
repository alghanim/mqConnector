package mq

import (
	"fmt"

	"github.com/streadway/amqp"
)

type RabbitMQConnector struct {
	url       string
	queueName string
	conn      *amqp.Connection
	channel   *amqp.Channel
}

func (c *RabbitMQConnector) Connect() error {
	conn, err := amqp.Dial(c.url)
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open a channel: %v", err)
	}

	// Declare the queue
	_, err = ch.QueueDeclare(
		c.queueName, // name
		false,       // durable
		false,       // delete when unused
		false,       // exclusive
		false,       // no-wait
		nil,         // arguments
	)
	if err != nil {
		return fmt.Errorf("failed to declare a queue: %v", err)
	}

	c.conn = conn
	c.channel = ch
	return nil
}

func (c *RabbitMQConnector) Disconnect() error {
	if c.channel != nil {
		c.channel.Close()
	}
	if c.conn != nil {
		c.conn.Close()
	}
	return nil
}

func (c *RabbitMQConnector) SendMessage(message []byte) error {
	if c.channel == nil {
		return fmt.Errorf("channel not opened")
	}

	err := c.channel.Publish(
		"",          // exchange
		c.queueName, // routing key
		false,       // mandatory
		false,       // immediate
		amqp.Publishing{
			ContentType: "text/plain",
			Body:        message,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to publish a message: %v", err)
	}
	return nil
}

func (c *RabbitMQConnector) ReceiveMessage() ([]byte, error) {
	if c.channel == nil {
		return nil, fmt.Errorf("channel not opened")
	}

	msgs, err := c.channel.Consume(
		c.queueName, // queue
		"",          // consumer
		true,        // auto-ack
		false,       // exclusive
		false,       // no-local
		false,       // no-wait
		nil,         // args
	)
	if err != nil {
		return nil, fmt.Errorf("failed to register a consumer: %v", err)
	}

	msg := <-msgs
	return msg.Body, nil
}
