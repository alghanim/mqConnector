# Features

Complete capability matrix for mqConnector. Items marked **stable** ship in the default build and have test coverage. **Build-tag** features ship only when compiled with the named tag. **Beta** features work end-to-end but lack the integration test coverage of stable items.

---

## Message-queue connectors

| Broker | Protocol | Driver | Status | Build flag |
|---|---|---|---|---|
| RabbitMQ | AMQP 0.9.1 | `rabbitmq/amqp091-go` v1.10 | **stable** | default |
| Kafka | Kafka 3.x | `IBM/sarama` v1.43 | **stable** | default |
| IBM MQ | MQI | `ibm-messaging/mq-golang/v5` | **stable** | `-tags ibmmq` (CGO + glibc) |
| MQTT | MQTT 3.1 / 3.1.1 / 5 | `eclipse/paho.mqtt.golang` v1.5 | **beta** | default |
| NATS / JetStream | NATS | `nats-io/nats.go` | **beta** | default |
| AMQP 1.0 | OASIS AMQP 1.0 | `Azure/go-amqp` v1.6 | **beta** | default |

**Per-connector capabilities:**

- TLS with optional CA pinning, client cert auth, and `insecure_skip_verify` for dev.
- Configurable credentials (broker passwords are AES-256-GCM-encrypted at rest with versioned keys).
- Auto-reconnect with exponential backoff on connection drop.
- Health probing: the pool evicts idle connections and re-establishes on next use.
- Per-broker tunables — Kafka consumer group, MQTT client ID + QoS, NATS stream/consumer name, AMQP 1.0 prefetch credit.

---

## Pipeline stages

Each pipeline is a typed list of stages, executed in declaration order. Stages mutate the message in place — by the time the message reaches the next stage, the prior stage's effects are visible.

### validate

JSON Schema (Draft-07), XSD, or Protobuf message validation against a schema stored in the `schemas` table.

- **JSON Schema** via `xeipuuv/gojsonschema`.
- **XSD** via `lestrrat-go/libxml2` (build-time dep — Protocol-Buffer–level reliability).
- **Protobuf** via `google.golang.org/protobuf` + `dynamicpb` — schemas are stored as base64-encoded `FileDescriptorSet` (output of `protoc --descriptor_set_out=`). No generated Go bindings needed.

Failures: validation errors push the original message to the DLQ with a `validation: <field>: <reason>` `error_reason`.

### filter

Drop fields from the message before forwarding. Path syntax:

- JSON: `$.user.password` (JSONPath dot syntax)
- XML: `//user/password` (XPath 1.0)

Multiple paths can be chained; each is applied in order. Filtering happens in place — the message keeps its original encoding.

### transform

Field-level mutations. Five operators:

| Operator | What it does | Example |
|---|---|---|
| `rename` | move a value to a new path | `customer.email` → `email` |
| `mask` | regex-replace a substring | `\d{12}(\d{4})` → `************$1` |
| `move` | rename a subtree to a different parent | `meta.tags` → `tags` |
| `set` | overwrite or insert a literal value | `meta.version = "v2"` |
| `delete` | remove a path | `internal.debug` |

All operators are declarative — no scripting needed. Order within a pipeline is preserved.

### translate

Wire-format conversion. Allowed pairs:

| From → To | Library | Notes |
|---|---|---|
| JSON → XML | `clbanning/mxj` | Element naming follows mxj conventions (configurable per pipeline). |
| XML → JSON | `clbanning/mxj` | Round-trippable for most documents; attribute-heavy XML loses information. |
| JSON → Protobuf | `protojson` | Requires schema_id + proto message FQN on the stage. |
| Protobuf → JSON | `protojson` | Same; uses canonical protojson format. |
| same → same | no-op | Leave the stage in place but defer to pipeline-level format. |

### route

Content-based fan-out. Each pipeline owns a list of routing rules:

```
WHEN <path> <operator> <value>  →  send to <destination_id>
```

Operators: `eq`, `neq`, `contains`, `regex`, `gt`, `lt`, `exists`. Rules are evaluated in priority order; **all** matching rules fire (a single message can hit multiple destinations). If no rules match, the pipeline's default destination is used.

### script

Sandboxed JavaScript for the cases declarative stages can't reach. The script receives a `message` object (already JSON-decoded if the message was JSON; raw bytes for XML), mutates it, and returns. Sandbox caps:

- CPU: 100 ms per message (configurable per pipeline)
- Memory: 16 MB heap
- No `fetch`, no filesystem, no `require`.
- Pure expression evaluation; no event loop, no async.

