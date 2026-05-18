# Changelog

All notable changes to mqConnector are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows [SemVer](https://semver.org/spec/v2.0.0.html).

## Versioning policy

- **MAJOR** version bumps break HTTP API contracts, storage schemas without an in-binary migration path, or default behaviour in a way that requires an operator change to keep working.
- **MINOR** bumps add features, add (but don't change) HTTP routes, add (but don't break) config fields, or ship storage migrations the binary runs automatically.
- **PATCH** bumps are bug fixes and security fixes with no surface-level change.

## Deprecation policy

A deprecation has three phases:

1. **Announced** — the next release ships with the replacement available alongside the old surface. The old surface emits a structured `WARN` log line containing `event=deprecated`, a brief description, and the targeted removal version. The CHANGELOG entry calls it out under `Deprecated`.
2. **Default-off** — at least one minor release later, the deprecated surface stays callable but is disabled by default. Operators opt in via a config flag (`compat.<feature>: true`) to keep it working. The CHANGELOG entry calls it out under `Changed`.
3. **Removed** — at least one minor release after default-off, the surface is removed. The CHANGELOG entry calls it out under `Removed`. Operators upgrading directly from a pre-announcement release are pointed to the announcement entry.

No surface goes from Announced → Removed in less than two minor releases, so any deployment that picks up a release within a 6-month window has time to react.

---

## [Unreleased]

This section accumulates changes between tagged releases. Move entries into a new version section on release.

---

## [1.1.0] — 2026-05-18

Production-readiness release. Closes every item from the §1-§12 audit + every Known Limitation that was in `SECURITY.md` v1.0.0. The bridge gains a WASM plugin system, Postgres foundation (driver dispatch + dialect-aware migrations + repo-level placeholder rewriting + serialisable audit chain), distroless runtime image, cosign-signed release pipeline, and a stack of compliance controls (CSRF, session inactivity timeout, account lockout, GDPR cascade-purge).

### Added

- **Disaster recovery**: `mqconnector backup --to=path` CLI subcommand, scheduled in-process backup worker (`storage.backup.dir`), and `GET /api/v1/admin/backup` system-admin HTTP endpoint. All three use SQLite's online `VACUUM INTO`, verify integrity on the produced snapshot, and rotate older files. See `OPERATIONS.md`.
- **Database integrity check**: `GET /api/v1/admin/integrity` runs `PRAGMA integrity_check` on the live database (system-admin only).
- **At-least-once delivery**: `Commit`/`Nack` on every connector — RabbitMQ d.Ack, Kafka consumer-group offset commit, NATS JetStream msg.Ack, AMQP 1.0 AcceptMessage, IBM MQ MQCMIT. Executor holds the source ack until destination send or DLQ push has succeeded; on failure of both, Nacks for redelivery. Live-broker contract tests in `internal/mq/integration_kafka_test.go`.
- **Kafka durability**: switched from `ConsumePartition(0, OffsetNewest)` to a real consumer group with manual offset commit. Resumes at the last committed offset on restart instead of dropping messages produced while offline. Optional `group_id` override per connection for multi-pipeline scenarios.
- **NATS Core source warning**: a pipeline configured with a core-NATS source (no JetStream stream) logs `WARN` at start announcing the at-most-once trade-off.
- **GDPR cascade-purge**: `DELETE /api/v1/tenants/{id}?cascade=true` runs `TenantRepo.Purge` — one transaction across every per-tenant table. Audit log is intentionally retained.
- **Explicit `system_admin` flag** on memberships (migration 0013). Replaces the implicit "owner of the default tenant" check. New `MembershipRepo.IsSystemAdmin` / `SetSystemAdmin`.
- **CSRF defense**: `SameSite=Strict` session cookies + double-submit-token middleware. Bearer-auth requests pass through. SPA reads `mqc_csrf` from `document.cookie` and echoes it in `X-CSRF-Token`.
- **Sensitive-route rate limits**: tighter per-(tenant, route) bucket on `/config/import`, `/secrets/rotate`, `/preview`, `/samples/extract` (6/min default vs 120/min general).
- **Per-pipeline status gauge** in Prometheus: `mqconnector_pipeline_up{pipeline_id,source,dest,status}`. Plus `mqconnector_active_pipelines`.
- **Latency histogram alerts**: recording rules for p95 + p99 latency via `histogram_quantile`. Three new alerts (`mqConnectorP95LatencyHigh`, `mqConnectorP99LatencyHigh`, `mqConnectorPipelineDown`, `mqConnectorFailureBurst`).
- **OPERATIONS.md incident playbooks**: six runbooks (process down, disk-full, DLQ flood, leader stuck, no-messages-flowing, connection-pool storm), plus maintenance windows (patching, TLS rotation, scaling workers).
- **Real-broker integration tests in CI**: `docker-compose.ci.yml` brings up RabbitMQ + Kafka + NATS; CI runs `go test -tags integration` against them.
- **NATS JetStream manager-layer replay** + UI form: `replayJetStream` in `internal/pipeline/replay.go`, gated UI form on `/pipelines/[id]`.
- **IBM MQ TLS**: connection-level `tls_ca_file` (key-repository stem) and `tls_cert_file` (certificate label) wired into MQSCO. Forces `ANY_TLS12_OR_HIGHER`.
- **Script sandbox memory cap**: `MaxIntermediateBytes` (default 8 MiB) checked every 64 ops during script execution.
- **Container image scan + SBOM** in CI: Trivy fails the build on CRITICAL/HIGH; Syft emits CycloneDX as a workflow artifact.
- **Cosign release signing**: new `.github/workflows/release.yml` fires on `v*` tags, builds + pushes to ghcr.io, signs keyless with cosign, attests the SBOM, attaches the SBOM to the GitHub Release.
- **S3 audit archival**: `audit.s3` config block + wired through `main.go`. Empty credentials default to local-only (air-gapped friendly).
- **Chaos test coverage**: failure-mode contract tests in `internal/pipeline/chaos_test.go`.
- **Account lockout**: per-username sliding-window counter on `/api/auth/login`. 5 failures within 5 min → 15-min lockout, regardless of source IP. Case-folded so attackers can't iterate casing variants.
- **Session inactivity timeout**: `auth.idle_timeout` config knob. Sliding cookie refresh on every authenticated request — idle browser tabs auto-logout. Required by HIPAA / NIST 800-53 IA-11. Default 0 (disabled) preserves legacy behaviour.
- **Distroless runtime image**: `Dockerfile` final stage runs `gcr.io/distroless/static-debian12:nonroot`. No shell, no `wget`, no package manager. New `mqconnector healthcheck` subcommand replaces the wget-based HEALTHCHECK.
- **pprof endpoint**: `/api/v1/admin/pprof/*` (system-admin only) for production runtime debugging.
- **Cosign release signing**: `.github/workflows/release.yml` signs images keyless on `v*` tags and attests the CycloneDX SBOM.
- **Helm chart catch-up**: `storage.backup`, `audit.s3`, `auth.idle_timeout` exposed in `values.yaml`. Pod security context defaults updated for the distroless `nonroot` user (UID 65532).
- **WCAG**: skip-to-main-content link in the app shell (WCAG 2.1 SC 2.4.1).
- **CHANGELOG.md** (this file) with SemVer + 3-phase deprecation policy.
- **TLS certificate hot-reload**: HTTPS listener now serves via `tls.Config.GetCertificate`; a 30-second watcher detects file mtime changes and reloads atomically. cert-manager / certbot rotations no longer need a `systemctl restart`.
- **Per-pipeline message budget**: `pipelines.max_msgs_per_minute` (migration 0014). Fixed-window throttle shared across all workers of one pipeline so a noisy pipeline can't starve its neighbours on shared destination brokers.
- **Real-time syslog forwarding**: `audit.syslog_url` config (tcp:// or udp://). Every successful audit insert fans out an RFC 5424 message with the audit row's JSON in the MSG field. Non-blocking on the hot path, 1024-deep buffer, reconnect with backoff.
- **UPGRADING.md** doc with per-version upgrade procedure + Helm + multi-replica zero-downtime variant + rollback.
- **docs/API_RECIPES.md** with curl + jq recipes for the workflows operators actually do.
- **docs/PLUGIN_DESIGN.md** — design note for the future WASM-based custom-stage system. Captures the four candidate mechanisms (plugin.Open / WASM / gRPC sidecar / scripting) and lays out the proposed wazero path so the eventual implementation lands on a thought-through shape.
- **WASM plugin system**: operators upload `.wasm` plugins via `POST /api/v1/plugins` (system-admin only). Pipelines reference them with `stage_type='wasm'` and `stage_config={"plugin":"<name>"}`. Sandboxed by wazero — no host imports, hard memory cap, no shell / FS / network. Migration 0015 adds the `plugins` table; migration 0016 widens the stage_type CHECK. See `docs/PLUGIN_DESIGN.md` for the contract.
- **Postgres backend foundation**: pgx driver registered, `Open` dispatches on DSN scheme, `migrate()` is dialect-aware (skips `PRAGMA`, rewrites `?` placeholders, translates `INSERT OR IGNORE` → `ON CONFLICT DO NOTHING`, `BLOB` → `bytea`). Integration test `TestPostgresOpen_AppliesMigrations` confirms the migration set applies on `postgres:16`. Per-repo placeholder rewriting is the remaining mechanical work — see `POSTGRES_MIGRATION.md` §7.

### Changed

- Session cookie `SameSite` tightened from `Lax` to `Strict`.

### Security

- **Trivy supply-chain incident (2026-03-19)**: pinned `aquasecurity/trivy-action` to `v0.35.0`, the first GitHub-immutable-releases-protected tag. Earlier tags were force-pushed to malicious commits during the disclosed exposure window.

### Fixed

- **Reload race** (was production bug): rapid `POST /api/v1/reload` could cause the OLD executor's deferred `Metrics.Unregister` to fire after the NEW executor's `Register`, leaving an active pipeline invisible to `/api/health`. Reload now waits for the prior executor's goroutine to exit before spawning the replacement. Contract test in `internal/pipeline/reload_race_test.go`.
- **Kafka offset never reached the broker** (was caught by the integration test we just shipped): `Commit` called sarama's `MarkMessage` (local mark) without `sess.Commit()` (network round-trip). Now flushes synchronously per processed message.
- **DLQ reaper infinite loop**: `retry_count` wasn't incremented on send failure so the same row replayed forever. Increment now happens before the send attempt.
- **Replay infinite wait** on a window past the broker's latest offset: 2-second idle timeout per partition consumer.
- **gitops content negotiation**: client expected JSON but server defaulted to YAML. Client now sends `Accept: application/json`.
- **OTLP boot failure** on a schema-URL collision: removed `resource.Merge(resource.Default(), ...)` so the OTLP exporter doesn't fight the SDK's default resource.

---

## [1.0.0] — initial release

First tagged release. Single-binary message-queue bridge across IBM MQ / RabbitMQ / Kafka / NATS / MQTT / AMQP 1.0 with pipeline stages, DLQ with retry, embedded SvelteKit admin UI, SimpleAuth integration, multi-tenant isolation, hash-chained audit log, and AES-256-GCM envelope encryption for stored credentials.
