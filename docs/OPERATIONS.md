# mqConnector — operations runbook

Audience: operators on call. This is the "what do I do when X happens"
document. For architecture and code structure, read `CLAUDE.md` and
`README.md` first.

---

## 1. Daily operations

### Where the state lives
- **Configuration** — `config.yaml` (+ env overrides) on disk. Read-only at runtime.
- **Runtime configuration** — SQLite at `storage.dsn` (default `./data/mqconnector.db`).
  Holds: connections, pipelines, transforms, routing rules, scripts, schemas,
  DLQ entries, audit log.
- **Auth users** — managed by SimpleAuth, not mqConnector. Their store
  is independent.
- **Logs** — stderr in JSON; collect via your platform's log shipper.
- **Metrics** — `/api/metrics/prometheus` (Prometheus exposition).

### Routine checks
- `curl -fsk https://<host>:8443/api/health` — green DB + every active
  pipeline status.
- `docker compose ps` — every service should be `(healthy)`.
- Disk usage on the SQLite volume — DLQ + audit grow over time.

---

## 2. Backups

### Cadence
- **Recommended:** every 6 hours, plus before any config change you'd
  want to roll back.
- The script uses SQLite's online backup API, so it's safe against a
  running mqConnector — no downtime needed.

### Take a backup

```sh
./scripts/backup-db.sh                       # ./backups/<timestamp>.db
./scripts/backup-db.sh /backups/foo.db       # explicit destination
DB_PATH=/var/lib/mqconnector/mqconnector.db \
  ./scripts/backup-db.sh                     # production path
```

The script verifies `PRAGMA integrity_check` on the copy and exits
non-zero on any failure, so it's safe to schedule via cron / systemd
timer / k8s CronJob without further wrapping.

Example cron line (every 6 hours, 30 days of retention):

```cron
0 */6 * * * cd /opt/mqconnector && DB_PATH=/var/lib/mqconnector/mqconnector.db \
    ./scripts/backup-db.sh /var/backups/mqc/$(date -u +\%Y\%m\%dT\%H\%M\%SZ).db \
  && find /var/backups/mqc -mtime +30 -name '*.db' -delete
```

### Restore from a backup

```sh
# 1. Stop mqconnector
systemctl stop mqconnector       # or: docker compose stop mqconnector

# 2. Restore (refuses to run if the DB is still in use)
DB_PATH=/var/lib/mqconnector/mqconnector.db \
  ./scripts/restore-db.sh /var/backups/mqc/<timestamp>.db

# 3. Restart and verify
systemctl start mqconnector
curl -fsk https://localhost:8443/api/health
```

`restore-db.sh` makes a timestamped snapshot of the current database
(`<DB_PATH>.pre-restore.<ts>`) before overwriting, so a wrong restore
is reversible — just stop mqConnector and `cp` the snapshot back.

---

## 3. Common incidents

### Pipeline stuck (broker outage)

**Symptom:** `/api/metrics/prometheus` shows
`mqconnector_pipeline_messages_total` flat for one pipeline,
`mqconnector_dlq_entries_total` rising.

**Action:**
1. Confirm broker reachability:
   `docker compose exec mqconnector wget --spider -q amqp://...`
   or whatever's appropriate per type.
2. Hit `POST /api/v1/connections/<id>/test` from the UI or via curl
   to confirm — this dry-runs `Connect + Ping` against the stored
   credentials.
3. If the broker is genuinely down, DLQ retention is configured
   (`pipeline.dlq.max_age`, `pipeline.dlq.max_rows`) — entries will be
   capped automatically.
4. Once the broker is back, retry from the DLQ:
   `POST /api/v1/dlq/<id>/retry` per entry, or bulk-retry via the UI.

### High DLQ growth from one pipeline

**Symptom:** Single pipeline filling the DLQ; messages are routed but failing.

**Action:**
1. Open the DLQ in the UI and inspect the `error_reason` on the most
   recent entries. Common patterns:
   - `validation failed: required field 'x' is missing` — schema mismatch
   - `transform error: ...` — recent transform rule is wrong
   - `script execution error: ...` — script regression
2. Fix the pipeline definition. The Manager hot-reloads on PUT/POST,
   no restart needed.
3. Optionally retry the DLQ entries once the fix is in.

### "Audit log is full of unexpected actor X"

