package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// loginLimiter caps how many login attempts a single client IP can make per
// window. The default policy is 10 attempts per minute, which gracefully
// handles operator typos while blocking simple credential-stuffing.
//
// The implementation is a fixed-window counter — simpler than a token bucket,
// and sufficient for the brute-force class of threat we're defending against.
// State is process-local; if mqconnector ever runs in a multi-replica
// deployment, push this to a shared store (Redis, etc.) or front it with a
// proper WAF.
type loginLimiter struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	hits   map[string]*limiterEntry
}

type limiterEntry struct {
	count       int
	windowStart time.Time
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	if limit <= 0 {
		limit = 10
	}
	if window <= 0 {
		window = time.Minute
	}
	return &loginLimiter{
		window: window,
		limit:  limit,
		hits:   map[string]*limiterEntry{},
	}
}

// allow returns true when the client is under the per-window cap. The
// counter is incremented as a side effect, so call this once per request.
func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	entry, ok := l.hits[ip]
	if !ok || now.Sub(entry.windowStart) >= l.window {
		l.hits[ip] = &limiterEntry{count: 1, windowStart: now}
		return true
	}
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	return true
}

// gc periodically prunes entries whose window has elapsed. Called from a
// goroutine started by the server constructor.
func (l *loginLimiter) gc(stop <-chan struct{}) {
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

// clientIP picks the remote address for rate-limiting purposes. The server
// honours X-Forwarded-For only when explicitly configured to trust a proxy —
// otherwise we use the direct connection's RemoteAddr so a malicious client
// can't spoof its own bucket key.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
