package pipeline

import (
	"context"
	"sync"
	"time"
)

// budget is a simple fixed-window throttle used by Executor to cap
// per-pipeline throughput. Not a token bucket — operators tune this
// at the "messages per minute" granularity, where the variance from
// fixed-vs-sliding window doesn't matter. A token bucket would let a
// caller burst 100 messages instantly after a quiet minute, which is
// the wrong shape for the "isolate a noisy pipeline" use case.
//
// Concurrency: shared across all workers of one pipeline so the cap
// is per-pipeline, not per-worker. take() blocks (respecting ctx)
// until the next slot is available.
type budget struct {
	limit  int
	window time.Duration

	mu          sync.Mutex
	count       int
	windowStart time.Time
}

func newBudget(limit int, window time.Duration) *budget {
	if window <= 0 {
		window = time.Minute
	}
	return &budget{
		limit:       limit,
		window:      window,
		windowStart: time.Now(),
	}
}

// take blocks until the budget admits one more message or ctx is
// cancelled. Sleeps until the window rolls when the bucket is dry —
// no busy-wait, no per-call goroutine.
func (b *budget) take(ctx context.Context) error {
	for {
		b.mu.Lock()
		now := time.Now()
		if now.Sub(b.windowStart) >= b.window {
			b.windowStart = now
			b.count = 0
		}
		if b.count < b.limit {
			b.count++
			b.mu.Unlock()
			return nil
		}
		// Bucket full — sleep until the window rolls over.
		wait := b.window - now.Sub(b.windowStart)
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
			// Loop and try again on the fresh window.
		}
	}
}
