# mqConnector — Threat Model & Security Controls

This document inventories the security posture of mqConnector for a SOC 2 / ISO 27001 readiness review. It pairs each STRIDE category against the controls actually implemented in the codebase. Cite locations rather than restating intent, so a reviewer can read the same files and verify.

Scope: the single Go binary, its embedded SvelteKit UI, the SQLite (and future Postgres) store, the SimpleAuth dependency, and the outbound broker connections. Out of scope: the host OS, the brokers themselves, and the operator's secret management.

---

## 1. Assets

| Asset | Where it lives | Sensitivity |
| --- | --- | --- |
| Broker passwords | `connections.password` column | High — direct broker access |
| Master encryption key(s) | `MQC_MASTER_KEY` / `MQC_MASTER_KEYS` env | Critical — decrypts every password |
| Tenant data (connection configs, pipelines, DLQ payloads) | SQLite tables | Medium |
| Audit trail | `audit_log` + `audit_log_diffs` | High — compliance evidence |
| Session cookies | Browser `Set-Cookie` (HttpOnly, Secure) | Medium — short TTL, refresh-able |
| TLS server cert / key | Files referenced from `config.yaml` | High |
| Broker mTLS client cert / key | Files referenced from `connections.tls_*_file` | High |

---

## 2. Trust boundaries

```
operator browser ──(HTTPS, session cookie)──▶ mqConnector  ──(TCP, optional TLS)──▶ brokers
                                                     │
                                                     ├─(local FS)──▶ SQLite + PEM files
                                                     │
                                                     └─(HTTPS)─────▶ SimpleAuth (auth only)
```

- **operator ↔ mqconnector** is HTTPS-only outside dev. CSP, HSTS, X-Frame-Options, Referrer-Policy and Permissions-Policy headers are emitted by `SecurityHeaders` (`internal/server/middleware.go`).
- **mqconnector ↔ broker** uses TCP by default; per-connection mTLS is opt-in via the `tls_*_file` columns (`internal/mq/tls.go`, migration 0006).
- **mqconnector ↔ SimpleAuth** is a single trust relationship for token validation; SimpleAuth is air-gapped, no public IdP.

---

## 3. STRIDE per module

### Spoofing (S)

- **Browser → API**: SimpleAuth JWT in an HttpOnly cookie. `RequireSession` middleware (`internal/auth`) validates signature, expiry, and tenant binding before any handler runs.
- **Cross-origin POST (CSRF)**: defended by both `SameSite=Strict` on the session cookie (`internal/auth/auth.go`) and a double-submit token middleware (`requireCSRF` in `internal/server/middleware_csrf.go`). The SPA reads the non-HttpOnly `mqc_csrf` cookie and echoes it as `X-CSRF-Token`; constant-time compare on the server. Bearer-token API clients (Authorization header) bypass — not browser-driven, not vulnerable.
- **mqconnector → SimpleAuth**: shared `AUTH_ADMIN_KEY` for the bootstrap path; bearer JWT for normal validation. No anonymous client trust.
- **Broker → mqconnector**: TLS server-cert verification when `tls_ca_file` is set. Pinning the CA is the recommended posture; `tls_insecure_skip_verify` is a dev escape hatch that logs at startup.

### Tampering (T)

- **Audit log**: SHA-256 hash chain (migration 0005). Each row's `hash = sha256(prev_hash || canonical(row))`; verifier walks per-tenant chains in `AuditRepo.Verify` and pinpoints the first divergent row. A single-row UPDATE outside the API is detectable in O(n). Exposed at `GET /api/v1/audit/verify`.
- **Audit diffs**: PUT request bodies are captured (capped at 64 KiB) in `audit_log_diffs` so a reviewer can reconstruct what a mutation intended. Wired in `internal/server/middleware_audit.go`.
- **Database integrity**: `GET /api/v1/admin/integrity` runs `PRAGMA integrity_check` on demand (system-admin only). A bit-flip or partial-write corruption is detected before it cascades into the audit chain or further writes. Backups produced by `mqconnector backup --to=...` and the in-process scheduled backup worker run the same check on the produced snapshot before declaring success.
- **At-least-once delivery contract**: every connector implements `Commit` and `Nack` (`internal/mq/connector.go`). The executor holds the source ack until the destination send AND DLQ-push outcome is final, so a crash mid-pipeline causes broker redelivery rather than silent loss. Contract is enforced in `internal/pipeline/atleastonce_test.go` and the real-broker `TestIntegration_Kafka_AtLeastOnce_ResumesAfterRestart`.
- **Data in transit**: TLS 1.2+ everywhere; minimum version is enforced in both the HTTP server (`server.go`) and the broker TLS dialers (`internal/mq/tls.go`).
- **CSP nonce per request**: `SecurityHeaders` generates a fresh 16-byte nonce and pins inline `<script>` tags to it so an injected `<script>` without the nonce is dropped by the browser.

