package mq

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// MemoryConnector is a process-local Connector backed by a Go channel. It is
// intended for tests and the e2e harness — it lets pipelines run end-to-end
// without needing a real broker process. Construct it with NewMemoryConnector
// or share an existing instance across "source" and "destination" roles by
// referencing the same name through a shared MemoryRegistry.
type MemoryConnector struct {
	name string
	reg  *MemoryRegistry

	mu        sync.Mutex
	connected bool
}

// MemoryRegistry is a hub of in-memory queues keyed by name. Multiple
// connectors targeting the same name share a single underlying channel, which
// is how a pipeline test wires its "source" and "destination" together.
type MemoryRegistry struct {
	mu     sync.Mutex
	queues map[string]chan []byte
	buffer int
}

// NewMemoryRegistry returns a fresh hub. Buffer is the per-queue channel size
// (0 → unbuffered). Use a small buffer (e.g. 16) for tests so consumers don't
// have to be ready before producers push.
func NewMemoryRegistry(buffer int) *MemoryRegistry {
	return &MemoryRegistry{queues: map[string]chan []byte{}, buffer: buffer}
}

// Queue returns the channel for name, creating it on miss.
func (r *MemoryRegistry) Queue(name string) chan []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.queues[name]; ok {
		return c
	}
	c := make(chan []byte, r.buffer)
	r.queues[name] = c
	return c
}

// Drain reads every message available on the named queue without blocking.
// Useful for assertions in tests.
func (r *MemoryRegistry) Drain(name string) [][]byte {
	q := r.Queue(name)
	var out [][]byte
	for {
		select {
		case m, ok := <-q:
			if !ok {
				return out
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

// NewMemoryConnector returns a Connector that publishes to and consumes from
// reg under name. The returned connector implements the full Connector API.
func NewMemoryConnector(reg *MemoryRegistry, name string) *MemoryConnector {
	return &MemoryConnector{name: name, reg: reg}
}

// Connect marks the connector live. The underlying channel is lazy.
func (m *MemoryConnector) Connect(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = true
	return nil
}

// Disconnect marks the connector closed. It does NOT close the underlying
// channel because other connectors may share it.
func (m *MemoryConnector) Disconnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = false
	return nil
}

// SendMessage pushes body onto the named queue. Returns ErrNotConnected if
// Connect hasn't been called.
func (m *MemoryConnector) SendMessage(ctx context.Context, body []byte) error {
	if !m.alive() {
		return ErrNotConnected
	}
	q := m.reg.Queue(m.name)
	select {
	case q <- append([]byte(nil), body...): // copy so callers can mutate
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReceiveMessage blocks until a message is available on the named queue.
func (m *MemoryConnector) ReceiveMessage(ctx context.Context) ([]byte, error) {
	if !m.alive() {
		return nil, ErrNotConnected
	}
	q := m.reg.Queue(m.name)
	select {
	case msg, ok := <-q:
		if !ok {
			return nil, fmt.Errorf("memory: queue %q closed", m.name)
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Ping returns nil while the connector is live.
func (m *MemoryConnector) Ping(_ context.Context) error {
	if !m.alive() {
		return ErrNotConnected
	}
	return nil
}

func (m *MemoryConnector) alive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

// Compile-time check.
var _ Connector = (*MemoryConnector)(nil)

// ErrMemoryRegistryRequired is returned by attempts to use the in-memory
// connector without first wiring up a registry. Tests should call
// NewMemoryRegistry once and share it.
var ErrMemoryRegistryRequired = errors.New("memory: registry not set")
