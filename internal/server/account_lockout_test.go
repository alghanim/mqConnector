package server

import (
	"strings"
	"testing"
	"time"
)

// TestAccountLockout_LocksAfterMaxFailures — the canonical property:
// after MaxFailures consecutive failures, allow returns false until
// the lockout duration elapses.
func TestAccountLockout_LocksAfterMaxFailures(t *testing.T) {
	l := newAccountLockout(3, time.Hour, 100*time.Millisecond)

	for i := 0; i < 3; i++ {
		ok, _ := l.allow("alice")
		if !ok {
			t.Fatalf("attempt %d should pass", i)
		}
		l.recordFailure("alice")
	}
	ok, retryAfter := l.allow("alice")
	if ok {
		t.Fatal("expected lockout after MaxFailures")
	}
	if retryAfter <= 0 || retryAfter > 100*time.Millisecond {
		t.Errorf("retryAfter out of range: %v", retryAfter)
	}

	// After the lockout duration, allow returns true again.
	time.Sleep(120 * time.Millisecond)
	if ok, _ := l.allow("alice"); !ok {
		t.Error("expected unlock after duration")
	}
}

// TestAccountLockout_SuccessResetsCounter — a successful login wipes
// the failure history so a legitimate user fat-fingering their
// password twice doesn't end up two failures away from a lockout
// forever.
func TestAccountLockout_SuccessResetsCounter(t *testing.T) {
	l := newAccountLockout(3, time.Hour, time.Hour)
	l.recordFailure("alice")
	l.recordFailure("alice")
	l.recordSuccess("alice") // reset

	// Three more failures should be needed to lock again, not one.
	l.recordFailure("alice")
	l.recordFailure("alice")
	ok, _ := l.allow("alice")
	if !ok {
		t.Error("counter should have reset after successful login")
	}
}

// TestAccountLockout_PerUsername — alice's failures don't affect
// bob. The whole point of the per-username bucket is to prevent
// distributed stuffing on a single account.
func TestAccountLockout_PerUsername(t *testing.T) {
	l := newAccountLockout(2, time.Hour, time.Hour)
	l.recordFailure("alice")
	l.recordFailure("alice")
	// alice is locked.
	if ok, _ := l.allow("alice"); ok {
		t.Fatal("alice should be locked")
	}
	// bob is not.
	if ok, _ := l.allow("bob"); !ok {
		t.Error("bob should not be affected by alice's lockout")
	}
}

// TestAccountLockout_CaseInsensitive — an attacker can't iterate
// "Alice", "ALICE", "alice" to multiply the budget.
func TestAccountLockout_CaseInsensitive(t *testing.T) {
	l := newAccountLockout(2, time.Hour, time.Hour)
	l.recordFailure("Alice")
	l.recordFailure("ALICE")
	if ok, _ := l.allow("alice"); ok {
		t.Error("case variants should share a bucket")
	}
}

// TestAccountLockout_WindowExpires — five failures spread across
// a long timeframe shouldn't lock; the window slides.
func TestAccountLockout_WindowExpires(t *testing.T) {
	l := newAccountLockout(3, 50*time.Millisecond, time.Hour)
	l.recordFailure("alice")
	l.recordFailure("alice")
	// Wait past the window — the next failure starts a fresh count.
	time.Sleep(60 * time.Millisecond)
	l.recordFailure("alice")
	if ok, _ := l.allow("alice"); !ok {
		t.Error("expired window should reset the count")
	}
}

// TestAccountLockout_EmptyUsername — defensive: an empty username
// must not panic and must not block legitimate flows. The handler
// rejects empty-username requests elsewhere.
func TestAccountLockout_EmptyUsername(t *testing.T) {
	l := newAccountLockout(1, time.Hour, time.Hour)
	l.recordFailure("")
	if ok, _ := l.allow(""); !ok {
		t.Error("empty username should not be lockable")
	}
}

// TestRetryAfterSeconds_FloorOne — Retry-After=0 means "retry now",
// which defeats the lockout signal. We always floor at 1 second.
func TestRetryAfterSeconds_FloorOne(t *testing.T) {
	if got := retryAfterSeconds(0); got != "1" {
		t.Errorf("zero duration → %q, want 1", got)
	}
	if got := retryAfterSeconds(500 * time.Millisecond); got != "1" {
		t.Errorf("sub-second → %q, want 1", got)
	}
	if got := retryAfterSeconds(75 * time.Second); !strings.HasPrefix(got, "7") {
		t.Errorf("75s → %q, expected ~75", got)
	}
}
