package pipeline

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned by send() when the per-pipeline circuit
// breaker is in the Open state. processOne handles it by Nack-ing the
// source message (so the broker retains it for redelivery after the
// cool-down) and sleeping briefly instead of pushing the message to
// DLQ — a destination outage shouldn't accumulate millions of DLQ rows
// that all represent the same broker problem.
var ErrCircuitOpen = errors.New("pipeline: destination circuit open")

// breakerState is the classic three-state circuit breaker.
//
//   - Closed   — normal operation; counts consecutive failures.
//   - Open     — destination is unhappy; send() short-circuits to
//                ErrCircuitOpen, the worker Nacks + sleeps.
//   - HalfOpen — cool-down has elapsed; the next send is allowed
//                through as a probe. Success → Closed, failure → Open
//                with a fresh cool-down.
type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

// breaker is the per-pipeline outbound circuit breaker. It guards
// against thundering-herd retries when the destination broker is
// down — every worker would otherwise burn CPU and broker connections
// retrying every received message, and the DLQ would fill with rows
// that all represent "destination broker is down right now".
//
// Defaults are conservative: 5 consecutive failures trips, 30s
// cool-down. Both override-able from pipeline config in a future
// commit; for now the constants are tuned for the typical
// "destination broker restart" recovery window (a few seconds) with
// enough headroom for slower recoveries.
//
// The breaker is goroutine-safe: every worker on a pipeline shares the
// same breaker, so a probe-and-recovery happens once per pipeline, not
// once per worker.
type breaker struct {
	mu        sync.Mutex
	state     breakerState
	fails     int
	threshold int
	cooldown  time.Duration
	openedAt  time.Time
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	if threshold <= 0 {
		threshold = defaultBreakerThreshold
	}
	if cooldown <= 0 {
		cooldown = defaultBreakerCooldown
	}
	return &breaker{threshold: threshold, cooldown: cooldown}
}

const (
	defaultBreakerThreshold = 5
	defaultBreakerCooldown  = 30 * time.Second
)

// allow returns true if the caller may attempt a send. When the
// breaker is Open but the cool-down has elapsed, allow transitions
// the breaker to HalfOpen and returns true exactly once — the next
// concurrent caller waits for that probe's recordResult.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerClosed:
		return true
	case breakerHalfOpen:
		// Another worker is mid-probe. Hold off — wait for its result.
		return false
	case breakerOpen:
		if time.Since(b.openedAt) < b.cooldown {
			return false
		}
		b.state = breakerHalfOpen
		return true
	}
	return true
}

// recordResult feeds the outcome of a send back into the breaker. On
// success the breaker closes (clears the fail counter). On failure
// the breaker counts up; once we cross threshold it opens with a
// fresh cool-down.
func (b *breaker) recordResult(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		b.state = breakerClosed
		b.fails = 0
		return
	}
	if b.state == breakerHalfOpen {
		// Probe failed — straight back to Open with a fresh
		// cool-down; the destination is still unhappy.
		b.state = breakerOpen
		b.openedAt = time.Now()
		return
	}
	b.fails++
	if b.fails >= b.threshold {
		b.state = breakerOpen
		b.openedAt = time.Now()
	}
}

// state is exposed for metrics / tests; the value can change
// immediately after the call returns, so callers should treat it as
// a point-in-time snapshot.
func (b *breaker) snapshot() (breakerState, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, b.fails
}