---

## Multi-tenancy

- **Tenant scoping** on every storage row — connections, pipelines, schemas, scripts, transforms, routing rules, DLQ, API tokens, webhooks.
- **Role-based access control** — four roles per tenant: `viewer`, `operator`, `admin`, `owner`. Roles compose hierarchically; an admin has every operator capability.
- **Tenant switching** via the `X-Tenant-Id` header on API tokens or the `?tenant=` query param on the UI session.
- **Rate-limiting per tenant** — token-bucket against `messages_per_minute` from the tenant row.
- **Cross-tenant isolation** is enforced at the SQL layer: every query filters by `tenant_id`. The middleware injects it from the session.

System-admin (the user who owns the `default` tenant) can create new tenants and assign owners.

---

## Auth and sessions

- **SimpleAuth** OIDC-style server. No external IdP — fits air-gapped department deploys.
- **Cookie sessions** — `Secure; HttpOnly; SameSite=Strict`. Refresh on every request once past 50% of TTL.
- **API tokens** — long-lived, scoped to a tenant + role. Created in the UI under `/tokens`. Tokens carry a visible prefix (`mqct_…`) so they're greppable in logs.
- **Login throttling** — per-IP rate limit on `/api/auth/login` and `/api/auth/refresh` to slow credential stuffing.
- **Audit log** — every mutation is recorded with a SHA-256 hash chain (prev_hash → hash). Tamper detection is built in: `/api/v1/audit/verify` walks the chain and reports any break.

---

## Observability

- **Structured logging** — `slog` JSON output. Every request gets a request_id, propagated through the context to all downstream calls.
- **W3C trace-context** propagation — incoming `traceparent` is honoured; outbound HTTP + MQ sends carry it forward.
- **Prometheus exposition** at `/api/metrics/prometheus` (admin auth). Per-pipeline counters: processed, failed, bytes, avg latency. Pool counters: get, miss, evict.
- **Live JSON metrics** at `/api/metrics`. Same data, JSON-shaped for the UI.
- **Server-sent events** at `/api/v1/events` — the admin UI subscribes for live throughput, DLQ totals, and health changes without polling.
- **/api/health** for liveness probes (no auth required). Includes per-pipeline status + last_error.

---

## DLQ

Failed messages land in the DLQ table with: tenant_id, pipeline_id, source_queue, original payload, error reason, retry_count, created_at, last_retry_at.

UI surface lives at `/dlq`:

- Filter by pipeline, error substring, time range.
- Retry: re-publishes the original message through the same pipeline. Increments retry_count.
- Bulk delete after review.
- Pagination + sortable columns.

The retry path is the real pipeline — not a shortcut. If the original failure was a transform bug, the retry will fail again until the transform is fixed.

---

## Webhooks

Outbound event notifications. Each tenant can register webhook URLs at `/webhooks`:

| Event type | Fires when |
|---|---|
| `pipeline.started` | a pipeline transitions to `connected` |
| `pipeline.stopped` | a pipeline executor exits |
| `pipeline.error` | a pipeline records a `last_error` |
| `dlq.entry_added` | a message lands in the DLQ |
| `connection.test_failed` | "Test connection" returns an error |

Payloads are JSON, HMAC-SHA256-signed in the `X-MQC-Signature` header (`sha256=<hex>`). Verify with the per-webhook secret. Retries: 3 with exponential backoff; permanent failure records a `last_status` + `last_error` on the webhook row.

---

## Config import / export

`/api/v1/config/export` returns a tenant-scoped JSON bundle of all connections, pipelines, stages, transforms, routing rules, schemas, and scripts.

`/api/v1/config/import` accepts the same shape. Two modes:

- **Dry-run** (`?dry_run=1`) — validates the bundle, reports counts, doesn't write.
- **Apply** — upserts everything in one transaction. Existing names are merged; new ones are created.

Useful for: dev → staging promotion, disaster-recovery seeding, multi-tenant template provisioning.

---

## Security hardening

