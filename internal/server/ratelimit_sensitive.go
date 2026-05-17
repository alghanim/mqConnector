package server

import (
	"net/http"
	"sync"
	"time"

	"mqConnector/internal/auth"
)

// sensitiveLimiter caps requests to high-blast-radius endpoints at a
// much tighter budget than the per-tenant limiter applied to ordinary
// admin actions. Bucketed per (tenant, route) — a tenant calling
// /preview heavily can't exhaust their config-import budget.
//
// The general tenant limiter sits at 120 req/min by default; this
// sits at 6 req/min. Targets:
//
//   - POST /api/v1/config/import     full state replace
//   - POST /api/v1/secrets/rotate    key rewrap (runs across every row)
//   - POST /api/v1/preview           arbitrary stage execution (JS scripts)
//   - POST /api/v1/samples/extract   live broker read
//
// All of these are operator-initiated, low-frequency by design. A
// human clicks them; a stuck dashboard polling them is a bug we want
// to surface. The cap is deliberately low.
type sensitiveLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*limiterEntry
}

func newSensitiveLimiter(limit int, window time.Duration) *sensitiveLimiter {
	if limit <= 0 {
		limit = 6
	}
	if window <= 0 {
		window = time.Minute
	}
	return &sensitiveLimiter{
		limit:  limit,
		window: window,
		hits:   map[string]*limiterEntry{},
	}
}

// allow checks the (tenant, route) bucket. tenant is required;
// requests with no resolved tenant fail closed.
func (l *sensitiveLimiter) allow(tenantID, route string) bool {
	if tenantID == "" {
		return false
	}
	key := tenantID + "|" + route
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	entry, ok := l.hits[key]
	if !ok || now.Sub(entry.windowStart) >= l.window {
		l.hits[key] = &limiterEntry{count: 1, windowStart: now}
		return true
	}
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	return true
}

// gc prunes stale buckets so deleted tenants / unused routes don't
// keep counter state forever.
func (l *sensitiveLimiter) gc(stop <-chan struct{}) {
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

// rateLimitSensitive wraps the sensitive endpoints. Route key uses
// the *pattern* (e.g. "/api/v1/preview") rather than the resolved
// path so dashboards that hammer the same endpoint don't get
// per-resource budget. The middleware runs AFTER RequireSession so
// auth.TenantID(ctx) is populated.
func (s *Server) rateLimitSensitive(routeKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant := auth.TenantID(r.Context())
			if s.sensitiveLimiter == nil || s.sensitiveLimiter.allow(tenant, routeKey) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests,
				"rate limit exceeded for sensitive endpoint")
		})
	}
}
