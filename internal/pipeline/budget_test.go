package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestBudget_AdmitsUpToLimit — the first `limit` calls within a
// window pass without blocking; subsequent calls block until the
// window rolls.
func TestBudget_AdmitsUpToLimit(t *testing.T) {
	b := newBudget(3, time.Hour)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := b.take(ctx); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	// The 4th call would block for ~1 hour; use a short ctx so we
	// can observe the block without waiting it out.
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := b.take(short); err == nil {
		t.Error("4th take should have blocked past the short ctx deadline")
	}
}

// TestBudget_WindowRolls — once the window elapses the bucket
// refills. Use a short window so the test runs fast.
func TestBudget_WindowRolls(t *testing.T) {
	b := newBudget(2, 100*time.Millisecond)
	ctx := context.Background()
	_ = b.take(ctx)
	_ = b.take(ctx)
	// Wait past the window; the next take should succeed without
	// blocking.
	time.Sleep(120 * time.Millisecond)
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := b.take(short); err != nil {
		t.Errorf("post-window take should pass, got %v", err)
	}
}

// TestBudget_RespectsCancel — a take blocked waiting for the window
// to roll returns ctx.Err() promptly on cancel.
func TestBudget_RespectsCancel(t *testing.T) {
	b := newBudget(1, time.Hour)
	ctx := context.Background()
	_ = b.take(ctx) // exhausts the bucket

	cancelled, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- b.take(cancelled)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("take did not return after cancel")
	}
}

// TestBudget_SharedAcrossGoroutines — N goroutines competing for the
// same budget see at most `limit` admissions before the next window.
// This is the property that makes the per-pipeline cap actually
// per-pipeline rather than per-worker.
func TestBudget_SharedAcrossGoroutines(t *testing.T) {
	b := newBudget(5, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var admitted atomic.Int64
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			if err := b.take(ctx); err == nil {
				admitted.Add(1)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	if got := admitted.Load(); got != 5 {
		t.Errorf("expected exactly 5 admissions, got %d", got)
	}
}