- **TLS** is mandatory in `mode: prod`. Refuses to start without `cert_file` + `key_file`.
- **CSP nonce** on every HTML response — strict source allowlists, no inline scripts.
- **Strict cookie defaults** — `Secure; HttpOnly; SameSite=Strict`.
- **Security headers** middleware: HSTS, X-Frame-Options, X-Content-Type-Options, Referrer-Policy, Permissions-Policy.
- **CORS** — explicit allowlist in `config.yaml`. No wildcards in prod mode.
- **Request size cap** — `server.max_body_bytes`, defaults to 4 MB.
- **Request timeout** — every handler has a hard ctx deadline; a stuck downstream can't pin a goroutine forever.
- **Encryption at rest** — broker passwords use AES-256-GCM envelope encryption with versioned keys. Rotation is online: add a new key id to config, the next mutation to a connection row re-encrypts with the new version.
- **Audit log integrity** — SHA-256 hash chain; tamper detection via `/api/v1/audit/verify`.
- **Script sandbox** — CPU + memory caps on user-supplied JavaScript stages.
- **Login rate-limit** — per-IP throttle on `/api/auth/login`.

---

## Admin UI

- **SvelteKit + Tailwind**, fully embedded in the binary (no separate frontend deploy).
- **Bilingual EN / AR** with full RTL — CSS logical properties throughout, gradient direction mirrors automatically.
- **Dark + Light themes** — token-driven design system. No raw hex anywhere outside `brand-tokens.css`.
- **Pages**:
  - `/` — Operations overview: dense KPI strip, throughput chart (processed + failed series), pipeline health matrix, recent activity feed.
  - `/connections` — CRUD for broker connections, per-type form fields, "Test connection" probe.
  - `/pipelines` — Pipeline registry with live metrics inline, sparkline per row, error pill on failure.
  - `/pipelines/[id]` — Per-pipeline form fallback: stages, transforms, routing rules, schemas, scripts.
  - `/flow` — Visual flow editor: drag-and-drop nodes, draggable edges, pinned sample/preview drawer.
  - `/dlq` — Dead-letter triage: filter, retry, bulk delete, pagination, sortable columns.
  - `/metrics` — Live throughput table, sortable, with sparklines and totals row.
  - `/tenants` — Multi-tenant administration: roles, members, default tenant pill, max-pipelines + rate limit.
  - `/tokens` — API token management.
  - `/webhooks` — Webhook configuration with last-status indicator.
  - `/settings` — Config import / export, secret rotation, system info.
  - `/help` — Plain-English documentation with bilingual content.
- **Keyboard shortcuts**:
  - `⌘K` / `Ctrl-K` / `/` — command palette
  - `?` — keyboard shortcut sheet
  - `Esc` — close any open overlay
- **Embedded fonts** — Inter Variable (Latin) + Noto Kufi Arabic (Arabic), shipped as woff2 inside the binary. No runtime CDN fetch — air-gap safe.

---

## Storage

- **SQLite** via `modernc.org/sqlite` (pure Go, no CGO).
- **Migrations** run on every boot; idempotent (`CREATE TABLE IF NOT EXISTS …`).
- **WAL mode** — concurrent readers + single writer; `PRAGMA wal_checkpoint(TRUNCATE)` before backup snapshots.
- **Typed repositories** per collection: Connections, Pipelines, DLQ, Scripts, Schemas, Webhooks, Tokens, Audit, Tenants, Memberships.
- **Single-writer leadership** via row-lock — for HA deploys, only the leader runs pipelines.

The full DB is a single file: `/var/lib/mqconnector/mqconnector.db`. Backup is `cp` (safe while running) or `sqlite3 .backup`.

---

## Operations

- **Hot reload** of pipeline definitions — `POST /api/v1/reload` reconciles running executors against the storage view, starting / stopping per change. No process restart needed.
- **Backup + restore** — `scripts/backup-db.sh` / `scripts/restore-db.sh`. Online backups via the SQLite backup API.
- **Audit archival** — `scripts/archive-audit.sh` exports older audit rows to S3 (stdlib-only SigV4 signing), deletes after successful upload. Cron-friendly.
- **Secret rotation** — `POST /api/v1/secrets/rotate` re-encrypts every encrypted field with the latest key version.
- **Helm chart** at `deploy/helm/mqconnector/` for k8s deploys.
- **Operations runbook** at [`docs/OPERATIONS.md`](OPERATIONS.md) for on-call playbooks.

---

## Build + test infrastructure

- **CI** (GitHub Actions, `.github/workflows/ci.yml`) — three jobs in parallel:
  - Backend: `go vet`, staticcheck (latest), `go test -race -cover`
  - Frontend: svelte-check, vitest, vite build
  - Docker: full multi-stage build + smoke test
- **Cross-platform builds** via `docker buildx` (linux/amd64 + linux/arm64).
- **Dev compose stack** — mqConnector, SimpleAuth, RabbitMQ, Kafka, certs init, all wired with healthchecks.
- **Test fixtures** — in-memory MQ connectors for e2e tests, real-broker integration tests behind `-tags integration`.
