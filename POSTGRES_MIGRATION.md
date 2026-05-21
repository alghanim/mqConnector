# Postgres Migration Plan

mqConnector currently ships with SQLite as the only backend. The dispatcher in `internal/storage/storage.go` recognises a Postgres DSN (`postgres://…` / `postgresql://…`) and errors clearly: "the postgres backend is not yet wired — see POSTGRES_MIGRATION.md." This document is that path.

Goal: a single binary that can be pointed at SQLite (today, single-node) or Postgres (Phase 18+, multi-replica / large-tenant) without changing application code outside this package.

---

## 1. Why move to Postgres

- **SQLite is single-writer.** Under the millions-of-msg/min target with multi-tenant write traffic, the writer lock will dominate p99 latency. Postgres' MVCC handles concurrent writers cleanly.
- **Leadership lease in HA.** The current `internal/leadership` lease works on SQLite via timestamp-and-update under serialisable txns. Postgres lets us swap it for `SELECT … FOR UPDATE` or advisory locks, which are simpler and less prone to clock-skew false-positives.
- **Operational ergonomics.** Backups, point-in-time recovery, replication, monitoring — all are first-class on Postgres and operationally painful on SQLite.
- **Audit archival.** Streaming old audit rows to object storage is easier when the source supports `LISTEN/NOTIFY` and logical replication.

---

## 2. Dialect surface to bridge

Every place SQLite-isms appear, marked with a `// dialect:` tag in code as they're added. The current divergences:

| SQLite                                  | Postgres                                          |
| --------------------------------------- | ------------------------------------------------- |
| `?` placeholders                        | `$1, $2, …`                                       |
| `INSERT OR IGNORE`                      | `INSERT … ON CONFLICT DO NOTHING`                 |
| `INSERT OR REPLACE`                     | `INSERT … ON CONFLICT … DO UPDATE`                |
| `INTEGER` for booleans                  | `BOOLEAN`                                         |
| `DATETIME DEFAULT CURRENT_TIMESTAMP`    | `TIMESTAMPTZ DEFAULT NOW()`                       |
| `TEXT` for JSON                         | `JSONB`                                           |
| `ALTER TABLE … ADD COLUMN … DEFAULT …`  | works identically; `NOT NULL` requires backfill   |
| `journal_mode=WAL`                      | n/a (Postgres always uses WAL)                    |
| `_pragma=foreign_keys(on)`              | always on                                         |
| `INTEGER PRIMARY KEY AUTOINCREMENT`     | `BIGSERIAL PRIMARY KEY` (we use UUIDs anyway)     |

The `rewritePlaceholders` helper in `dialect.go` already handles `?` → `$N` and is unit-tested.

---

## 3. Migration steps

### Step 1 — add the driver (one-time op)

```sh
go get github.com/jackc/pgx/v5/stdlib
```

Wire it in `storage.go`:

```go
import _ "github.com/jackc/pgx/v5/stdlib"

// in Open():
case DialectPostgres:
    db, err := sql.Open("pgx", dsn)
```

The pgx stdlib adapter speaks `database/sql`, so the rest of the repo code stays unchanged for any query that doesn't hit the divergences listed above.

### Step 2 — duplicate the migrations under a Postgres dialect

`internal/storage/migrations.go` currently holds SQLite SQL. Split it:

```
internal/storage/migrations_sqlite.go     ← existing
internal/storage/migrations_postgres.go   ← new
internal/storage/migrations.go            ← dispatcher
```

The Postgres variants:

- `INTEGER NOT NULL DEFAULT 0` → `BOOLEAN NOT NULL DEFAULT false` for the flag columns (`tls_insecure_skip_verify`, `enabled`).
- `DATETIME DEFAULT CURRENT_TIMESTAMP` → `TIMESTAMPTZ DEFAULT NOW()`.
- `INSERT OR IGNORE` → `INSERT … ON CONFLICT DO NOTHING`.
- `TEXT` for JSON columns (filter_paths, stage_config, audit_log_diffs) → `JSONB` once we want index-on-content.

### Step 3 — repo methods that use dialect-specific SQL