### Repudiation (R)

- Every state-changing API request lands in `audit_log` with actor, sub, action, resource, status, request-id, remote IP, and (for PUTs) a captured request body. Read paths (GET) are excluded by design to keep the signal high.
- Audit insert is best-effort but the chain serialises through `chainMu` so concurrent writers can't fork the chain.

### Information disclosure (I)

- **Broker passwords**: AES-256-GCM envelope encryption at rest (`internal/secrets`). Ciphertext is tagged with the key version (`enc:v{N}:`) and Decrypt selects the matching key, allowing live rotation. Plaintext rows from pre-encryption deploys are migrated in place by `RewrapPasswords` after a `POST /api/v1/secrets/rotate`.
- **Tenant isolation**: every row in connections, pipelines, transforms, routing_rules, scripts, schemas, DLQ, audit_log carries `tenant_id`. Storage repos enforce `ErrTenantRequired` and reject "" tenant IDs at the API boundary. Cross-tenant access is tested in `tenant_isolation_test.go`.
- **GDPR / right-to-erasure**: `DELETE /api/v1/tenants/{id}?cascade=true` runs `TenantRepo.Purge` (`internal/storage/tenants.go`) — one transaction across every per-tenant table. Audit log is intentionally retained (tamper chain is compliance evidence); operators archive it separately via the audit archiver. Tested in `tenants_purge_test.go`.
- **DLQ payloads**: never returned in bulk to non-authorised callers. The `/api/v1/dlq/{id}` shape requires authentication and respects tenant scope.
- **CORS**: defaults to off; only origins listed in `server.cors_origins` get `Access-Control-Allow-Origin`. Credentials require an exact origin match (no `*`).

### Denial of service (D)

- **Login rate limiting**: per-source-IP token bucket on `/api/auth/login` and `/api/auth/refresh` (10/minute) — slows credential stuffing.
- **Per-tenant rate limiting**: state-changing requests under cookie auth bucketed per tenant (120/minute default, per-tenant override). Read paths exempt.
- **Sensitive endpoint rate limit**: tighter per-(tenant, route) bucket (6/minute) for high-blast-radius endpoints — `/config/import`, `/secrets/rotate`, `/preview`, `/samples/extract`. Each can replace full state, run arbitrary stages, or hit a live broker; the bucket is sized for human-driven operator use.
- **HTTP body cap**: `MaxBodyBytes` middleware refuses requests over the configured limit. Pairs with the 30s `WriteTimeout` and per-request `RequestContextTimeout` so a stuck downstream can't pin a goroutine indefinitely. SSE streams opt out cleanly via the `Accept: text/event-stream` bypass.
- **Script sandbox**: line-eval script stage caps op count, source size, and post-encode output size (Phase 17b). A runaway script trips `ErrScriptResourceLimit` and the message goes to DLQ rather than crash-looping a worker.
- **DLQ retry limits**: per-row retry counter; further retries past `MaxRetries` reject with `ErrMaxRetries`.
- **DLQ retention sweeper**: `dlq.Retention` (`internal/dlq/retention.go`) bounds the DLQ table by age and row count so a sustained outage can't fill the disk. Leader-aware so multi-replica deploys don't over-prune.
- **Audit retention**: `audit.Archiver` (`internal/audit/archive.go`) streams rows older than `audit.max_age` to daily JSONL files and prunes the live table. Leader-aware.
- **Goroutine inventory**: pool-managed broker connectors plus one consumer goroutine per pipeline. No per-request goroutine spawning on the hot path.

