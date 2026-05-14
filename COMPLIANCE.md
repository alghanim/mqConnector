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
| TLS everywhere (no exceptions in non-dev) | ✅ | Config validation rejects `tls.enabled: false` unless `mode: dev` |
| No hardcoded secrets | ✅ | All secrets are config values; example file uses placeholders |
| Input size limits on uploads / publish bodies | ✅ | 10 MB cap on REST→MQ publish; 10 MB cap on sample uploads |
| Auth on all mutation endpoints | ✅ | `RequireSession` middleware on every `/api/*` except `/api/auth/login` and `/api/health` |
| SQL parameter binding (no string concat) | ✅ | `database/sql` `?` placeholders only |
| Password storage (SimpleAuth handles hashing) | ✅ | bcrypt via SimpleAuth |

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

---

## Deviations

None.
