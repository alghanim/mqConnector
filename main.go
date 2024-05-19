package main

import (
	"fmt"
	"mqConnector/mq"
)

func main() {
	config := map[string]string{
		"queueManager": "QM1",
		"connName":     "localhost(1414)",
		"channel":      "DEV.APP.SVRCONN",
		"user":         "app",
		"password":     "password",
		"queueName":    "TEST.QUEUE",
		"url":          "amqp://guest:guest@localhost:5672/",
		"brokers":      "localhost:9092",
		"topic":        "test_topic",
	}

	// Example for IBM MQ
	ibmConnector, err := mq.NewMQConnector(mq.IBM, config)
	if err != nil {
		fmt.Printf("Failed to create IBM MQ connector: %v\n", err)
		return
	}

	err = ibmConnector.Connect()
	if err != nil {
		fmt.Printf("Failed to connect to IBM MQ: %v\n", err)
		return
	}
	defer ibmConnector.Disconnect()

	// Send a message
	err = ibmConnector.SendMessage([]byte("Hello IBM MQ"))
	if err != nil {
		fmt.Printf("Failed to send message: %v\n", err)
		return
	}

	// Receive a message
	msg, err := ibmConnector.ReceiveMessage()
	if err != nil {
		fmt.Printf("Failed to receive message: %v\n", err)
		return
	}
	fmt.Printf("Received message from IBM MQ: %s\n", string(msg))

	// Example for RabbitMQ
	rabbitConnector, err := mq.NewMQConnector(mq.RabbitMQ, config)
	if err != nil {
		fmt.Printf("Failed to create RabbitMQ connector: %v\n", err)
		return
	}

	err = rabbitConnector.Connect()
	if err != nil {
		fmt.Printf("Failed to connect to RabbitMQ: %v\n", err)
		return
	}
	defer rabbitConnector.Disconnect()

	// Send a message
	err = rabbitConnector.SendMessage([]byte("Hello RabbitMQ"))
	if err != nil {
		fmt.Printf("Failed to send message: %v\n", err)
		return
	}

	// Receive a message
	msg, err = rabbitConnector.ReceiveMessage()
	if err != nil {
		fmt.Printf("Failed to receive message: %v\n", err)
		return
	}
	fmt.Printf("Received message from RabbitMQ: %s\n", string(msg))

	// Example for Kafka
	kafkaConnector, err := mq.NewMQConnector(mq.Kafka, config)
	if err != nil {
		fmt.Printf("Failed to create Kafka connector: %v\n", err)
		return
	}

	err = kafkaConnector.Connect()
	if err != nil {
		fmt.Printf("Failed to connect to Kafka: %v\n", err)
		return
	}
	defer kafkaConnector.Disconnect()

	// Send a message
	err = kafkaConnector.SendMessage([]byte("Hello Kafka"))
	if err != nil {
		fmt.Printf("Failed to send message: %v\n", err)
		return
	}

	// Receive a message
	msg, err = kafkaConnector.ReceiveMessage()
	if err != nil {
		fmt.Printf("Failed to receive message: %v\n", err)
		return
	}
	fmt.Printf("Received message from Kafka: %s\n", string(msg))
}