Mostly the existing repo methods are dialect-agnostic. Known exceptions:

- `tenants.go::Upsert` (uses `INSERT OR IGNORE`)
- `memberships.go::Upsert` (same)
- `audit_diffs::SaveDiff` (uses `INSERT OR REPLACE`)

Refactor: add a `r.q(sql)` helper on each repo that calls `rewritePlaceholders(sql, r.dialect)`. For the conflict-resolution variants, branch:

```go
if r.dialect == DialectPostgres {
    sql = `INSERT INTO t (...) VALUES (...) ON CONFLICT (...) DO NOTHING`
}
```

### Step 4 — leadership lease

The current lease uses `UPDATE leadership SET … WHERE … AND lease_until < ?` and relies on SQLite's single-writer to serialise. Postgres needs:

```sql
SELECT id FROM leadership FOR UPDATE
```

inside a `tx.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})`, or use advisory locks:

```sql
SELECT pg_try_advisory_xact_lock(1234567890)
```

The advisory-lock path is cheaper but invisible to operators; SELECT-FOR-UPDATE is debuggable from psql.

### Step 5 — `chainMu` for audit

The in-process `chainMu` on `AuditRepo` serialises chain-head lookups + inserts. On a multi-replica Postgres deploy you'd want either:

- A serialisable transaction wrapping the SELECT + INSERT (cleaner), or
- An advisory lock keyed by tenant id.

Pick serialisable; it gives the right semantics and the audit insert isn't on the user-visible hot path.

### Step 6 — load test + cutover

```sh
# Spin up Postgres locally (or point PG_DSN at an existing instance).
docker run -d --rm -p 5432:5432 -e POSTGRES_PASSWORD=mqc postgres:16

# Default: 30 s × 8 writers per backend, both run from the same binary
# so the comparison is at the storage layer, not the HTTP stack.
./scripts/load-test.sh

# Tune via env vars.
DURATION=2m CONCURRENCY=32 ./scripts/load-test.sh
```

Implementation: `internal/storage/loadtest/` runs the same Create/Get/Update/List/Delete mix against the same `*storage.Store` API the production binary uses. The mix mirrors the admin UI's read-heavy + occasional-write profile (1 create, 1 get, 1 update, 1 list, 1 delete per worker cycle). Output: JSON to stdout (machine-readable; the runner script diffs two of them), human-readable table to stderr.

Acceptance: Postgres p99 ≤ 1.2 × SQLite p99 at the same workload. The runner script prints `PASS` or `FAIL` based on the ratio; CI can wire it as a gate before publishing a Postgres-recommended release. Override the ceiling with `P99_CEILING=1.5 ./scripts/load-test.sh` for workloads where the SQLite single-writer is already a relative win.

---

## 4. What's NOT changing

- The repository interface stays the same — every method that callers depend on (`Get`, `List`, `Create`, etc.) keeps its signature.
- The migration *content* stays one-source-of-truth: divergences are minimal and the Postgres file is a translation of the SQLite file, not a parallel design.
- The CLI surface (`mqconnector -config …`) doesn't change. The DSN in config.yaml picks the backend.

---

## 5. Test plan

1. Unit: every repo test runs against both dialects via a build tag or env-driven test container (testcontainers-go).
2. Integration: spin up a 3-replica Postgres + mqConnector deployment, kill the leader, confirm the new leader picks up within `lease_ttl` and no message is double-sent.
3. Performance: k6 workload as above, compared against a SQLite baseline on the same hardware.
4. Migration: run the SQLite → Postgres data import once (a small `mqctl migrate-data` subcommand reading SQLite and inserting into Postgres) on a populated dev DB.

---

## 6. Risks

- **Dual-dialect maintenance cost**: every new migration must land in both `migrations_sqlite.go` and `migrations_postgres.go`. Mitigated by a per-PR CI step that diffs the two files for structural drift.
- **Driver-specific behaviour**: pgx's prepared-statement caching needs `default_statement_timeout` set on the Postgres side to avoid holding statements through a connection bounce. Configurable via the DSN.
- **Operational hurdle**: an operator running the SQLite single-binary today will need to run Postgres separately if they want this. The single-binary story stays for small deployments.

