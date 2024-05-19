# mqConnector

`mqConnector` is a Go-based connector for interfacing with IBM MQ systems. This repository provides a simple implementation to connect, send, and receive messages from an IBM MQ queue.

## Features

- Connect to IBM MQ
- Send messages to a queue
- Receive messages from a queue

## Prerequisites

- Go 1.15 or later
- IBM MQ Server

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

## Configuration

Edit the `main.go` file to configure your MQ connection settings:

```go
mqc := MQConfig{
    QueueManager: "QM1",
    QueueName:    "QUEUE1",
    Channel:      "CHANNEL1",
    Host:         "localhost",
    Port:         "1414",
    User:         "admin",
    Password:     "password",
}

## Usage

1. Build the application:
    ```sh
    go build -o mqConnector
    ```

> This command compiles the Go code into an executable named `mqConnector`.

2. Run the application:
    ```sh
    ./mqConnector
    ```

> This command executes the compiled application.

## Directory Structure

- `mq/`: Contains the MQ connection and message handling logic.
- `vendor/`: Vendor dependencies.

> Brief explanation of the key directories and their purpose in the project.

## Contributing

Contributions are welcome! Please open an issue or submit a pull request for any changes or enhancements.

> Encourage community involvement and contributions to the project.

## License

This project is licensed under the MIT License.

> Specify the licensing information for the project.

> The annotations in this README file explain the purpose of each section, making it easier to understand the structure and content of the document.
