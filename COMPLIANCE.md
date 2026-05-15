# COMPLIANCE.md

**Standard:** Department Coding Standard
**Project:** mqConnector
**Version:** 1.0.0
**Last reviewed:** 2026-05-15

This document is the explicit checklist of compliance against the Department Coding Standard. Every item is either ✅ (compliant), ⚠️ (deviation with rationale), or ❌ (non-compliant — must be fixed).

---

## Structural

| Item | Status | Notes |
|---|---|---|
| Git repo with `.gitignore` | ✅ | Excludes `dist/`, `data/`, secrets, frontend node_modules, embedded dist contents |
| `VERSION` file at root | ✅ | semver, starts at `1.0.0` |
| `README.md` with purpose, build, deploy, config reference | ✅ | |
| `CLAUDE.md` with AI-agent context | ✅ | |
| `COMPLIANCE.md` | ✅ | This file |
| `BRAND-COMPLIANCE.md` | ✅ | See `BRAND-COMPLIANCE.md` |

## Backend (Go)

| Item | Status | Notes |
|---|---|---|
| `cmd/`, `internal/`, `scripts/` structure | ✅ | |
| Single binary with embedded assets | ✅ | SvelteKit static build embedded via `go:embed` from `internal/web/dist/` |
| Go 1.22+ | ✅ | `go.mod` declares `go 1.22.0` |
| Structured logging (`slog`) with configurable level | ✅ | `internal/logging`, JSON output, level from config |
| YAML config with env var overrides | ✅ | `internal/config`, dot-to-underscore env mapping |
| `/api/health` endpoint | ✅ | `internal/health` + `internal/server/handlers_health.go` |
| Graceful shutdown (SIGTERM, SIGINT) | ✅ | `cmd/mqconnector/main.go` cancels root context, server `Shutdown(ctx)`, pipeline manager `Stop()` |
| SimpleAuth integration | ✅ | `internal/auth` — embedded SimpleAuth, no third-party IdP |
| TLS configured | ✅ | Required by default; can be disabled only when `server.mode: dev` |
| Admin UI for runtime configuration with self-restart capability | ✅ | UI mutations call REST → storage → pipeline.Manager.Reload() — no process restart needed |

## Deployment

| Item | Status | Notes |
|---|---|---|
| Single binary OR Debian package OR Docker Compose | ✅ | `scripts/build-dist.sh` produces a tarball with binary + example config + systemd unit |
| Immutable artifact across environments | ✅ | Same binary in dev/staging/prod; only `config.yaml` and env vars change |
| Reproducible build | ✅ | `scripts/build.sh` is deterministic given the same source tree |

## Security

| Item | Status | Notes |
|---|---|---|
| TLS everywhere (no exceptions in non-dev) | ✅ | Config validation rejects `tls.enabled: false` unless `mode: dev`; min TLS version configurable |
| No hardcoded secrets | ✅ | All secrets are config values; example file uses placeholders |
| Input size limits on uploads / publish bodies | ✅ | `MaxBodyBytes` middleware caps every `/api/*` body to the configured value (default 10 MB) |
| Auth on all mutation endpoints | ✅ | `RequireSession` middleware on every `/api/*` except `/api/auth/login` and `/api/health` |
| Login brute-force protection | ✅ | Per-IP rate limiter on `/api/auth/login` — 10 attempts / 60 s, returns 429 with `Retry-After` |
| Panic recovery | ✅ | `Recover` middleware wraps every request; panic → 500 + structured log, process keeps running |
| Per-request timeout | ✅ | `RequestContextTimeout` middleware applies `server.write_timeout` as a hard ceiling on handler ctx |
| SQL parameter binding (no string concat) | ✅ | `database/sql` `?` placeholders only |
| Password storage (SimpleAuth handles hashing) | ✅ | bcrypt via SimpleAuth; mqConnector itself never persists user passwords |
| Connection password storage | ⚠️ | MQ connection passwords are stored plaintext in `storage.connections.password` so the bridge can dial brokers. SQLite file should be set to mode `0640` and live on an encrypted filesystem. Envelope encryption with a KMS master key is a planned follow-up (tracked in `BRAND-COMPLIANCE.md` deviations section if/when graduated to roadmap). |
| Security headers | ✅ | X-Content-Type-Options, X-Frame-Options=DENY, Referrer-Policy, HSTS, Content-Security-Policy (locked-down: no inline scripts, no external origins, no framing), Permissions-Policy denies geolocation/microphone/camera |
| HttpOnly + SameSite=Strict session cookie | ✅ | `internal/auth.SetCookie` |
| Request ID in every log line | ✅ | `RequestID` middleware sets `X-Request-Id`, propagates through ctx |