---

## 7. Status

| Step                                          | State        |
| --------------------------------------------- | ------------ |
| DSN dispatcher in `Open`                      | ✅ done      |
| `rewritePlaceholders` helper                  | ✅ done      |
| pgx import + driver registration              | ✅ done      |
| `migrate()` dialect-aware (PRAGMA skip, etc.) | ✅ done      |
| Inline SQL translation (BLOB→bytea, etc.)     | ✅ done      |
| Integration test: Open + migrate              | ✅ done (`internal/storage/postgres_integration_test.go`, env-gated by `POSTGRES_DSN`) |
| Repo-level placeholder rewriting              | ✅ done via `dbWrap` (all repos converted; call sites unchanged) |
| Audit chain on serialisable                   | ✅ done (`insertSerialised` + retry on SQLSTATE 40001) |
| Migrations 0010+ recreate-and-copy            | ✅ done — `postgresMigrationOverrides` in `migrations.go` ships ALTER TABLE … DROP/ADD CONSTRAINT bodies for 0010 + 0016; the runner picks them over the SQLite recreate-and-rename when the dialect is Postgres. |
| Leadership lease on serialisable / advisory   | ✅ done — `leadership.NewWithDialect` + `attemptPostgres` wraps the upsert in `pg_try_advisory_xact_lock`. `TestPostgresLeadership_AdvisoryLockSerialisesClaim` covers a 3-replica race. |
| Postgres-specific load test                   | ✅ done — `cmd/loadtest` binary + `scripts/load-test.sh` runner. Default workload: 30 s × 8 writers; emits JSON with p50/p95/p99 + per-op breakdown for both backends and prints PASS/FAIL against the 1.2× p99 ceiling. |

### What works right now

- A Postgres DSN (`postgres://…` / `postgresql://…`) is dispatched to the pgx driver.
- `Open` connects + pings.
- Migrations 0001–0009 apply cleanly on Postgres via the SQL translator (`INSERT OR IGNORE` → `ON CONFLICT DO NOTHING`, `BLOB` → `bytea`, `DATETIME` → `TIMESTAMP`, statement-by-statement scan).
- Every repository SQL statement now passes through `dbWrap` which rewrites `?` → `$N` transparently. Repo CRUD against Postgres works.
- The audit chain has a Postgres-specific path: `insertSerialised` opens a `LevelSerializable` transaction, retries up to 5 times on SQLSTATE 40001. Concurrent writers across processes can't fork the chain.
- Integration tests (`TestPostgresCRUD_Connections`, `TestPostgresAudit_SerialisableChain`) exist but currently fail at migration 0010+ — see "What doesn't work yet" below.

The driver dispatch path:

```sh
docker run -d --rm -p 5432:5432 -e POSTGRES_PASSWORD=mqc postgres:16
POSTGRES_DSN='postgres://postgres:mqc@localhost:5432/postgres?sslmode=disable' \
  go test -tags integration -run TestPostgres ./internal/storage/...
```

### What doesn't work yet

- **Postgres-specific load test** — no k6 / vegeta workload comparing p99 latency on SQLite vs Postgres at scale. Single-replica Postgres is correctness-tested; performance characterisation is the remaining gate before declaring Postgres "production-recommended" rather than "production-supported".
- The migration runner's `applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP` works on Postgres but reads back as `time.Time{}` without timezone — repos that scan it as `time.Time` should be unaffected (database/sql round-trips zero-offset OK).

### Recommended next steps

1. Add a small `(r *Repo) rw(sql string) string` helper to each repo that wraps `rewritePlaceholders` with the dialect captured at construction.
2. Convert all `db.ExecContext(ctx, "...SQL with ?...", args...)` to `db.ExecContext(ctx, r.rw("...SQL with ?..."), args...)`. ~200 sites across the repos; trivial mechanical change.
3. Add per-repo CRUD integration tests that run against both SQLite and Postgres via a build matrix.
4. Land the leadership-lease + audit-chain Postgres-native paths before declaring Postgres a production-supported backend.
