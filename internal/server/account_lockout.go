package server

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// retryAfterSeconds formats a duration as a whole-second Retry-After
// header value, with a minimum of 1 second so a caller never sees
// "0" (which means "retry immediately" and defeats the lockout
// signalling).
func retryAfterSeconds(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 1 {
		s = 1
	}
	return strconv.Itoa(s)
}

// accountLockout is a per-username cap on consecutive failed login
// attempts. The per-IP loginLimiter slows credential stuffing from a
// single source; accountLockout closes the complementary gap of
// distributed stuffing — N hosts each attempting one password
// against the same username.
//
// Policy:
//
//   - Up to MaxFailures consecutive failures from any source within
//     a sliding Window. After the cap, every login attempt for that
//     username is rejected for LockoutDuration regardless of
//     credentials.
//   - A successful login resets the counter.
//   - Cleanup goroutine prunes expired buckets.
//
// Defaults: 5 failures within 5 minutes → 15-minute lockout. Tunable
// via config; an operator running a small department deployment may
// want stricter, a noisy CI environment may want looser.
type accountLockout struct {
	mu              sync.Mutex
	maxFailures     int
	window          time.Duration
	lockoutDuration time.Duration
	entries         map[string]*lockoutEntry
}

type lockoutEntry struct {
	failures    int
	firstFailed time.Time
	lockedUntil time.Time
}

func newAccountLockout(maxFailures int, window, lockout time.Duration) *accountLockout {
	if maxFailures <= 0 {
		maxFailures = 5
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	if lockout <= 0 {
		lockout = 15 * time.Minute
	}
	return &accountLockout{
		maxFailures:     maxFailures,
		window:          window,
		lockoutDuration: lockout,
		entries:         map[string]*lockoutEntry{},
	}
}

// normalisedKey lowercases the username so "Alice" and "alice" share
// a bucket — otherwise an attacker tries casing variants to multiply
// the budget.
func (l *accountLockout) normalisedKey(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// allow reports whether a login attempt for username should proceed.
// Returns (false, retryAfter) when the account is currently locked.
// The caller is expected to invoke recordFailure or recordSuccess
// after the credential check completes.
func (l *accountLockout) allow(username string) (bool, time.Duration) {
	key := l.normalisedKey(username)
	if key == "" {
		// Empty username — the handler rejects it elsewhere; we
		// pass through so the limiter doesn't crash on the empty
		// key.
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return true, 0
	}
	if !e.lockedUntil.IsZero() && time.Now().Before(e.lockedUntil) {
		return false, time.Until(e.lockedUntil)
	}
	return true, 0
}

func (l *accountLockout) recordFailure(username string) {
	key := l.normalisedKey(username)
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e, ok := l.entries[key]
	if !ok || now.Sub(e.firstFailed) > l.window {
		// Fresh window: start counting again.
		l.entries[key] = &lockoutEntry{
			failures:    1,
			firstFailed: now,
		}
		return
	}
	e.failures++
	if e.failures >= l.maxFailures {
		e.lockedUntil = now.Add(l.lockoutDuration)
	}
}

func (l *accountLockout) recordSuccess(username string) {
	key := l.normalisedKey(username)
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// gc prunes stale entries. Runs forever until stop closes.
func (l *accountLockout) gc(stop <-chan struct{}) {
	t := time.NewTicker(l.window)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			l.mu.Lock()
			for k, e := range l.entries {
				// Drop entries that have expired their window AND
				// any associated lockout — we don't want to keep
				// counter state for a user who hasn't tried in
				// hours.
				if now.Sub(e.firstFailed) > l.window && now.After(e.lockedUntil) {
					delete(l.entries, k)
				}
			}
			l.mu.Unlock()
		}
	}
}