### Elevation of privilege (E)

- **RBAC**: `viewer < operator < admin < owner` per tenant. Owner-only routes (member management, tenant settings) gate-keep in the handler via the `Role.Rank()` check.
- **System admin**: currently the owner of the default tenant. Coarse but explicit — used for cross-tenant audit verify and secrets rotation. A proper `system_admin` flag is on the roadmap but the present check is enforced consistently.
- **No path traversal in static handler**: `mountStatic` only serves files inside the embedded FS — there is no on-disk lookup that could be tricked into resolving `..`.
- **No SQL injection**: every query uses `?` placeholders.
- **No JS sandbox escape**: the script stage is a hand-written line evaluator. No `eval`, no FFI, no I/O — the entire surface is set/delete/copy on a `map[string]any` plus a fixed list of arithmetic operators.

---

## 4. Cryptography

| Use | Algorithm | Notes |
| --- | --- | --- |
| Connection password at rest | AES-256-GCM | 32-byte master key, 12-byte random nonce per row, key-version prefix (`enc:v{N}:`) |
| Audit chain | SHA-256 | One-way hash of canonical row || prev_hash |
| Session cookies | SimpleAuth (JWT / HS-style signing) | TTL + refresh; HttpOnly + Secure + SameSite=Strict |
| CSRF token | crypto/rand | 32 bytes per session, hex-encoded; non-HttpOnly so the SPA can echo it in `X-CSRF-Token` (double-submit) |
| TLS to UI | TLS 1.2+ | Operator-provided cert/key; min version enforced in `tls.Config` |
| TLS to brokers | TLS 1.2+ | Per-connection PEM paths; CA-pinning + mTLS supported |
| CSP nonces | crypto/rand | 16 bytes per request, base64-url encoded |
| Master-key rotation | Live add-then-rewrap | New key installed via `POST /api/v1/secrets/rotate`; old keys retained for decrypt-only |

---

## 5. Operational guarantees

- **No secrets in logs**: `slog` emitters strip the `password` field before logging. The `MQC_MASTER_KEY` is read once and never serialised. Confirmed by `internal/logging` tests + grep against handler files.
- **Reproducible binary**: single `go build` produces the entire server + UI; no runtime download or CDN dependency. Strict CSP forbids external script sources.
- **Air-gapped friendly**: no telemetry, no auto-update, no calling home. SimpleAuth is the only outbound dependency at runtime.
- **Graceful shutdown**: `mgr.StopAndWait(30 * time.Second)` drains in-flight pipeline messages on SIGTERM before exit. Out-of-budget shutdowns log a warning naming the pipelines still in flight so operators can identify the laggard.
- **Backup / disaster recovery**: SQLite `VACUUM INTO` snapshots via `mqconnector backup`, scheduled worker (`internal/storage/backup_worker.go`), or `GET /api/v1/admin/backup`. All three verify integrity on the produced snapshot before declaring success. See `OPERATIONS.md` for the restore procedure.
- **Supply chain**: CI runs Trivy on the built image and fails on CRITICAL/HIGH CVEs; SBOM emitted as CycloneDX JSON and retained as a workflow artifact. Both run on every PR + push to main (`.github/workflows/ci.yml`).
- **Real-broker integration tests**: CI brings up RabbitMQ + Kafka + NATS via `docker-compose.ci.yml` and runs the integration-tagged tests on every push. This is the path that caught the Kafka `MarkMessage`-without-`Commit` bug before it shipped.
- **Incident playbooks**: six on-call playbooks in `OPERATIONS.md` (process down, disk-full, DLQ flood, leader stuck, no traffic, connection-pool storm). Each is ordered "stop the bleeding → diagnose → recover" so a new on-call can follow them at 3am.

---

## 6. Known limitations (honest list)

This document is reviewed each release. The PR template has a "did this change the threat model?" checkbox. As of this release, every item previously listed has been closed; the section below tracks them for the audit trail.

### Closed in recent releases

