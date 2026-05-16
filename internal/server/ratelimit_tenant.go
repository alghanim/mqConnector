package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// tenantLimiter caps how many state-changing requests a tenant can
// issue per window. Read-only GETs are exempt — they don't consume
// noticeable backend work and rate-limiting them would harm dashboard
// polling pages. The bucket key is the resolved tenant id (not the
// user) so a malicious member can't spend the whole tenant's budget
// from one session.
//
// Fixed-window counter, like loginLimiter — both choose simplicity
// over the burst smoothness of a true token bucket, and that's fine
// at the limits we enforce (admin actions per minute are O(10)).
//
// The default per-tenant limit is conservative; tenants with a
// higher MaxMsgsPerMinute on their row get that value instead. A
// MaxMsgsPerMinute of 0 means "default" (unlimited would be a
// configuration footgun); operators set it explicitly when they
// want to widen the cap.
type tenantLimiter struct {
	mu             sync.Mutex
	defaultLimit   int
	window         time.Duration
	hits           map[string]*limiterEntry
	tenantOverride func(tenantID string) int
}

func newTenantLimiter(defaultLimit int, window time.Duration, override func(string) int) *tenantLimiter {
	if defaultLimit <= 0 {
		defaultLimit = 120 // 120 admin actions / minute / tenant
	}
	if window <= 0 {
		window = time.Minute
	}
	return &tenantLimiter{
		defaultLimit:   defaultLimit,
		window:         window,
		hits:           map[string]*limiterEntry{},
		tenantOverride: override,
	}
}

func (l *tenantLimiter) allow(tenantID string) bool {
	if tenantID == "" {
		// No tenant resolved (pre-auth path or system endpoint) —
		// rate limiting is handled elsewhere or doesn't apply.
		return true
	}
	limit := l.defaultLimit
	if l.tenantOverride != nil {
		if v := l.tenantOverride(tenantID); v > 0 {
			limit = v
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	entry, ok := l.hits[tenantID]
	if !ok || now.Sub(entry.windowStart) >= l.window {
		l.hits[tenantID] = &limiterEntry{count: 1, windowStart: now}
		return true
	}
	if entry.count >= limit {
		return false
	}
	entry.count++
	return true
}

// gc prunes stale tenant buckets so a deleted tenant doesn't keep
// counter state in memory forever.
func (l *tenantLimiter) gc(stop <-chan struct{}) {
	t := time.NewTicker(l.window)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			l.mu.Lock()
			for k, e := range l.hits {
				if now.Sub(e.windowStart) >= 2*l.window {
					delete(l.hits, k)
				}
			}
			l.mu.Unlock()
		}
	}
}

// rateLimitTenant wraps state-changing handlers. GETs and unscoped
// (no tenant in context) requests pass through; everything else
// checks the per-tenant bucket. 429 includes a Retry-After hint.
func (s *Server) rateLimitTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cheap exemption: GET/HEAD/OPTIONS don't consume the bucket.
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		tenant := auth.TenantID(r.Context())
		if s.tenantLimiter == nil || !s.tenantLimiter.allow(tenant) {
			if s.tenantLimiter != nil {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, "tenant rate limit exceeded")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// tenantOverrideFromStore returns a callback that reads
// tenant.MaxMsgsPerMinute. Wired by Server.New when the store is
// available so the limiter respects per-tenant configuration without
// the limiter itself needing a storage dependency.
func tenantOverrideFromStore(store *storage.Store) func(string) int {
	if store == nil {
		return nil
	}
	return func(tenantID string) int {
		// Best-effort lookup with a short timeout: a missed override
		// degrades to the default cap, which is the safest failure
		// mode (lower throughput, never higher).
		if store.Tenants == nil {
			return 0
		}
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		t, err := store.Tenants.Get(ctx, tenantID)
		if err != nil || t == nil {
			return 0
		}
		return t.MaxMsgsPerMinute
	}
}
