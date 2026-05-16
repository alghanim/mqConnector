# mqConnector

A single-binary message-queue bridge and pipeline runner. Consume from one MQ, apply a configurable pipeline (validate → filter → transform → translate → route → script), forward to another. Six broker families supported, four wire formats handled, all wrapped in a SvelteKit admin UI that ships embedded in the same binary.

```
   ┌───────────┐     ┌──────────────────────────────┐     ┌──────────────┐
   │  Source   │     │   mqConnector (single binary) │     │ Destination  │
   │  broker   │────▶│                              │────▶│  broker(s)   │
   └───────────┘     │  validate → filter →         │     └──────────────┘
                     │  transform → translate →     │
                     │  route → script              │            │
                     │                              │            │
                     │  on failure: DLQ + retry     │   on route: fan-out
                     │  hot-reload pipelines        │
                     │  Prometheus + audit + UI     │
                     └──────────────────────────────┘
                                  │
                          SQLite (config + DLQ)
                          SimpleAuth (auth)
                          Embedded SvelteKit UI
```

**Looking for…**
- [Feature list →](docs/FEATURES.md) — every capability, what's stable, what's behind a build tag
- [Dependency analysis →](docs/DEPENDENCIES.md) — what we import, why, license + footprint
- [Message-flow walkthrough →](docs/REQUEST_FLOW.md) — broker-in to broker-out, including DLQ
- [API request lifecycle →](docs/API_FLOW.md) — middleware chain, auth, audit, response
- [Operations runbook →](docs/OPERATIONS.md) — backups, restore, rotation, incidents
- [Compliance →](COMPLIANCE.md) — coding-standard checklist

---

