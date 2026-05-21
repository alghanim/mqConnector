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
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Dialect identifies the underlying DB so the lease can use the right
// SQL placeholder style and (on Postgres) wrap its claim in an
// advisory lock for explicit cross-replica serialisation. Kept here as
// a tiny enum rather than importing storage.Dialect to avoid a
// circular import — leadership has no other reason to depend on
// storage.
type Dialect int

const (
	DialectSQLite Dialect = iota
	DialectPostgres
)

// Advisory-lock key. Postgres advisory locks are 64-bit integers
// scoped to the database. Operators worried about collision with
// their own advisory locks should treat this as a reserved value;
// the upper bits are chosen so it's unlikely to overlap with
// integer-id schemes operators typically use.
const postgresAdvisoryLockKey int64 = 0x6d71636f6e6e6c64 // "mqconnld" packed

// Lease is the lock-like primitive. Construct with New, then call Run in
// its own goroutine; the cancelled-context flow tears it down cleanly.
type Lease struct {
	db      *sql.DB
	id      string
	ttl     time.Duration
	dialect Dialect
	logger  *slog.Logger

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
	return NewWithDialect(db, id, ttl, DialectSQLite, logger)
}

// NewWithDialect is the dialect-aware constructor. main.go passes the
// dialect derived from the storage DSN so the lease picks the right
// claim path. SQLite uses the existing INSERT ON CONFLICT WHERE
// pattern (single-writer makes the WHERE atomic for free); Postgres
// uses pg_try_advisory_xact_lock + a parameterised UPDATE so claims
// are serialised explicitly across replicas instead of relying on
// row-lock ordering.
func NewWithDialect(db *sql.DB, id string, ttl time.Duration, dialect Dialect, logger *slog.Logger) *Lease {
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
		dialect:  dialect,
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
// Defaults to SQLite for backwards compatibility — callers that know
// the dialect should use MigrateWithDialect.
func Migrate(ctx context.Context, db *sql.DB) error {
	return MigrateWithDialect(ctx, db, DialectSQLite)
}

// MigrateWithDialect is the dialect-aware variant. Postgres needs
// TIMESTAMP instead of SQLite's DATETIME alias to avoid the implicit
// "without time zone" conversion that pgx complains about when
// scanning back into time.Time.
func MigrateWithDialect(ctx context.Context, db *sql.DB, dialect Dialect) error {
	timestampType := "DATETIME"
	if dialect == DialectPostgres {
		timestampType = "TIMESTAMP"
	}
	_, err := db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS leadership (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			holder TEXT NOT NULL,
			expires_at %s NOT NULL,
			generation INTEGER NOT NULL DEFAULT 0
		)`, timestampType))
	return err
}

// Run blocks until ctx is cancelled. It first races to acquire the
// lease, then renews it at ttl/2. If renewal fails, it relinquishes
// leadership and competes again on the next tick.
func (l *Lease) Run(ctx context.Context) error {
	if err := MigrateWithDialect(ctx, l.db, l.dialect); err != nil {
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
	// the row is expired, or we already hold it.
	//
	// SQLite: single-writer serialisation makes the WHERE clause atomic
	// for free — exactly one INSERT…ON CONFLICT WHERE writer commits.
	//
	// Postgres: we additionally take a transaction-scoped advisory lock
	// so two replicas can't both pass the WHERE on stale snapshots. The
	// lock is released automatically when the txn commits or rolls back;
	// no separate unlock step needed.
	sql := `
		INSERT INTO leadership (id, holder, expires_at, generation)
		VALUES (1, ?, ?, 1)
		ON CONFLICT(id) DO UPDATE SET
			holder = excluded.holder,
			expires_at = excluded.expires_at,
			generation = leadership.generation + 1
		WHERE leadership.holder = excluded.holder
		   OR leadership.expires_at < ?`
	sql = l.rewrite(sql)

	var res sql2Result
	var err error
	if l.dialect == DialectPostgres {
		res, err = l.attemptPostgres(ctx, sql, now, newExpires)
	} else {
		res, err = l.db.ExecContext(ctx, sql, l.id, newExpires, now)
	}
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

// sql2Result is the minimal interface attemptPostgres / Exec share so
// the caller can read RowsAffected. sql.Result is satisfied by both
// *sql.Result-from-Exec and *sql.Result-from-tx.Exec.
type sql2Result interface {
	RowsAffected() (int64, error)
}

// attemptPostgres wraps the upsert in a transaction holding a
// transaction-scoped advisory lock. Other replicas hitting
// pg_try_advisory_xact_lock simultaneously back off (try_ returns
// false → we silently treat as "someone else has it"); the lock
// guarantees the WHERE clause sees the post-commit state of the
// leadership row, not a Read-Committed snapshot.
func (l *Lease) attemptPostgres(ctx context.Context, upsertSQL string, now, newExpires time.Time) (sql2Result, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var locked bool
	if err := tx.QueryRowContext(ctx,
		`SELECT pg_try_advisory_xact_lock($1)`, postgresAdvisoryLockKey).Scan(&locked); err != nil {
		return nil, fmt.Errorf("advisory lock: %w", err)
	}
	if !locked {
		// Another replica holds the lock for this round; report
		// no-op so the caller treats us as a non-leader.
		return zeroResult{}, nil
	}
	res, err := tx.ExecContext(ctx, upsertSQL, l.id, newExpires, now)
	if err != nil {
		return nil, fmt.Errorf("upsert: %w", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return staticResult{rows: rowsAffected}, nil
}

type zeroResult struct{}

func (zeroResult) RowsAffected() (int64, error) { return 0, nil }

type staticResult struct{ rows int64 }

func (r staticResult) RowsAffected() (int64, error) { return r.rows, nil }

func (l *Lease) release(ctx context.Context) {
	if !l.isLeader.Load() {
		return
	}
	_, err := l.db.ExecContext(ctx,
		l.rewrite(`UPDATE leadership SET holder = '', expires_at = ? WHERE holder = ?`),
		time.Now().UTC().Add(-time.Second), l.id)
	if err != nil {
		l.logger.Warn("leadership release failed", "err", err)
	}
	l.demote(ctx)
}

// rewrite converts SQLite `?` placeholders to Postgres `$N` form when
// the dialect demands it. Pulled out as a tiny helper so each query
// site stays one-line; cost is O(len(sql)) per call which is fine for
// the half-dozen short statements this package issues.
func (l *Lease) rewrite(sql string) string {
	if l.dialect != DialectPostgres {
		return sql
	}
	var b []byte
	idx := 1
	inSingle, inDouble := false, false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			b = append(b, c)
		case c == '"' && !inSingle:
			inDouble = !inDouble
			b = append(b, c)
		case c == '?' && !inSingle && !inDouble:
			b = append(b, '$')
			b = append(b, []byte(strconv.Itoa(idx))...)
			idx++
		default:
			b = append(b, c)
		}
	}
	return string(b)
}


func (l *Lease) demote(ctx context.Context) {
	if l.isLeader.Swap(false) {
		l.logger.Info("lost leadership")
		l.emit(l.Snapshot())
	}
}

func (l *Lease) refreshState(ctx context.Context, weAreLeader bool) {
	row := l.db.QueryRowContext(ctx,
		l.rewrite(`SELECT holder, expires_at FROM leadership WHERE id = 1`))
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