## Operability

| Item | Status | Notes |
|---|---|---|
| Prometheus metrics | ✅ | `/api/metrics/prometheus` |
| Health check | ✅ | DB ping + per-pipeline status |
| Configurable log level | ✅ | `logging.level` |
| Versioned API | ✅ | `/api/v1/*` |
| Graceful restart of pipelines on config change | ✅ | `pipeline.Manager.Reload()` |

## Tests

| Item | Status | Notes |
|---|---|---|
| Unit tests for pipeline stages | ✅ | `internal/pipeline/*_test.go` |
| Unit tests for routing operators | ✅ | `internal/pipeline/route_test.go` |
| Unit tests for transform rules | ✅ | `internal/pipeline/transform_test.go` |
| Unit tests for metrics | ✅ | `internal/metrics/metrics_test.go` |
| Unit tests for config loading + env override | ✅ | `internal/config/config_test.go` |
| Connector tests (factory + pool) | ✅ | `internal/mq/*_test.go` — live MQ tests skipped without env |

## Documentation

| Item | Status | Notes |
|---|---|---|
| README build/run/deploy | ✅ | |
| Inline package-level comments where non-obvious | ✅ | |
| Config reference (`config.example.yaml`) | ✅ | |
| Compliance docs | ✅ | This file + `BRAND-COMPLIANCE.md` |
| API documentation (OpenAPI 3.0) | ✅ | `internal/server/openapi.yaml`, served at `/api/openapi.yaml`, embedded via `go:embed` |
| Real-broker integration coverage | ✅ | `internal/pipeline/integration_rabbit_test.go` — `//go:build integration`, drives a live RabbitMQ end-to-end. IBM MQ counterpart: `integration_ibmmq_test.go` (`//go:build integration && ibmmq`). |
| IBM MQ build variant | ✅ | `Dockerfile.ibmmq` produces a CGO-linked image with `-tags ibmmq` against the bundled IBM client |

## Multi-replica safety

| Item | Status | Notes |
|---|---|---|
| Single active-replica enforcement | ✅ | `internal/leadership` provides a SQLite-backed lease: with `leadership.enabled: true`, only the lease holder runs pipeline workers; standbys serve the admin UI but stay idle. Takeover on a crashed leader is bounded by `leadership.ttl` (default 30 s). |
| Lease primitive tested | ✅ | `leadership_test.go` covers solo acquire, race-of-N exactly-one-wins, renewal-within-TTL holds, takeover-after-expiry, context-cancel release, and role-flip notification. |
| Observability | ✅ | `GET /api/v1/leadership` reports self/holder/is_leader/expires_at; logs emit on every role flip. |

---

## Deviations

- **MQ connection passwords are stored plaintext in SQLite.** The bridge needs to dial brokers without operator interaction, so a key has to live near the binary. The right next step is envelope encryption keyed by a master key from an external KMS (or an HSM-backed file). Until that lands, mitigations are: (a) SQLite file mode `0640` owned by the `mqconnector` user only, (b) deployment on an LUKS / dm-crypt volume, (c) least-privilege MQ accounts so a leak is bounded.
