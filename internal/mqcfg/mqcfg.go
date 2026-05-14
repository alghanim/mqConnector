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
