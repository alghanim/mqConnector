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

`scripts/backup.sh` makes a transactionally-consistent snapshot via
`sqlite3 .backup` (safe to run while the bridge is actively
processing) and tars it together with the active TLS cert + key + the
config that produced it.

```sh
# Default location: ./backups/mqconnector-<UTC-timestamp>.tar.gz
./scripts/backup.sh

# Custom destination
./scripts/backup.sh /mnt/backups/mqc-prod-2026-05-15.tar.gz
```

The script falls back to a raw file copy if `sqlite3` is not on the
PATH — in that case stop mqconnector first.

**Recommended cadence**: hourly to local disk, daily off-host. The
state is small (≤ a few hundred MB even with a year of audit + DLQ
history at typical bridge throughput), so daily snapshots can be
retained for months at trivial cost.

## Restore

```sh
./scripts/restore.sh /mnt/backups/mqc-prod-2026-05-15.tar.gz
```

The script extracts the archive into a fresh directory and prints next
steps. The actual write to the live data dir is left as a manual step
to avoid clobbering a running instance.

In-situ restore procedure:

```sh
sudo systemctl stop mqconnector
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

mqConnector is **single-instance by design** for the current release:

- The pipeline Manager assumes it's the only consumer of each source
  queue. Running two instances against the same queue will
  double-consume messages.
- The SQLite store is local — there is no clustering or replication.

Acceptable HA postures:

1. **Hot-cold with shared storage** — two hosts share a network volume
   carrying the SQLite file + TLS files. Only one runs `mqconnector`
   at a time. Failover means: stop on host A, start on host B,
   re-point DNS. Targets ≤ 1 min RTO.
2. **Cold standby** — periodic backup restored to a stand-by host on
   demand. RTO matches your backup cadence + restore time.

For genuine multi-active deployment, plan around:
- Leader election for the pipeline Manager (etcd / pg advisory lock)
- Sharding pipelines by ID across nodes
- A shared store (Postgres or libsql) instead of file-local SQLite

That work is not in scope for the current release.

## Common operational tasks

### Rotate the master encryption key

The pre-rotation state must be backed up. Then:

```sh
# 1. snapshot current state
./scripts/backup.sh

# 2. generate the new key
NEW_KEY=$(openssl rand -hex 32)

# 3. read each connection, decrypt with old key, re-encrypt with new
#    (no CLI yet — do this through the admin UI: Edit + Save each
#    connection, which re-seals the password on write)
```

A built-in `mqconnector rotate-secrets` subcommand is on the roadmap.
Until then the admin-UI loop is the official path.

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
