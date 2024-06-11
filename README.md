# mqConnector

`mqConnector` is a Go-based connector for interfacing with multiple messaging systems, including IBM MQ, RabbitMQ, and Kafka. This repository provides a simple implementation to connect, send, and receive messages from various queue types.

## Features

- Connect to multiple messaging systems (IBM MQ, RabbitMQ, Kafka)
- Send messages to a queue
- Receive messages from a queue

> This section gives an overview of the key functionalities provided by the mqConnector.

## Prerequisites

- Go 1.15 or later
- IBM MQ Server (if using IBM MQ)
- RabbitMQ Server (if using RabbitMQ)
- Kafka Server (if using Kafka)

> List the software and versions required to use the mqConnector.

## Installation

1. Clone the repository:
    ```sh
    git clone https://github.com/alghanim/mqConnector.git
    cd mqConnector
    ```

2. Install dependencies:
    ```sh
    go mod tidy
    ```

> Step-by-step instructions to download and set up the project on your local machine.

## Configuration

Edit the configuration to match your messaging system. Below is an example for IBM MQ:

```go
mqc := NewMQConnector(IBM, map[string]string{
    "queueManager": "QM1",
    "connName":     "localhost(1414)",
    "channel":      "CHANNEL1",
    "user":         "admin",
    "password":     "password",
})

```

For RabbitMQ:

```go
mqc := NewMQConnector(RabbitMQ, map[string]string{
    "url":       "amqp://guest:guest@localhost:5672/",
    "queueName": "testQueue",
})
```

For Kafka:

```go
mqc := NewMQConnector(Kafka, map[string]string{
    "brokers": "localhost:9092",
    "topic":   "testTopic",
})
```

## Usage

1. Build the application:
    ```sh
    ./BuildLinuxAmd64.sh
    ```

> This command compiles the Go code into an executable named `mqConnector` as you need to set CGO_CFLAGS and CGO_LDFLAGS 

2. Run the application:
    ```sh
    ./Run.sh or ./mqConnector
    ```

> This command executes the compiled application.