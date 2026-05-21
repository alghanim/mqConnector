package pipeline

import (
	"testing"
	"time"
)

func TestBreaker_OpensAfterThresholdFailures(t *testing.T) {
	b := newBreaker(3, 50*time.Millisecond)
	for i := 0; i < 2; i++ {
		if !b.allow() {
			t.Fatalf("allow should be true while Closed; iter %d", i)
		}
		b.recordResult(false)
	}
	// Third failure trips the threshold.
	if !b.allow() {
		t.Fatal("third allow should still be true before trip")
	}
	b.recordResult(false)
	if b.allow() {
		t.Fatal("breaker should be Open after threshold failures")
	}
}

func TestBreaker_HalfOpenProbeAfterCooldown(t *testing.T) {
	b := newBreaker(2, 20*time.Millisecond)
	for i := 0; i < 2; i++ {
		_ = b.allow()
		b.recordResult(false)
	}
	if b.allow() {
		t.Fatal("breaker should be Open immediately after threshold")
	}
	time.Sleep(30 * time.Millisecond)
	if !b.allow() {
		t.Fatal("breaker should transition to HalfOpen after cool-down")
	}
	// Second concurrent caller is blocked while a probe is in flight.
	if b.allow() {
		t.Fatal("breaker should reject second probe while HalfOpen")
	}
	// Probe success closes the breaker.
	b.recordResult(true)
	if !b.allow() {
		t.Fatal("breaker should be Closed after successful probe")
	}
}

func TestBreaker_ProbeFailureReturnsToOpenWithFreshCooldown(t *testing.T) {
	b := newBreaker(1, 20*time.Millisecond)
	_ = b.allow()
	b.recordResult(false) // now Open
	time.Sleep(30 * time.Millisecond)
	if !b.allow() {
		t.Fatal("expected HalfOpen probe to be allowed")
	}
	b.recordResult(false) // probe failed
	if b.allow() {
		t.Fatal("expected breaker to be Open again immediately after probe failure")
	}
}

func TestBreaker_SuccessResetsFailureCounter(t *testing.T) {
	b := newBreaker(3, time.Second)
	_ = b.allow()
	b.recordResult(false)
	_ = b.allow()
	b.recordResult(false)
	// Success should reset the counter.
	_ = b.allow()
	b.recordResult(true)
	// Now we can take 2 more failures without tripping.
	_ = b.allow()
	b.recordResult(false)
	_ = b.allow()
	b.recordResult(false)
	if !b.allow() {
		t.Fatal("counter should have reset after success; breaker should still be Closed")
	}
}
