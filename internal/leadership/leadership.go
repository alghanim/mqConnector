// Package leadership provides a SQLite-backed lease so multiple
// mqConnector replicas pointed at the same database don't both run the
// pipeline workers (which would double-consume from source queues).
//
// Design:
//   - Single row in the `leadership` table holds the current leader's id,
//     a monotonic generation counter, and a lease expiry.
//   - A would-be leader competes by issuing an UPDATE that is conditional
//     on either (a) the row not existing, (b) the row existing but expired,
//     or (c) the row's holder matching us (renewal). Whichever transaction
//     commits first wins because SQLite serialises writes — losers see
//     RowsAffected == 0 and back off.
//   - The leader runs a renewal loop at half the lease TTL. If a renewal
//     fails (DB hiccup, process pause exceeding the TTL, etc.) the loop
//     drops leadership, notifies the consumer via OnLost, and returns to
//     competing.
//   - Consumers (e.g. pipeline.Manager) listen on IsLeader() / Channel()
//     to start or stop their workers.
//
// The leader id is a stable string from config (typically the hostname or
// a config-supplied replica id). Empty / colliding ids are tolerated as
// long as only one runs at a time — but operators should set distinct
// values so the audit log + /api/leadership endpoint can tell replicas
// apart.
package leadership

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Lease is the lock-like primitive. Construct with New, then call Run in
// its own goroutine; the cancelled-context flow tears it down cleanly.
type Lease struct {
	db        *sql.DB
	id        string
	ttl       time.Duration
	logger    *slog.Logger

	mu       sync.RWMutex
	isLeader atomic.Bool
	state    State
	onChange chan State
}

// State is what's currently in the leadership row, plus our role.
type State struct {
	Self     string    `json:"self"`
	Holder   string    `json:"holder"`
	IsLeader bool      `json:"is_leader"`
	Expires  time.Time `json:"expires_at"`
}

// New constructs a Lease.
//
// id should be unique per replica (hostname is a fine default).
// ttl is how long a lease lives between renewals; pick a value larger than
// your worst pause-the-process budget (GC pauses, networked-disk hiccups)
// but small enough that takeover after a crashed leader is bounded — 30s
// is a reasonable default.
func New(db *sql.DB, id string, ttl time.Duration, logger *slog.Logger) *Lease {
	if logger == nil {
		logger = slog.Default()
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Lease{
		db:       db,
		id:       id,
		ttl:      ttl,
		logger:   logger.With("component", "leadership", "self", id),
		onChange: make(chan State, 8),
	}
}

// IsLeader reports whether this replica currently holds the lease.
func (l *Lease) IsLeader() bool {
	return l.isLeader.Load()
}

// Snapshot returns the most recent observed state.
func (l *Lease) Snapshot() State {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := l.state
	s.IsLeader = l.isLeader.Load()
	s.Self = l.id
	return s
}

// Changes returns a channel that emits a State whenever the role flips.
// Callers must drain it; emissions are dropped if the buffer fills up.
func (l *Lease) Changes() <-chan State {
	return l.onChange
}

// Migrate creates the leadership table if it's missing. Idempotent.
func Migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS leadership (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			holder TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			generation INTEGER NOT NULL DEFAULT 0
		)`)
	return err
}

// Run blocks until ctx is cancelled. It first races to acquire the
// lease, then renews it at ttl/2. If renewal fails, it relinquishes
// leadership and competes again on the next tick.
func (l *Lease) Run(ctx context.Context) error {
	if err := Migrate(ctx, l.db); err != nil {
		return fmt.Errorf("leadership: migrate: %w", err)
	}

	tick := l.ttl / 2
	if tick < 250*time.Millisecond {
		tick = 250 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()

	// First attempt right away — don't make the operator wait a full tick
	// on a fresh boot.
	l.attempt(ctx)
	for {
		select {
		case <-ctx.Done():
			l.release(ctx)
			return nil
		case <-t.C:
			l.attempt(ctx)
		}
	}
}

func (l *Lease) attempt(ctx context.Context) {
	now := time.Now().UTC()
	newExpires := now.Add(l.ttl)

	// Race-conditional UPDATE: only succeeds if either no row exists yet,
	// the row is expired, or we already hold it. SQLite gives us
	// serialisable writes so exactly one updater wins.
	//
	// Two-step: try INSERT…ON CONFLICT DO UPDATE … WHERE condition.
	res, err := l.db.ExecContext(ctx, `
		INSERT INTO leadership (id, holder, expires_at, generation)
		VALUES (1, ?, ?, 1)
		ON CONFLICT(id) DO UPDATE SET
			holder = excluded.holder,
			expires_at = excluded.expires_at,
			generation = leadership.generation + 1
		WHERE leadership.holder = excluded.holder
		   OR leadership.expires_at < ?`,
		l.id, newExpires, now)
	if err != nil {
		l.logger.Warn("leadership upsert failed", "err", err)
		l.demote(ctx)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Someone else holds it. Just refresh our view.
		l.refreshState(ctx, false)
		l.demote(ctx)
		return
	}

	l.refreshState(ctx, true)
	if !l.isLeader.Swap(true) {
		l.logger.Info("acquired leadership", "expires", newExpires)
		l.emit(l.Snapshot())
	}
}

func (l *Lease) release(ctx context.Context) {
	if !l.isLeader.Load() {
		return
	}
	_, err := l.db.ExecContext(ctx,
		`UPDATE leadership SET holder = '', expires_at = ? WHERE holder = ?`,
		time.Now().UTC().Add(-time.Second), l.id)
	if err != nil {
		l.logger.Warn("leadership release failed", "err", err)
	}
	l.demote(ctx)
}

func (l *Lease) demote(ctx context.Context) {
	if l.isLeader.Swap(false) {
		l.logger.Info("lost leadership")
		l.emit(l.Snapshot())
	}
}

func (l *Lease) refreshState(ctx context.Context, weAreLeader bool) {
	row := l.db.QueryRowContext(ctx,
		`SELECT holder, expires_at FROM leadership WHERE id = 1`)
	var holder string
	var expires time.Time
	if err := row.Scan(&holder, &expires); err != nil && !errors.Is(err, sql.ErrNoRows) {
		l.logger.Warn("leadership refresh failed", "err", err)
		return
	}
	l.mu.Lock()
	l.state = State{Self: l.id, Holder: holder, Expires: expires}
	l.mu.Unlock()
	_ = weAreLeader
}

func (l *Lease) emit(s State) {
	select {
	case l.onChange <- s:
	default:
		// Channel full — drop. State() is the authoritative source anyway.
	}
}
