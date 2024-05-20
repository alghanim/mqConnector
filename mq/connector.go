package mq

import (
	"fmt"
	"log"
)

type MQConnector interface {
	Connect() error
	Disconnect() error
	SendMessage(message []byte) error
	ReceiveMessage() ([]byte, error)
}

type QueueType int

const (
	IBM QueueType = iota
	RabbitMQ
	Kafka
)

func NewMQConnector(queueType QueueType, config map[string]string) (MQConnector, error) {
	switch queueType {
	case IBM:
		return &IBMMQConnector{
			queueManager: config["queueManager"],
			connName:     config["connName"],
			channel:      config["channel"],
			user:         config["user"],
			password:     config["password"],
			queueName:    config["queueName"],
		}, nil
	case RabbitMQ:
		return &RabbitMQConnector{
			url:       config["url"],
			queueName: config["queueName"],
		}, nil
	case Kafka:
		return &KafkaConnector{
			brokers: []string{config["brokers"]},
			topic:   config["topic"],
		}, nil
	default:
		return nil, fmt.Errorf("unsupported queue type")
	}
}

func GetQueueType(queueType string) QueueType {
	switch queueType {
	case "IBM":
		return 0
	case "RabbitMQ":
		return 1
	case "Kafka":
		return 2
	default:
		log.Fatalf("Unsupported queue type: %v", queueType)
		return -1
	}
}