**Action:**
1. `/api/v1/audit?actor=X` shows the trail.
2. Cross-reference with `request_id` in the application logs for
   request body + IP details.
3. If unauthorized, revoke the SimpleAuth user, rotate the master key
   for connection encryption (see §5), force-restart all mqConnector
   instances to invalidate in-memory caches.

### Process crashed / restarted

**Action:**
- The Recover middleware should keep the process up; a real crash means
  a runtime panic on goroutine outside the request lifecycle (rare).
- Inspect logs for the panic stack trace — `request_id` is null for
  these; search for `level=ERROR msg=panic`.
- Pipelines reload from storage on boot, so no manual recovery is needed.
- DLQ retention runs its first sweep right at boot, catching up any
  rows that aged out while the process was down.

---

## 4. Capacity & resource limits

| Resource | Default cap | Where it's enforced |
|---|---|---|
| Request body | 10 MB | `server.max_body_bytes` middleware |
| MQ pool idle TTL | 5 min | `mq.pool.idle_timeout` |
| IBM MQ recv buffer | 4 MB | `mq.pool.ibm_recv_buffer` |
| DLQ rows | 100k | `pipeline.dlq.max_rows` (background sweeper) |
| DLQ age | 30 days | `pipeline.dlq.max_age` (background sweeper) |
| SimpleAuth session | 12 h | `auth.session_ttl` |
| Login attempts | 10/min/IP | hard-coded `rateLimitLogin` middleware |

---

## 5. Secrets and rotation

### What's stored as a secret
- **MQ connection passwords** — encrypted at rest via AES-GCM with a
  master key from env (`MQC_MASTER_KEY` or the `secrets.master_key_file`
  config option).
- **SimpleAuth admin key** — only used by mqConnector to provision its
  initial admin user; not stored in mqConnector's DB at all.
- **TLS private key** — `server.tls.key_file` on disk, mode 0640.

### Rotate the master key
1. Generate a new key: `openssl rand -base64 32`
2. Start mqConnector with both keys in env:
   `MQC_MASTER_KEY=<new> MQC_MASTER_KEY_PREVIOUS=<old> ./mqconnector`
3. Open every connection in the UI and "Save" — this re-encrypts the
   password with the new key. (A future release may add a dedicated
   rewrap endpoint.)
4. Drop `MQC_MASTER_KEY_PREVIOUS` and restart.

### Rotate the TLS cert
Just replace the cert + key files and reload (SIGHUP — graceful
restart re-reads `tls.cert_file` / `tls.key_file`). No client-side
change required.

---

## 6. Upgrades

### In-place upgrade
1. Take a fresh backup (see §2).
2. Stop the process.
3. Replace the binary (or `docker compose pull && docker compose up -d`).
4. Start — migrations run automatically; `schema_migrations` table is
   the source of truth for what's been applied.
5. Confirm `/api/health` returns ok with the new `version` value.

### Rollback
1. Stop the new binary.
2. Restore the pre-upgrade backup (see §2).
3. Start the old binary.
   Note: migrations are forward-only. If the new release added a
   migration that the old binary doesn't know about, the old binary
   will refuse to start. Restoring the backup makes the database
   match the old binary's expectations.

---

## 7. Multi-replica deployment

The current binary assumes **a single active replica per logical
pipeline**. Running two replicas pointed at the same source queue will
double-consume.

Operationally the safe pattern is:
- One mqConnector instance per (broker, source queue) pair.
- For HA, run a passive replica behind a leader election layer (k8s
  Lease, Consul, file lock on shared storage) — *outside* mqConnector
  itself. A simple `flock` over a shared NFS path is enough for many
  deployments.
- The UI/admin API can be served by an N-way replica behind a load
  balancer, but only one replica should hold the worker goroutines.

A future release may add native leader election. Until it does, **do
not horizontally scale a single pipeline**.

---

## 8. Reference

| Endpoint | Purpose |
|---|---|
| `GET /api/health` | Liveness/readiness — public |
| `GET /api/openapi.yaml` | Full API spec — public |
| `GET /api/v1/audit` | Audit log — admin |
| `GET /api/metrics/prometheus` | Prometheus metrics — admin |
| `POST /api/v1/connections/{id}/test` | Live connectivity test — admin |
| `POST /api/v1/dlq/{id}/retry` | Re-publish a DLQ entry — admin |

See `internal/server/openapi.yaml` for the full contract.
