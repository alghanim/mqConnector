package server

import (
	"testing"
	"time"
)

// TestTenantLimiter_DefaultCap confirms the limiter rejects once the
// per-tenant default cap is exhausted within the window.
func TestTenantLimiter_DefaultCap(t *testing.T) {
	l := newTenantLimiter(3, time.Minute, nil)
	for i := 0; i < 3; i++ {
		if !l.allow("tenant-a") {
			t.Fatalf("under-cap call %d rejected", i+1)
		}
	}
	if l.allow("tenant-a") {
		t.Fatal("expected rejection past the cap")
	}
}

// TestTenantLimiter_PerTenantIsolation makes sure one tenant burning
// through its budget doesn't affect another.
func TestTenantLimiter_PerTenantIsolation(t *testing.T) {
	l := newTenantLimiter(2, time.Minute, nil)
	if !l.allow("a") || !l.allow("a") {
		t.Fatal("tenant a should have a 2-budget")
	}
	if l.allow("a") {
		t.Fatal("tenant a should be rejected past 2")
	}
	if !l.allow("b") {
		t.Fatal("tenant b should still have budget")
	}
}

// TestTenantLimiter_Override exercises the per-tenant max override.
func TestTenantLimiter_Override(t *testing.T) {
	override := func(id string) int {
		if id == "premium" {
			return 5
		}
		return 0
	}
	l := newTenantLimiter(2, time.Minute, override)
	for i := 0; i < 5; i++ {
		if !l.allow("premium") {
			t.Fatalf("premium tenant rejected at call %d", i+1)
		}
	}
	if l.allow("premium") {
		t.Fatal("premium tenant should be rejected past the override cap")
	}

	// Default-tier tenant still capped at 2.
	for i := 0; i < 2; i++ {
		if !l.allow("default") {
			t.Fatalf("default tenant rejected at call %d", i+1)
		}
	}
	if l.allow("default") {
		t.Fatal("default tenant should be rejected past the default cap")
	}
}

// TestTenantLimiter_EmptyTenantPassesThrough ensures unauthenticated /
// pre-auth requests aren't blocked by the tenant limiter.
func TestTenantLimiter_EmptyTenantPassesThrough(t *testing.T) {
	l := newTenantLimiter(1, time.Minute, nil)
	for i := 0; i < 10; i++ {
		if !l.allow("") {
			t.Fatalf("empty tenant call %d should pass through", i+1)
		}
	}
}
