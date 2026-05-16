// Package events is a tiny in-process publish/subscribe bus for the
// lifecycle signals the webhook dispatcher needs to react to (and
// that future internal consumers might want too — metrics, audit
// extensions, etc.).
//
// Why not a heavier message bus? mqConnector is a single binary with a
// single-leader-elected writer per tenant. Events fan out at HTTP-handler
// or pipeline-loop scale (handfuls per second), and consumers are
// in-process. A `chan Event` per subscriber with non-blocking fan-out
// gives us at-most-once semantics that match what webhooks want.
//
// Subscribers must read fast or drop. Publish() does a non-blocking
// channel send to each subscriber's buffered channel; a full buffer
// causes the event to be dropped for that subscriber and logged so an
// operator can size the buffer up. This is a deliberate choice: a slow
// webhook receiver must never back-pressure the message-bridge hot path.
package events

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Event types. Match the strings stored in webhooks.events filter.
const (
	TypePipelineStarted = "pipeline.started"
	TypePipelineStopped = "pipeline.stopped"
	TypePipelineError   = "pipeline.error"
	TypeDLQPushed       = "dlq.pushed"
	TypeConnectionTest  = "connection.test"
)

// Event is the on-the-wire shape. Type is one of the constants above.
// TenantID scopes routing — subscribers filter by tenant before
// firing webhooks so a tenant A event never reaches a tenant B sink.
// Data is the event-specific payload; structure is per-type.
type Event struct {
	Type     string         `json:"type"`
	TenantID string         `json:"tenant_id"`
	At       time.Time      `json:"at"`
	Data     map[string]any `json:"data,omitempty"`
}

// Publisher is the bus. Construct with NewPublisher.
//
// Concurrency: Subscribe / Unsubscribe / Publish are all goroutine-
// safe. Subscribe returns a buffered channel + an unsubscribe func.
// The buffer is sized at construction time; Publish drops on a full
// buffer rather than blocking.
type Publisher struct {
	mu          sync.RWMutex
	subscribers map[int]chan Event
	nextID      int
	bufSize     int
	logger      *slog.Logger
}

// NewPublisher builds a publisher with `bufSize` slots per subscriber.
// Sensible default: 64 — enough to absorb a short burst, small enough
// that an unhealthy subscriber leaks at most a few KB per event class.
func NewPublisher(bufSize int, logger *slog.Logger) *Publisher {
	if bufSize <= 0 {
		bufSize = 64
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Publisher{
		subscribers: map[int]chan Event{},
		bufSize:     bufSize,
		logger:      logger.With("component", "events"),
	}
}

// Subscribe registers a new consumer. Returns the receive-only channel
// + an unsubscribe func the caller must invoke on shutdown.
func (p *Publisher) Subscribe() (<-chan Event, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.nextID
	p.nextID++
	ch := make(chan Event, p.bufSize)
	p.subscribers[id] = ch
	return ch, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if c, ok := p.subscribers[id]; ok {
			delete(p.subscribers, id)
			close(c)
		}
	}
}

// Publish fans the event out to every subscriber, dropping on full
// buffer. Returns the number of subscribers that received the event
// (i.e. didn't drop).
func (p *Publisher) Publish(_ context.Context, e Event) int {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	p.mu.RLock()
	subs := p.subscribers
	delivered := 0
	for id, ch := range subs {
		select {
		case ch <- e:
			delivered++
		default:
			p.logger.Warn("event dropped — subscriber buffer full",
				"subscriber", id, "event_type", e.Type, "tenant", e.TenantID)
		}
	}
	p.mu.RUnlock()
	return delivered
}
