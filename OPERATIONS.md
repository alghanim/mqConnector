# mqConnector — Operations Runbook

This file is the on-call reference for running mqConnector in production.
It covers: install, configure, observe, back up, restore, upgrade,
disaster recovery, and high-availability constraints. It does NOT cover
how the bridge works internally — see `README.md` and `CLAUDE.md` for
that.

## Service contract

| Property | Value |
|---|---|
| Single binary | `dist/mqconnector` (≈14 MB) |
| Default port | TCP 8443 (TLS) |
| Data dir | configurable, default `./data/` |
| State | one SQLite file + a pair of TLS files |
| Logs | structured JSON on stderr |
| Metrics | Prometheus at `/api/metrics/prometheus` |
| Health | `/api/health` (200 healthy, 503 unhealthy, body has detail) |
| Graceful shutdown | first SIGTERM/SIGINT begins drain, second forces exit |

## Required external dependencies

- **SimpleAuth** (https://github.com/bodaay/SimpleAuth) — reachable at the
  URL in `auth.simpleauth_url`. mqConnector does **not** host users; all
  authentication round-trips through SimpleAuth.
- **The MQ brokers** the operator configures as connections (IBM MQ /
  RabbitMQ / Kafka — at least one of each). The bridge will boot without
  them and surface the failure in `/api/health.connections[*].status`.

That's it. No databases, no caches, no message queue of its own.

## Install

### Docker compose (recommended for dev / small prod)

```sh
cp .env.example .env
# Edit MQC_MODE, MQC_TLS_*, SIMPLEAUTH_ADMIN_*, MQC_MASTER_KEY in .env
docker compose up -d
```

The compose file pulls SimpleAuth from `../SimpleAuth/`, generates a
self-signed dev TLS cert into a named volume, and creates the initial
admin user via SimpleAuth's admin API.

### Debian / systemd (recommended for larger prod)

```sh
./scripts/build-dist.sh
sudo dpkg-installable tarball: dist/mqconnector-<version>.tar.gz
sudo tar -xzf dist/mqconnector-<version>.tar.gz -C /opt
sudo /opt/mqconnector-<version>/install.sh
sudo systemctl enable --now mqconnector
```

The installer:
- Creates a `mqconnector` system user
- Puts the binary in `/opt/mqconnector/`
- Seeds `/etc/mqconnector/config.yaml` from the example
- Installs `/etc/systemd/system/mqconnector.service` with
  `NoNewPrivileges=true`, `ProtectSystem=strict`, `ProtectHome=true`,
  `PrivateTmp=true`, and a writable `/var/lib/mqconnector`.

## Configuration

`config.yaml` is the single source of truth. Every value can be
overridden by an environment variable named `<DOT_PATH_UPPERCASED>`
(e.g. `server.tls.enabled` → `SERVER_TLS_ENABLED`).

Critical env vars for production:

| Var | Purpose |
|---|---|
| `MQC_MASTER_KEY` | 32-byte hex/base64 key. Without it, MQ connection passwords are stored plaintext. Generate with `openssl rand -hex 32`. |
| `AUTH_SIMPLEAUTH_URL` | URL of the SimpleAuth instance — must include the base path (`/sauth` by default). |
| `SERVER_TLS_CERT_FILE` / `KEY_FILE` | Paths to TLS material. The binary refuses to start in prod mode without both. |
| `SERVER_LISTEN` | Default `0.0.0.0:8443`. |
| `STORAGE_DSN` | SQLite DSN. Default uses `./data/mqconnector.db` with WAL + busy_timeout=5s + FK on. |

The default `config.example.yaml` lists every supported field with
inline documentation.

## Observability

- **Logs**: JSON on stderr by default. Every line includes `time`,
  `level`, `msg`, `request_id`, and per-handler context (pipeline_id,
  remote_ip, status, etc.). Drop into your existing log shipper as-is.
- **Metrics**: Prometheus at `/api/metrics/prometheus` (auth-gated).
  Per-pipeline counters for processed / failed / bytes, latency gauge,
  uptime gauge.
- **Health**: `/api/health` (public; pass a `Authorization` header to
  get the verbose form). The healthcheck contract a load-balancer
  should use is 200 vs 503 — anything else means the binary itself is
  in trouble.
- **Audit**: every state-changing admin action lands in `audit_log`.
  Pull it via `GET /api/v1/audit` or query the SQLite file directly —
  it's an append-only table indexed by `at`, `actor`, and `resource`.

## Backup

mqConnector ships **three** ways to snapshot the SQLite state. All
three use SQLite's online `VACUUM INTO` under the hood, which produces
a transactionally consistent file while writers stay active — there is
no "stop the binary first" step required.

### 1. Scheduled in-process snapshots (recommended)

Set `storage.backup.dir` in `config.yaml`:

```yaml
storage:
  dsn: file:./data/mqconnector.db?_pragma=journal_mode(WAL)
  backup:
    dir: /var/backups/mqconnector
    interval: 1h          # default 24h
    keep: 168             # default 7 (1 week of hourly snapshots)
```

On a multi-replica deploy only the leader writes — the worker checks
the leadership lease each tick. Snapshots are named
`mqconnector-<UTC-timestamp>.db` and the worker auto-prunes older
files past `keep`.

### 2. CLI subcommand (ad-hoc / external scheduler)

```sh
# Single file:
mqconnector backup --to=/mnt/backups/mqc-prod-2026-05-17.db

# Directory mode with rotation (suitable for cron):
mqconnector backup --to-dir=/var/backups/mqconnector --keep=30
```

The CLI runs the same `VACUUM INTO` and then verifies the snapshot by
opening it read-only and running `PRAGMA integrity_check`. Exit code
`2` means the snapshot was written but failed verification — treat
the file as suspect and check the source DB.

### 3. HTTP endpoint (one-off / from a dashboard)

```sh
curl -sS --cacert ca.pem \
     -b "session=<cookie>" \
     -o snapshot.db \
     https://mqc.svc:8443/api/v1/admin/backup
```

System-admin only. Streams the snapshot back as
`application/octet-stream` with a timestamped `Content-Disposition`.
Useful for one-off "grab the state to a laptop" operations — for
real DR, use options 1 or 2 with a path on a separate volume / off-host.

### Legacy script

`scripts/backup.sh` (raw file copy + tar of TLS material) still works
and is harmless to keep in cron. Switch to options 1/2/3 when
convenient.

**Recommended cadence**: hourly to local disk, daily off-host. The
state is small (≤ a few hundred MB even with a year of audit + DLQ
history at typical bridge throughput), so daily snapshots can be
retained for months at trivial cost.

### Integrity check

Quick sanity check on the live database:

```sh
curl -sS --cacert ca.pem -b "session=<cookie>" \
     https://mqc.svc:8443/api/v1/admin/integrity
# → {"ok": true, "duration_ms": 142}
```

`{"ok": false, "errors": [...]}` indicates corruption — stop writes
and restore from the most recent good snapshot (see below). System-
admin only.

## Restore

A snapshot from any of the three backup paths is itself a valid SQLite
database. Restore is just a file swap:

```sh
sudo systemctl stop mqconnector
sudo cp /var/backups/mqconnector/mqconnector-20260517T030000Z.db \
        /var/lib/mqconnector/mqconnector.db
sudo chown mqconnector:mqconnector /var/lib/mqconnector/mqconnector.db
sudo systemctl start mqconnector
# Verify the integrity of the restored database
curl -sS --cacert ca.pem -b "session=<cookie>" \
     https://mqc.svc:8443/api/v1/admin/integrity
```

For the legacy tarball format:

```sh
./scripts/restore.sh /backups/<tar.gz> /tmp/mqc-restore
sudo cp /tmp/mqc-restore/mqconnector.db /var/lib/mqconnector/
sudo cp /tmp/mqc-restore/server.crt /etc/mqconnector/tls/
sudo cp /tmp/mqc-restore/server.key /etc/mqconnector/tls/
sudo chown -R mqconnector:mqconnector /var/lib/mqconnector /etc/mqconnector/tls
sudo systemctl start mqconnector
```

Migrations are idempotent. A newer binary will upgrade the restored
schema on first boot without data loss.

## Upgrade

The standard rolling-upgrade pattern (drain → replace → start) does
NOT apply here because mqConnector is single-instance. Follow this
single-host procedure instead:

1. **Snapshot first** (`scripts/backup.sh`). This is non-negotiable.
2. **Read the release notes** for any migration warnings. mqConnector
   only forward-migrates — there is no automatic downgrade path. If
   the new release introduces a schema change, the only roll-back
   path is "restore the pre-upgrade snapshot."
3. **Stop the old binary**: `systemctl stop mqconnector`. Pipelines
   stop publishing; in-flight messages either commit cleanly or land
   in the DLQ on the next run.
4. **Replace the binary**. Verify with `mqconnector -version` before
   starting.
5. **Start**, then watch `/api/health` and the first 5 minutes of
   `mqconnector_messages_processed_total` to confirm pipelines came
   back up.

## Disaster recovery

| Failure | Recovery |
|---|---|
| Binary crashes / panics | `systemd` restarts. Recover-middleware turns handler panics into 500s without taking the process down — only out-of-handler panics get here. |
| SQLite file corrupted | Restore from the most recent good snapshot. Without WAL recovery, expect to lose at most one minute of audit + DLQ history. |
| TLS cert expired | Replace cert + key. `mqconnector` re-reads them on next start; `systemctl restart` is the safe path. |
| SimpleAuth down | All `/api/*` mutations fail with 401. Existing sessions keep working until their JWT expires. Bring SimpleAuth back; sessions reactivate. |
| MQ broker unreachable | Affected pipelines surface as `status=error` in `/api/health`. The pool evicts the dead connector on its sweep tick; new attempts dial fresh. Messages already in the source queue stay there. |
| DLQ filling under sustained outage | The retention sweeper bounds the table by `pipeline.dlq.max_age` and `max_rows`. Both default to 30d / 100k. Raise if you want a longer recovery window; lower if disk is the limit. |
| MQC_MASTER_KEY lost | Connection passwords cannot be decrypted. Re-enter them via the UI; restart mqconnector with the new key set. |

## High-availability constraints

mqConnector supports a **hot-warm** HA posture via an in-database
leadership lease. Multiple replicas can point at the same SQLite file
(over a shared network volume); only the lease-holder runs pipeline
workers. The standbys serve the admin UI and read-only endpoints but
stay idle on the source queues, so messages aren't double-consumed.

Enable it in config:

```yaml
leadership:
  enabled: true
  id: ""           # empty → hostname; set explicitly per replica in compose/k8s
  ttl: "30s"       # how long a lease lives between renewals
```

Failover semantics:

- Leader renews every `ttl/2`. A clean shutdown clears the lease so a
  standby promotes within one tick.
- A crashed leader's lease expires after `ttl`; standbys see the
  expiry on their next tick and one promotes. Worst-case takeover
  is `ttl + tick` ≈ `1.5 * ttl`.
- The current leader is visible at `/api/health` and tracked in the
  audit log via state-change events.

Single-process deployments (the default — `leadership.enabled: false`)
behave exactly as before: workers start at boot and run for the
lifetime of the process.

Constraints that still apply:

- All replicas MUST point at the same SQLite file (shared NFS, EBS,
  whatever). Without that, the lease they're competing on lives in
  different files and you get split-brain. Use a single shared volume.
- The store is still SQLite. WAL mode tolerates the read-side standby
  traffic but writes serialise through the leader.
- Pipelines are not yet sharded across leaders. Multi-active deployment
  (where two replicas each own a subset of pipelines) is future work
  and would require a partition key in the leadership table.

## Common operational tasks

### Rotate the master encryption key

```sh
# 1. snapshot current state (always)
./scripts/backup.sh

# 2. generate the new key
NEW_KEY=$(openssl rand -hex 32)

# 3. stop the service so it doesn't fight the rotator for the SQLite lock
sudo systemctl stop mqconnector

# 4. dry-run to confirm the row count you expect
mqconnector rotate-secrets --old-key "$OLD_KEY" --new-key "$NEW_KEY" --dry-run

# 5. rotate for real
mqconnector rotate-secrets --old-key "$OLD_KEY" --new-key "$NEW_KEY"

# 6. update the service env (MQC_MASTER_KEY) and restart
sudo systemctl set-environment MQC_MASTER_KEY="$NEW_KEY"
sudo systemctl start mqconnector
```

Edge cases:
- First-time encryption (rows currently plaintext) — omit `--old-key`.
- Removing encryption (return to plaintext) — omit `--new-key` and
  unset `MQC_MASTER_KEY` before restart.

### Audit-log archival

Live `audit_log` rows are queryable via `/api/v1/audit`. To keep the
table bounded and ship history to a SIEM, enable the archiver:

```yaml
audit:
  archive_dir: /var/log/mqconnector/audit
  max_age: "168h"          # archive rows older than 7 days
  sweep_interval: "1h"
```

Each sweep streams matching rows into `audit-YYYY-MM-DD.jsonl` (one
record per line, keyed by row `id`), fsyncs the file, then deletes the
rows. A crash between fsync and DELETE re-archives the same rows on
the next sweep — downstream consumers must deduplicate on `id`. Point
your SIEM's file-input at `*.jsonl` in the archive dir.

### Bulk-edit DLQ

### Bulk-edit DLQ

There is no batch endpoint. Loop with curl + `jq` against
`/api/v1/dlq` and `/api/v1/dlq/:id/retry` if you need to retry
hundreds of entries at once. Be aware of `pipeline.dlq.max_retries`
— entries already at the cap stay quiescent.

### Inspect raw audit log

Audit is a plain table — read it however you want:

```sh
sqlite3 -readonly /var/lib/mqconnector/mqconnector.db \
  "SELECT at, actor, action, resource, status FROM audit_log ORDER BY at DESC LIMIT 50"
```

Snapshot the audit table to your SIEM at whatever cadence your
retention policy demands; the table is append-only.
