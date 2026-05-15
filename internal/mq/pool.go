package mq

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Pool is a connection cache keyed by an opaque ID. Entries are reused across
// callers, health-checked at a fixed interval, and evicted when idle past the
// configured timeout.
type Pool struct {
	idleTimeout    time.Duration
	healthInterval time.Duration
	logger         *slog.Logger

	mu      sync.Mutex
	entries map[string]*poolEntry
	stop    chan struct{}
	stopped bool
}

type poolEntry struct {
	conn      Connector
	lastUsed  time.Time
	mu        sync.Mutex // serialises Send/Receive/Disconnect on this connector
}

// PoolOptions configures a pool. Zero values fall back to safe defaults.
type PoolOptions struct {
	IdleTimeout    time.Duration
	HealthInterval time.Duration
	Logger         *slog.Logger
}

// NewPool constructs a pool and starts its background sweeper goroutine.
func NewPool(opts PoolOptions) *Pool {
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 5 * time.Minute
	}
	if opts.HealthInterval <= 0 {
		opts.HealthInterval = 30 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	p := &Pool{
		idleTimeout:    opts.IdleTimeout,
		healthInterval: opts.HealthInterval,
		logger:         logger.With("component", "mq.Pool"),
		entries:        map[string]*poolEntry{},
		stop:           make(chan struct{}),
	}
	go p.sweep()
	return p
}

// Get returns a connected Connector for the given id, creating it on miss.
// The returned connector is locked for the caller's exclusive use until they
// call Release.
func (p *Pool) Get(ctx context.Context, id string, cfg Config) (Connector, func(), error) {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return nil, nil, errors.New("mq: pool stopped")
	}
	entry, ok := p.entries[id]
	if !ok {
		conn, err := New(cfg)
		if err != nil {
			p.mu.Unlock()
			return nil, nil, err
		}
		entry = &poolEntry{conn: conn}
		p.entries[id] = entry
	}
	p.mu.Unlock()

	entry.mu.Lock()
	entry.lastUsed = time.Now()
	if err := entry.conn.Connect(ctx); err != nil {
		entry.mu.Unlock()
		return nil, nil, err
	}
	release := func() {
		entry.lastUsed = time.Now()
		entry.mu.Unlock()
	}
	return entry.conn, release, nil
}

// Release explicitly disconnects and removes an entry. Safe to call on missing
// ids.
func (p *Pool) Release(id string) {
	p.mu.Lock()
	entry, ok := p.entries[id]
	if ok {
		delete(p.entries, id)
	}
	p.mu.Unlock()
	if !ok {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if err := entry.conn.Disconnect(); err != nil {
		p.logger.Warn("disconnect on release", "id", id, "err", err)
	}
}

// Close stops the sweeper and disconnects every pooled connector. Safe to
// call multiple times. Disconnects run in parallel with a short hard
// timeout — a wedged broker shouldn't be able to make process shutdown
// hang.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	close(p.stop)
	entries := p.entries
	p.entries = map[string]*poolEntry{}
	p.mu.Unlock()

	const disconnectBudget = 3 * time.Second
	done := make(chan struct{}, len(entries))
	for id, e := range entries {
		go func(id string, e *poolEntry) {
			defer func() { done <- struct{}{} }()
			e.mu.Lock()
			defer e.mu.Unlock()
			if err := e.conn.Disconnect(); err != nil {
				p.logger.Warn("disconnect on close", "id", id, "err", err)
			}
		}(id, e)
	}
	deadline := time.After(disconnectBudget)
	for i := 0; i < len(entries); i++ {
		select {
		case <-done:
		case <-deadline:
			p.logger.Warn("pool close budget exhausted; leaving open connectors behind",
				"pending", len(entries)-i)
			return
		}
	}
}

func (p *Pool) sweep() {
	ticker := time.NewTicker(p.healthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.runSweep()
		}
	}
}

func (p *Pool) runSweep() {
	p.mu.Lock()
	toCheck := make(map[string]*poolEntry, len(p.entries))
	for id, e := range p.entries {
		toCheck[id] = e
	}
	idleTimeout := p.idleTimeout
	p.mu.Unlock()

	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for id, e := range toCheck {
		if !e.mu.TryLock() {
			continue // in use right now; leave it alone
		}
		// idle eviction
		if now.Sub(e.lastUsed) > idleTimeout {
			if err := e.conn.Disconnect(); err != nil {
				p.logger.Warn("disconnect idle", "id", id, "err", err)
			}
			e.mu.Unlock()
			p.mu.Lock()
			delete(p.entries, id)
			p.mu.Unlock()
			continue
		}
		// health check
		if err := e.conn.Ping(ctx); err != nil {
			p.logger.Warn("ping failed", "id", id, "err", err)
			if err := e.conn.Disconnect(); err != nil {
				p.logger.Warn("disconnect after failed ping", "id", id, "err", err)
			}
			e.mu.Unlock()
			p.mu.Lock()
			delete(p.entries, id)
			p.mu.Unlock()
			continue
		}
		e.mu.Unlock()
	}
}

// Size returns the current pool size. Useful for tests and metrics.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// InjectForTest preseeds the pool with a pre-built Connector under id. The
// connector must already be Connect()'d. This is the documented hook for
// dlq/pipeline tests that want to assert end-to-end behaviour without a real
// broker — production code paths never reach for it.
func (p *Pool) InjectForTest(id string, conn Connector) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries[id] = &poolEntry{conn: conn, lastUsed: time.Now()}
}
