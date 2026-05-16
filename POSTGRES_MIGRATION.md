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

Add a `make load-test` target that runs k6 against both backends with the same workload (1M msgs / 5min, 4 tenants, 32 connections). Compare p99 send latency and per-stage CPU. Acceptance: Postgres at <= 1.2× SQLite p99 at half the SQLite single-writer ceiling.

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

| Step                                  | State        |
| ------------------------------------- | ------------ |
| DSN dispatcher in `Open`              | ✅ done      |
| `rewritePlaceholders` helper          | ✅ done      |
| `migrations_postgres.go`              | ⏳ TODO      |
| pgx import + driver registration      | ⏳ TODO (needs dep approval) |
| Repo-level dialect branches           | ⏳ TODO      |
| Leadership lease on serialisable      | ⏳ TODO      |
| Audit chain on serialisable           | ⏳ TODO      |
| Test plan                             | ⏳ TODO      |
