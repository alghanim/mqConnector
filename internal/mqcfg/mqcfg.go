// Package mqcfg holds the conversion from a storage.Connection (the persisted
// shape) to an mq.Config (the connector-factory shape). It lives in its own
// package to keep storage and mq from depending on each other, and to break
// what would otherwise be a pipeline ↔ dlq import cycle.
package mqcfg

import (
	"strings"

	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// From returns the mq.Config that corresponds to a storage.Connection. The
// connection type is normalised via mq.ParseType; if it can't be parsed the
// returned Config carries an empty Type (the connector factory will reject
// it). Brokers (kafka) are comma-split and trimmed.
func From(c *storage.Connection) mq.Config {
	t, _ := mq.ParseType(c.Type)
	cfg := mq.Config{
		Type:         t,
		QueueManager: c.QueueManager,
		ConnName:     c.ConnName,
		Channel:      c.Channel,
		Username:     c.Username,
		Password:     c.Password,
		QueueName:    c.QueueName,
		URL:          c.URL,
		Topic:        c.Topic,
		// MQTT / NATS / AMQP 1.0 — see storage migration 0009.
		// ClientID / QoS go to MQTT + AMQP 1.0; StreamName /
		// ConsumerName to NATS JetStream. Each connector reads only
		// the fields it cares about; the rest are no-ops.
		ClientID:     c.ClientID,
		QoS:          c.QoS,
		StreamName:   c.StreamName,
		ConsumerName: c.ConsumerName,
		GroupID:       c.GroupID,
		InitialOffset: c.InitialOffset,
		TLS: mq.TLSConfig{
			CAFile:             c.TLSCAFile,
			CertFile:           c.TLSCertFile,
			KeyFile:            c.TLSKeyFile,
			InsecureSkipVerify: c.TLSInsecureSkipVerify,
		},
	}
	if c.Brokers != "" {
		parts := strings.Split(c.Brokers, ",")
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				cfg.Brokers = append(cfg.Brokers, t)
			}
		}
	}
	return cfg
}
