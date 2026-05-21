//go:build integration

package storage

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mqConnector/internal/leadership"
)

// TestPostgresOpen_AppliesMigrations proves the Postgres dispatch
// path: pgx connects, migrate() applies every migration, the
// schema_migrations bookkeeping reflects the count.
//
// Skipped by default. To run:
//
//   docker run -d --rm -p 5432:5432 -e POSTGRES_PASSWORD=mqc postgres:16
//   POSTGRES_DSN='postgres://postgres:mqc@localhost:5432/postgres?sslmode=disable' \
//     go test -tags integration -run TestPostgres ./internal/storage/...
//
// What this covers AS OF THIS COMMIT:
//   - pgx driver registration + Open()
//   - migrate() flow with dialect-aware placeholder rewriting
//   - SQLite → Postgres SQL translation for the simple substitutions
//     (INSERT OR IGNORE → ON CONFLICT DO NOTHING, BLOB → bytea)
//
// What this does NOT cover yet (out of scope for this commit; the
// repo-level SQL still uses `?` placeholders that won't bind on pgx):
//   - Per-repo CRUD against Postgres
//   - Postgres-specific concurrency tests (advisory locks, etc.)
//
// Follow-up PRs add per-repo dialect handling. See
// POSTGRES_MIGRATION.md §3 for the porting plan.
func TestPostgresOpen_AppliesMigrations(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_DSN to run; e.g. postgres://postgres:pw@localhost:5432/postgres?sslmode=disable")
	}

	// Open the store fresh — this attempts migrate() and fails the
	// test if any migration fails to translate or apply.
	s, err := Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("Open postgres: %v", err)
	}
	defer s.Close()

	// Confirm the dialect was actually detected as Postgres.
	if s.Dialect() != DialectPostgres {
		t.Errorf("dialect = %q, want %q", s.Dialect(), DialectPostgres)
	}

	// Confirm migrations recorded — at least the count we ship.
	var n int
	err = s.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	if err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != len(migrations) {
		t.Errorf("applied %d migrations, want %d", n, len(migrations))
	}

	// Sanity: a known table is queryable. We use a raw exec, not
	// the repo (which still uses `?` placeholders incompatible
	// with pgx), so this part of the test will keep passing even
	// while the repo port is in progress.
	if _, err := s.DB.ExecContext(context.Background(),
		`SELECT id FROM connections LIMIT 1`); err != nil {
		t.Errorf("connections table missing or unqueryable: %v", err)
	}
}

// TestPostgresCRUD_Connections proves the placeholder-rewriter is
// doing its job: a repo method that issues `?`-placeholder SQL hits
// pgx, which would normally reject it, but the dbWrap converts it
// to `$N` form before the driver sees it.
func TestPostgresCRUD_Connections(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_DSN to run")
	}
	s, err := Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	// Clean slate.
	_, _ = s.DB.ExecContext(context.Background(), `DELETE FROM connections`)

	ctx := context.Background()
	c := &Connection{Name: "pg-test", Type: "rabbitmq", URL: "amqp://x", QueueName: "q"}
	if err := s.Connections.Create(ctx, DefaultTenantID, c); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.ID == "" {
		t.Fatal("Create should populate ID")
	}
	got, err := s.Connections.Get(ctx, DefaultTenantID, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "pg-test" {
		t.Errorf("Get name = %q, want pg-test", got.Name)
	}
	got.Name = "renamed"
	if err := s.Connections.Update(ctx, DefaultTenantID, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := s.Connections.Delete(ctx, DefaultTenantID, c.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// TestPostgresAudit_SerialisableChain verifies the serialisable
// audit path: even with concurrent inserts on Postgres the chain
// stays linear (each row's prev_hash matches an earlier row's hash).
// The retry loop in insertSerialised should handle 40001 conflicts
// transparently.
func TestPostgresAudit_SerialisableChain(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_DSN to run")
	}
	s, err := Open(dsn, 8, 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	_, _ = s.DB.ExecContext(context.Background(),
		`DELETE FROM audit_log WHERE tenant_id = $1`, DefaultTenantID)

	ctx := context.Background()
	const writers = 8
	const perWriter = 10
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = s.Audit.Insert(ctx, &AuditEntry{
					Actor:    "concurrent-writer",
					Action:   "POST",
					Resource: "/x",
					At:       time.Now().UTC(),
				})
			}
		}()
	}
	wg.Wait()

	// Verify the chain is intact.
	statuses, err := s.Audit.Verify(ctx, DefaultTenantID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Status != "ok" {
		t.Errorf("audit chain broken under concurrent writes: %+v", statuses)
	}
}

// TestPostgresLeadership_AdvisoryLockSerialisesClaim runs three
// would-be leaders against the same Postgres database and confirms
// at most one holds the lease at any moment. The lease uses the
// advisory-lock-wrapped INSERT…ON CONFLICT path on Postgres; this
// test is what catches a regression where two replicas pass the
// WHERE clause concurrently and both stamp themselves as leader.
//
// Convergence is loose-timed (500ms windows × 3 ticks) — the lease
// internally renews at ttl/2 so 1-second TTL means 500ms ticks.
// We sample each replica's IsLeader() flag every 100ms for 2s and
// assert no observation showed more than one leader.
func TestPostgresLeadership_AdvisoryLockSerialisesClaim(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_DSN to run")
	}
	// Open three separate stores so each "replica" has its own
	// connection pool — closest we can get to a multi-process
	// deploy in a single test binary.
	stores := make([]*Store, 3)
	for i := range stores {
		s, err := Open(dsn, 4, 2)
		if err != nil {
			t.Fatalf("Open replica %d: %v", i, err)
		}
		t.Cleanup(func() { _ = s.Close() })
		stores[i] = s
	}
	// Clean slate.
	_, _ = stores[0].DB.ExecContext(context.Background(),
		`DELETE FROM leadership`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	leases := make([]*leadership.Lease, 3)
	for i := range leases {
		leases[i] = leadership.NewWithDialect(stores[i].DB,
			"replica-"+string(rune('A'+i)),
			time.Second,
			leadership.DialectPostgres,
			nil)
		l := leases[i]
		go func() { _ = l.Run(ctx) }()
	}

	// Give the first claim attempt a window to complete.
	time.Sleep(300 * time.Millisecond)

	// Sample for 2 seconds. At each tick, count how many replicas
	// believe they're leader. The maximum observed count is what
	// we care about — a regression that lets two replicas both
	// pass the WHERE will spike this above 1.
	var maxLeaders atomic.Int32
	const samples = 20
	for i := 0; i < samples; i++ {
		count := int32(0)
		for _, l := range leases {
			if l.IsLeader() {
				count++
			}
		}
		if count > maxLeaders.Load() {
			maxLeaders.Store(count)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := maxLeaders.Load(); got > 1 {
		t.Errorf("observed %d concurrent leaders; lease serialisation broken", got)
	}
	// And at least ONE replica should have claimed it — otherwise
	// the lease itself isn't running.
	if maxLeaders.Load() == 0 {
		t.Error("no replica claimed leadership within sampling window")
	}
}
