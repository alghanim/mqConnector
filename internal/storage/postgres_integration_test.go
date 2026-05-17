//go:build integration

package storage

import (
	"context"
	"os"
	"testing"
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
