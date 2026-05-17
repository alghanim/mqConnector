package server

import (
	"testing"
	"time"
)

// TestSensitiveLimiter_PerTenantPerRoute — the bucket is keyed on the
// (tenant, route) pair so a tenant hammering /preview can't exhaust
// their /config/import budget. This is the property that makes the
// limiter useful: independent budgets per high-blast-radius route.
func TestSensitiveLimiter_PerTenantPerRoute(t *testing.T) {
	l := newSensitiveLimiter(3, time.Hour)

	const tenant = "t1"
	// Exhaust /preview for tenant t1.
	for i := 0; i < 3; i++ {
		if !l.allow(tenant, "/preview") {
			t.Fatalf("call %d for /preview rejected before limit", i)
		}
	}
	if l.allow(tenant, "/preview") {
		t.Error("/preview should be exhausted after 3 calls")
	}
	// Same tenant, different route — independent budget.
	if !l.allow(tenant, "/config/import") {
		t.Error("/config/import should still have budget — different route")
	}
}

// TestSensitiveLimiter_PerTenantIsolation — exhausting one tenant
// does not affect another. The limiter is per-tenant, not per-IP, so
// noisy tenant A can't deny service to tenant B.
func TestSensitiveLimiter_PerTenantIsolation(t *testing.T) {
	l := newSensitiveLimiter(2, time.Hour)

	for i := 0; i < 2; i++ {
		l.allow("t1", "/preview")
	}
	if l.allow("t1", "/preview") {
		t.Error("t1 should be exhausted")
	}
	if !l.allow("t2", "/preview") {
		t.Error("t2 should have its own budget")
	}
}

// TestSensitiveLimiter_FailsClosedOnEmptyTenant — a request that
// arrives without a resolved tenant (somehow bypassed RequireSession)
// must NOT pass. Anonymous high-blast-radius calls would defeat the
// limiter's purpose.
func TestSensitiveLimiter_FailsClosedOnEmptyTenant(t *testing.T) {
	l := newSensitiveLimiter(100, time.Hour)
	if l.allow("", "/preview") {
		t.Error("empty tenant should fail closed")
	}
}

// TestSensitiveLimiter_WindowResets — after the window elapses the
// bucket refills. Sized for one-second windows to keep the test fast.
func TestSensitiveLimiter_WindowResets(t *testing.T) {
	l := newSensitiveLimiter(1, 100*time.Millisecond)
	if !l.allow("t1", "/preview") {
		t.Fatal("first call should pass")
	}
	if l.allow("t1", "/preview") {
		t.Fatal("second call should be capped")
	}
	time.Sleep(150 * time.Millisecond)
	if !l.allow("t1", "/preview") {
		t.Error("call after window reset should pass")
	}
}