## Table of contents
1. [What it does](#what-it-does)
2. [Quick start (5 minutes, Docker)](#quick-start-5-minutes-docker)
3. [Quick start (native binary)](#quick-start-native-binary)
4. [Build](#build)
5. [Configuration](#configuration)
6. [Deployment](#deployment)
   - [Single-host Docker Compose](#deployment--single-host-docker-compose)
   - [Production (TLS, persistent volumes)](#deployment--production)
   - [Helm / Kubernetes](#deployment--helm)
   - [Air-gapped tarball install](#deployment--air-gapped-tarball)
   - [IBM MQ variant](#deployment--ibm-mq-variant)
7. [Verifying the install](#verifying-the-install)
8. [Project layout](#project-layout)
9. [Development](#development)
10. [Testing](#testing)
11. [Troubleshooting](#troubleshooting)

---

## What it does

mqConnector sits between two message-queue brokers and runs every message through a tenant-scoped pipeline:

| Stage | What it does |
|---|---|
| **validate** | JSON Schema (Draft-07), XSD, or Protobuf (FileDescriptorSet) — rejects malformed payloads to the DLQ |
| **filter** | drop fields by JSON-path / XPath before forwarding |
| **transform** | rename / mask / move / set / delete fields, declaratively |
| **translate** | JSON ↔ XML ↔ Protobuf wire format conversion |
| **route** | content-based routing — multiple destinations from one source, decided per-message |
| **script** | safe sandboxed JavaScript for the cases declarative stages can't reach |

Across six broker types:

| Broker | Driver | CGO? | Build tag |
|---|---|:---:|---|
| RabbitMQ (AMQP 0.9.1) | `rabbitmq/amqp091-go` | no | — |
| Kafka | `IBM/sarama` | no | — |
| MQTT (3.1 / 3.1.1 / 5) | `eclipse/paho.mqtt.golang` | no | — |
| NATS / JetStream | `nats-io/nats.go` | no | — |
| AMQP 1.0 (ActiveMQ, Artemis, Solace, Azure SB) | `Azure/go-amqp` | no | — |
| IBM MQ | `ibm-messaging/mq-golang/v5` | **yes** | `ibmmq` |

Configuration is in SQLite — no etcd, no Postgres, no external state store. The admin UI ships embedded; the entire deploy is one binary and one database file.

---

## Quick start (5 minutes, Docker)

The fastest path to a running stack — mqConnector + SimpleAuth + RabbitMQ + Kafka + a self-signed TLS bootstrap, all wired together.

**Prerequisites:** Docker Desktop or Docker Engine 24+, Docker Compose v2, a sibling `SimpleAuth/` checkout one directory up (see [SimpleAuth setup](#simpleauth-setup) below if you don't have one).

```sh
git clone https://github.com/alghanim/mqConnector.git
cd mqConnector

cp .env.example .env
# At minimum, set SIMPLEAUTH_ADMIN_PASSWORD in .env

docker compose up -d
docker compose ps          # wait for "healthy" on all services
```

Service URLs once everything is healthy (~30 s):

| Service | URL | Default creds |
|---|---|---|
| mqConnector admin UI | https://localhost:8443/ | from SimpleAuth |
| SimpleAuth | https://localhost:9443/ | `admin / <SIMPLEAUTH_ADMIN_PASSWORD>` |
| RabbitMQ management | http://localhost:15672/ | `mqc / mqc-dev` |
| Kafka bootstrap | `localhost:9192` (host-side) | — |

First login: open `https://localhost:8443/`, accept the self-signed cert, sign in with the SimpleAuth admin user.

### SimpleAuth setup

mqConnector authenticates against SimpleAuth (lightweight, air-gap-friendly, no third-party identity provider). The compose file expects SimpleAuth's source to be a sibling directory:

```sh
cd ..
git clone https://github.com/bodaay/SimpleAuth.git
cd mqConnector
docker compose up -d   # builds SimpleAuth as part of the stack
```

If your SimpleAuth checkout is elsewhere, set in `.env`:
```
SIMPLEAUTH_CONTEXT=/abs/path/to/SimpleAuth
SIMPLEAUTH_SDK_PATH=/abs/path/to/SimpleAuth/sdk/go
```

---

## Quick start (native binary)

```sh
# Build the binary (default build — no CGO, no IBM MQ)
./scripts/build.sh

# Configure
cp config.example.yaml config.yaml
# Edit config.yaml — at minimum, set auth.simpleauth.{base_url, admin_token}

# Run
./scripts/dev.sh           # dev mode (TLS optional, debug logs)
# or
./dist/mqconnector         # production mode (TLS required)
```

Default admin URL: https://localhost:8443/

---

## Build

```sh
# Default build — pure Go, no CGO; RabbitMQ, Kafka, MQTT, NATS, AMQP 1.0
./scripts/build.sh

# IBM MQ variant — CGO, requires linux/amd64 host or QEMU
./scripts/build.sh --ibmmq

# Cross-platform IBM MQ build via Docker (recommended on macOS / ARM)
docker buildx build --platform linux/amd64 \
  --build-context simpleauth-sdk=../SimpleAuth/sdk/go \
  -f Dockerfile.ibmmq -t mqconnector:ibmmq .

# Distribution tarball (binary + config.example + scripts)
./scripts/build-dist.sh
```

What the build does:
1. Builds the SvelteKit frontend with `adapter-static` into `internal/web/dist/`
2. Embeds that directory into the Go binary via `go:embed`
3. Stamps the version from `VERSION` into the binary
4. Outputs `dist/mqconnector` (~25 MB stripped, default build)

The OpenAPI 3.0 spec at [`internal/server/openapi.yaml`](internal/server/openapi.yaml) is served live at `/api/openapi.yaml` on every running instance.

---

## Configuration

Configuration is YAML on disk with environment variable overrides. The full reference is [`config.example.yaml`](config.example.yaml).

**Override convention:** YAML path → ENV by uppercasing and replacing `.` with `_`. Examples:

| YAML | Environment variable |
|---|---|
| `server.tls.enabled` | `SERVER_TLS_ENABLED` |
| `auth.simpleauth.base_url` | `AUTH_SIMPLEAUTH_BASE_URL` |
| `storage.sqlite.path` | `STORAGE_SQLITE_PATH` |

**Minimum production config:**

```yaml
mode: prod           # disables CORS wildcards, enforces TLS
server:
  listen: ":8443"
  tls:
    enabled: true
    cert_file: /etc/mqconnector/tls/cert.pem
    key_file:  /etc/mqconnector/tls/key.pem
auth:
  simpleauth:
    base_url: https://simpleauth.internal/
    admin_token: ${SIMPLEAUTH_ADMIN_TOKEN}
storage:
  sqlite:
    path: /var/lib/mqconnector/mqconnector.db
secrets:
  encryption_keys:        # AES-256-GCM envelope keys (versioned)
    - id: v1
      key: ${MQC_KEY_V1}  # 32-byte base64
```

Two boots required to populate `secrets.encryption_keys` — without them the broker passwords in the connections table are stored in plaintext. See [docs/OPERATIONS.md](docs/OPERATIONS.md) for key rotation.

---

## Deployment

### Deployment — single-host Docker Compose

The reference deploy is the included `docker-compose.yml`. Two volumes are persistent:

| Volume | Path | Contents |
|---|---|---|
| `mqc-data` | `/var/lib/mqconnector` | SQLite DB, DLQ |
| `certs` | `/etc/mqconnector/tls` | TLS material (self-signed by default) |

To swap the self-signed cert for a real one, mount your cert + key into `/etc/mqconnector/tls/` and restart the `mqconnector` service.

### Deployment — production

For hardened deploys:

1. **Set `mode: prod`** in `config.yaml`. This:
   - Forces TLS (refuses to start without `cert_file` + `key_file`)
   - Disables CORS wildcards
   - Tightens the CSP nonce policy
   - Switches log format to JSON-only

2. **Provide a real TLS cert** — recommended issuer is your internal CA. Self-signed is acceptable only for dev.

3. **Configure SimpleAuth** to issue tokens with the `tenant_owner` claim for users you want to grant tenant-creation rights to.

4. **Set `secrets.encryption_keys`** — broker passwords are AES-256-GCM-encrypted at rest with versioned keys. Add a new key id at rotation time; old rows decrypt with the old key version and re-encrypt with the new one on the next save.

5. **Configure audit log + DLQ retention.** Both grow unbounded by default. Either:
   - Set `storage.retention.audit_days` / `storage.retention.dlq_days` to auto-prune
   - Or use the archival job ([`scripts/archive-audit.sh`](scripts/archive-audit.sh)) which exports older rows to S3 (SigV4-signed, stdlib only) and deletes them

6. **Mount a writable volume at `/var/lib/mqconnector`** — the SQLite DB lives there. **Back up this volume.** A simple `cp` while the binary is running is safe (we use WAL mode + `PRAGMA wal_checkpoint(TRUNCATE)` before serialise) — see `scripts/backup-db.sh`.

7. **Prometheus scraping** — point your collector at `https://<host>:8443/api/metrics/prometheus`. The endpoint requires an admin session, so use a [scoped API token](#api-tokens) — see the operations runbook for examples.

### Deployment — Helm

A Helm chart lives at [`deploy/helm/mqconnector/`](deploy/helm/mqconnector/). The minimal `values.yaml`:

```yaml
image:
  repository: ghcr.io/alghanim/mqconnector
  tag: 1.0.0
config:
  mode: prod
  simpleauth:
    baseURL: https://simpleauth.svc.cluster.local/
secrets:
  simpleauthAdminToken: <stored in a k8s Secret>
  encryptionKey: <base64 32 bytes>
persistence:
  size: 5Gi
  storageClass: standard
tls:
  certificate: <ref to cert-manager issued cert>
```

Install:
```sh
helm install mqconnector ./deploy/helm/mqconnector -f values.yaml
```

The chart provisions: Deployment (1 replica by default — see [HA section](#high-availability)), PersistentVolumeClaim, Service, optional Ingress, a Secret for the SimpleAuth admin token, and a ConfigMap for `config.yaml`.

### Deployment — air-gapped tarball

For environments with no Docker registry / no internet:

```sh
./scripts/build-dist.sh                 # builds dist/mqconnector-<ver>.tar.gz
scp dist/mqconnector-1.0.0.tar.gz host:/tmp/
ssh host
tar -xzf /tmp/mqconnector-1.0.0.tar.gz -C /opt/
cp /opt/mqconnector/config.example.yaml /etc/mqconnector/config.yaml
# edit /etc/mqconnector/config.yaml, then:
systemctl enable --now mqconnector       # unit file ships in the tarball
```

The tarball contents:
```
mqconnector            single binary (~25 MB, no shared lib deps)
config.example.yaml    annotated reference config
mqconnector.service    systemd unit file
scripts/               backup, restore, archive, version-bump
```

### Deployment — IBM MQ variant

The IBM MQ build links against IBM's redistributable client which is **linux/amd64 only**. On ARM hosts, use the Docker buildx command (above) which runs QEMU.

```sh
docker buildx build --platform linux/amd64 \
  --build-context simpleauth-sdk=../SimpleAuth/sdk/go \
  -f Dockerfile.ibmmq -t mqconnector:ibmmq .

docker run -d --name mqconnector \
  -p 8443:8443 \
  -v $PWD/config.yaml:/etc/mqconnector/config.yaml:ro \
  -v mqc-data:/var/lib/mqconnector \
  mqconnector:ibmmq
```

### High availability

mqConnector is **leader-elected single-writer**, not active-active. Multiple replicas can run for read availability — the leader (chosen via the storage layer's row-lock leadership protocol) owns pipeline execution; followers serve read-only UI traffic.

For most installs, a single replica with the data volume snapshotted is the right call.

---

## Verifying the install

```sh
# Liveness
curl -k https://localhost:8443/api/health
# → 200 {"status":"healthy","db_status":"ok","uptime":"...","version":"..."}

# OpenAPI spec
curl -k https://localhost:8443/api/openapi.yaml | head -20

# Sign in via the UI
open https://localhost:8443/

# Or programmatically — get a session cookie
curl -k -c /tmp/cookies.jar -X POST https://localhost:8443/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<password>"}'

# Then exercise the API
curl -k -b /tmp/cookies.jar https://localhost:8443/api/v1/connections
```

Smoke test the message pipeline end-to-end (assumes the dev RabbitMQ from `docker compose` is up):

```sh
# 1. Create a connection (in the UI or via API)
# 2. Create a pipeline source→dest
# 3. Publish to the source queue:
curl -k -b /tmp/cookies.jar -X POST \
  https://localhost:8443/api/v1/bridge/publish/<connection_id> \
  -d '{"order":"abc","qty":3}'
# 4. Watch the dest queue, or the /api/metrics endpoint, for delivery
```

---

## Project layout

```
cmd/mqconnector/         Entry point — boots config → logger → storage → mq pool →
                         pipeline manager → server. Signal-driven graceful shutdown.

internal/auth/           SimpleAuth integration + middleware (RequireSession,
                         RequireRole). Multi-tenant aware.
internal/audit/          Hash-chained audit log (SHA-256 chain), tamper detection.
internal/config/         YAML loader + env override + validation. Single Config struct.
internal/dlq/            Dead-letter queue persistence + retry (re-publishes the
                         original payload through the pipeline).
internal/events/         Internal pub/sub for SSE + webhook dispatch.
internal/health/         /api/health aggregation: DB ping + metrics snapshot.
internal/leadership/     Single-writer election (storage-row lock).
internal/logging/        slog wrapper, JSON output, request-scoped context.
internal/metrics/        Counter store + Prometheus exposition.
internal/mq/             Connector interface + per-broker impls + Pool with
                         health checks and eviction.
internal/mqcfg/          Storage row ↔ mq.Config conversion (the encryption boundary).
internal/pipeline/       Stages + Manager (hot reload) + Executor (per-pipeline goroutine).
internal/sample/         Sample-message capture for the pipeline preview UI.
internal/secrets/        AES-256-GCM envelope encryption, versioned keys.
internal/server/         chi router, middleware chain, REST handlers, TLS, SSE.
internal/storage/        SQLite + migrations + typed repositories.
internal/tracing/        W3C trace-context propagation.
internal/web/            Embedded SvelteKit dist (go:embed).
internal/webhooks/       Outbound webhook dispatcher (HMAC-SHA256 signed).

web/                     SvelteKit source. Builds into internal/web/dist via
                         the static adapter.

scripts/                 build.sh, build-dist.sh, dev.sh, version-bump.sh,
                         backup-db.sh, restore-db.sh, archive-audit.sh.

deploy/helm/             Helm chart for k8s deploys.
docs/                    Feature catalogue, flow walkthroughs, operations runbook.
```

---

## Development

```sh
# Backend in watch mode (no automatic reload — restart by hand)
go run ./cmd/mqconnector -config config.yaml

# Frontend dev server — proxies /api to the backend at :8443
cd web
npm install
npm run dev   # http://localhost:5173/ — full HMR
```

The frontend dev server proxies `/api/*` to `https://localhost:8443` (configured in `web/vite.config.ts`). You sign in via the dev server origin; the cookie still lands on `:8443` thanks to the proxy.

For changes that span both halves, the typical loop is:
1. Frontend dev server keeps running (HMR handles UI iteration)
2. Backend: `Ctrl-C` + `go run` after each Go change (or use `air`)
3. `npm run check` + `go test ./...` before committing

---

## Testing

```sh
# Full suite (~9 s)
go test ./...

# Race-detected (~10 s) — CI runs this
go test -race ./...

# Coverage summary
go test -cover ./...

# Hot-path benchmarks
go test -bench=. ./internal/pipeline/

# Frontend
cd web
npm run check          # svelte-check (0 errors / 0 warnings = green)
npm test               # vitest — 88 tests across stores, components, routes
```

**End-to-end (no external broker):** [`internal/pipeline/e2e_test.go`](internal/pipeline/e2e_test.go) seeds storage, uploads a JSON Schema, configures filter + transform stages, drives messages through in-memory MQ connectors, and asserts both the destination payload and the DLQ contents.

**Real-broker integration tests:** behind `-tags integration`, skipped without the relevant env var.

```sh
# RabbitMQ
docker compose up -d rabbitmq
RABBIT_URL=amqp://mqc:mqc-dev@localhost:5672 \
  go test -tags integration -run TestIntegration_RabbitMQ ./internal/pipeline/

# IBM MQ (requires the ibmmq build tag + linux/amd64)
IBMMQ_URL=... go test -tags 'integration ibmmq' ./internal/pipeline/
```

### Hot-path benchmarks (Apple M5, in-process)

| Stage | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `Detect_JSON` | 1.8 | 0 | 0 |
| `Detect_XML`  | 3.9 | 0 | 0 |
| `Filter_JSON` | 43 µs | 23 KB | 580 |
| `Filter_XML`  | 89 µs | 52 KB | 1327 |
| `Transform_RenameMask` | 52 µs | 26 KB | 612 |
| `Translate_JSON→XML` | 47 µs | 31 KB | 585 |
| `Route_5Rules` | 27 µs | 17 KB | 402 |
| `Script_AssignDelete` | 47 µs | 23 KB | 588 |
| `Validate_JSONSchema` | 27 µs | 17 KB | 394 |
| `Pool_Get_cached` | 117 ns | 16 B | 1 |
| `Pool_Send_cached` | 152 ns | 64 B | 2 |

### Coverage (last green CI run)

| Package | Coverage |
|---|---:|
| `health` | 100% |
| `metrics` | 100% |
| `mqcfg` | 100% |
| `config` | 86% |
| `logging` | 82% |
| `dlq` | 79% |
| `pipeline` | 69% |
| `auth` | 66% |
| `storage` | 63% |
| `server` | 50% |
| `mq` | 43% |

---

## Troubleshooting

**`pattern all:dist: no matching files found` at compile time** — `internal/web/dist/` is empty. Either run `cd web && npm run build` first or just `touch internal/web/dist/.gitkeep`.

**Docker build fails with `git: not found`** — old `golang:1.23-alpine` base. Repo now uses `golang:1.25-alpine` + installs git. Pull the latest source.

**`go.mod requires go >= 1.25.0`** — your local Go toolchain is older than 1.25. Bump via Homebrew (`brew upgrade go`), the official installer, or use the Dockerfile's golang base.

**SimpleAuth not reachable from container** — the compose stack uses internal DNS (`simpleauth:8080`). If you're running mqConnector outside compose but SimpleAuth in compose, point `auth.simpleauth.base_url` at `https://localhost:9443/`.

**`could not read Username for 'https://github.com'` during `go mod download`** — the simpleauth-go replace directive can't resolve. Either provide the SDK as a build context (compose / `docker buildx --build-context simpleauth-sdk=...`) or rewrite the replace target to a real path.

**UI loads but every API call returns 401** — your SimpleAuth session cookie isn't reaching the API. Check `Set-Cookie` from `/api/auth/login`; on first-party deploys the cookie should be `Secure; SameSite=Strict; Path=/`. CORS misconfiguration won't show this — auth failures will.

**Pipeline runs but nothing reaches the destination** — check `/api/metrics` for `messages_failed`. If failures are growing, the messages are in the DLQ (`/api/v1/dlq`). Each row has the original payload and the rejection reason.

**High latency on `/api/v1/dlq`** — the DLQ table has indexes on `(tenant_id, created_at)` and `(tenant_id, pipeline_id)`. If you're filtering on `error_reason LIKE '%...%'` against millions of rows, expect a sequential scan. Use the time-range filter to bound it.

---

## License

[MIT](LICENSE). Free to use, modify, redistribute — commercially or otherwise. No warranty.