- ~~IBM MQ TLS~~ — `internal/mq/connector_ibm_on.go` now wires `tls_ca_file` (key-repository stem) and `tls_cert_file` (certificate label) into `MQSCO`, forces `ANY_TLS12_OR_HIGHER`.
- ~~Explicit SystemAdmin flag~~ — `tenant_memberships.system_admin` column (migration 0013) replaces the implicit "owner of the default tenant" check. Backfilled for existing owners. New grants happen explicitly through `MembershipRepo.SetSystemAdmin`.
- ~~Script sandbox memory cap~~ — `MaxIntermediateBytes` (default 8 MiB), checked every 64 ops during execution. Catches a runaway append BEFORE it has burned tens of MiB. See `internal/pipeline/script.go`.
- ~~Container image signing (cosign)~~ — `.github/workflows/release.yml` builds, pushes to ghcr.io, signs keyless with cosign, attests the CycloneDX SBOM, and attaches the SBOM to the GitHub Release on every `v*` tag.
- ~~NATS JetStream replay UI~~ — `web/src/routes/pipelines/[id]/+page.svelte` ships a replay form gated on `source.type === 'kafka' || (source.type === 'nats' && source.stream_name)`. Operators drive JetStream replay from the same UI as Kafka.
- ~~S3 audit archival~~ — `cfg.Audit.S3` config block wires `internal/audit/s3.go` end-to-end. Empty credentials default to local-only (air-gapped friendly); set the four fields to upload rotated daily files to any S3-compatible store.
- ~~Graceful shutdown~~ — `mgr.StopAndWait(30s)` drains the pipeline manager on SIGTERM.
- ~~Audit log archival~~ — `internal/audit/archive.go` rolls daily JSONL files and prunes the live table. Leader-aware in multi-replica deploys.
- ~~CI vuln scanning + SBOM~~ — Trivy + Syft wired in `.github/workflows/ci.yml`. Trivy fails the build on CRITICAL/HIGH CVEs; Syft emits CycloneDX as a workflow artifact.
- ~~CSRF protection~~ — `requireCSRF` middleware + `SameSite=Strict` cookies. See section 3, Spoofing.
- ~~GDPR / right-to-erasure~~ — `TenantRepo.Purge` + `DELETE /api/v1/tenants/{id}?cascade=true`. See section 3, Information disclosure.
- ~~Backup / DR primitives~~ — `mqconnector backup` CLI, scheduled worker, admin HTTP endpoint. See section 5.
- ~~At-least-once delivery~~ — `Commit`/`Nack` on every connector; executor wires them around the source→stages→send→DLQ loop. See section 3, Tampering.

---

## 7. Reporting a vulnerability

Internal: file in the security ticket queue (private).
External (post-release): see `SECURITY.md` of the deployed product for the contact channel set by the deploying authority.

---

## 8. References

- `COMPLIANCE.md` — coding-standard checklist
- `OPERATIONS.md` — runbook + incident playbooks
- `internal/secrets/secrets.go` — envelope encryption
- `internal/storage/audit.go` — hash-chained audit log
- `internal/storage/connections.go` — tenant scoping enforcement
- `internal/storage/tenants.go` — `Purge` cascade for GDPR right-to-erasure
- `internal/mq/tls.go` — broker TLS dialer helper
- `internal/mq/connector.go` — at-least-once `Commit`/`Nack` contract
- `internal/server/middleware.go` — security headers, CSP nonce
- `internal/server/middleware_audit.go` — audit + diff capture
- `internal/server/middleware_csrf.go` — double-submit CSRF protection
- `internal/server/ratelimit_sensitive.go` — per-(tenant, route) limiter
- `internal/server/handlers_admin.go` — backup / integrity admin endpoints
- `internal/audit/archive.go` — audit retention sweep + S3 uploader
- `internal/dlq/retention.go` — DLQ retention sweep
- `internal/storage/backup_worker.go` — scheduled snapshot worker
- `internal/pipeline/atleastonce_test.go` — Commit/Nack contract tests
- `internal/pipeline/chaos_test.go` — failure-mode contract tests
- OWASP Top 10 (2021) — mappings noted inline in the modules above
