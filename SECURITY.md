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
- **mqconnector → SimpleAuth**: shared `AUTH_ADMIN_KEY` for the bootstrap path; bearer JWT for normal validation. No anonymous client trust.
- **Broker → mqconnector**: TLS server-cert verification when `tls_ca_file` is set. Pinning the CA is the recommended posture; `tls_insecure_skip_verify` is a dev escape hatch that logs at startup.

### Tampering (T)

- **Audit log**: SHA-256 hash chain (migration 0005). Each row's `hash = sha256(prev_hash || canonical(row))`; verifier walks per-tenant chains in `AuditRepo.Verify` and pinpoints the first divergent row. A single-row UPDATE outside the API is detectable in O(n). Exposed at `GET /api/v1/audit/verify`.
- **Audit diffs**: PUT request bodies are captured (capped at 64 KiB) in `audit_log_diffs` so a reviewer can reconstruct what a mutation intended. Wired in `internal/server/middleware_audit.go`.
- **Data in transit**: TLS 1.2+ everywhere; minimum version is enforced in both the HTTP server (`server.go`) and the broker TLS dialers (`internal/mq/tls.go`).
- **CSP nonce per request**: `SecurityHeaders` generates a fresh 16-byte nonce and pins inline `<script>` tags to it so an injected `<script>` without the nonce is dropped by the browser.

### Repudiation (R)

- Every state-changing API request lands in `audit_log` with actor, sub, action, resource, status, request-id, remote IP, and (for PUTs) a captured request body. Read paths (GET) are excluded by design to keep the signal high.
- Audit insert is best-effort but the chain serialises through `chainMu` so concurrent writers can't fork the chain.

### Information disclosure (I)

- **Broker passwords**: AES-256-GCM envelope encryption at rest (`internal/secrets`). Ciphertext is tagged with the key version (`enc:v{N}:`) and Decrypt selects the matching key, allowing live rotation. Plaintext rows from pre-encryption deploys are migrated in place by `RewrapPasswords` after a `POST /api/v1/secrets/rotate`.
- **Tenant isolation**: every row in connections, pipelines, transforms, routing_rules, scripts, schemas, DLQ, audit_log carries `tenant_id`. Storage repos enforce `ErrTenantRequired` and reject "" tenant IDs at the API boundary. Cross-tenant access is tested in `tenant_isolation_test.go`.
- **DLQ payloads**: never returned in bulk to non-authorised callers. The `/api/v1/dlq/{id}` shape requires authentication and respects tenant scope.
- **CORS**: defaults to off; only origins listed in `server.cors_origins` get `Access-Control-Allow-Origin`. Credentials require an exact origin match (no `*`).

### Denial of service (D)

- **Login rate limiting**: per-source-IP token bucket on `/api/auth/login` and `/api/auth/refresh` (10/minute) — slows credential stuffing.
- **HTTP body cap**: `MaxBodyBytes` middleware refuses requests over the configured limit. Pairs with the 30s `WriteTimeout` and per-request `RequestContextTimeout` so a stuck downstream can't pin a goroutine indefinitely. SSE streams opt out cleanly via the `Accept: text/event-stream` bypass.
- **Script sandbox**: line-eval script stage caps op count, source size, and post-encode output size (Phase 17b). A runaway script trips `ErrScriptResourceLimit` and the message goes to DLQ rather than crash-looping a worker.
- **DLQ retry limits**: per-row retry counter; further retries past `MaxRetries` reject with `ErrMaxRetries`.
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
| Session cookies | SimpleAuth (JWT / HS-style signing) | TTL + refresh; HttpOnly + Secure + SameSite=Lax |
| TLS to UI | TLS 1.2+ | Operator-provided cert/key; min version enforced in `tls.Config` |
| TLS to brokers | TLS 1.2+ | Per-connection PEM paths; CA-pinning + mTLS supported |
| CSP nonces | crypto/rand | 16 bytes per request, base64-url encoded |
| Master-key rotation | Live add-then-rewrap | New key installed via `POST /api/v1/secrets/rotate`; old keys retained for decrypt-only |

---

## 5. Operational guarantees

- **No secrets in logs**: `slog` emitters strip the `password` field before logging. The `MQC_MASTER_KEY` is read once and never serialised. Confirmed by `internal/logging` tests + grep against handler files.
- **Reproducible binary**: single `go build` produces the entire server + UI; no runtime download or CDN dependency. Strict CSP forbids external script sources.
- **Air-gapped friendly**: no telemetry, no auto-update, no calling home. SimpleAuth is the only outbound dependency at runtime.
- **Graceful shutdown**: TODO — bounded-channel backpressure + drain on SIGTERM is on the Phase 18 roadmap.

---

## 6. Known limitations (honest list)

These are known gaps, in priority order:

1. **IBM MQ TLS** is not yet wired through the connection-level `tls_*_file` columns. IBM MQ has its own TLS plumbing via CCDT and channel settings; Phase 17d covers RabbitMQ + Kafka. IBM MQ TLS is configured today by setting MQ env vars and using an IBM-formatted CCDT — out of band of mqConnector's columns.
2. **Audit log archival**: rows past the retention window are pruned but not yet exported to object storage. The export hook exists (`AuditRepo.IterOlderThan`) and S3 archival is on the Phase 20 list.
3. **SystemAdmin role**: the cross-tenant elevation today is "owner of the default tenant". An explicit flag is queued.
4. **Sandbox depth**: the script line evaluator has op-count + size caps but not memory caps on the intermediate `map[string]any`. A pathological script can still allocate ~tens of MiB before the output cap fires; the message gets DLQ'd cleanly but the worker briefly holds the allocation.
5. **CI/supply chain**: vuln scanning (Trivy/grype), SBOM emission (CycloneDX), and image signing (cosign) are not yet wired. These need CI infrastructure, not code changes.
6. **Threat model freshness**: this document must be reviewed each release. The PR template has a "did this change the threat model?" checkbox.

---

## 7. Reporting a vulnerability

Internal: file in the security ticket queue (private).
External (post-release): see `SECURITY.md` of the deployed product for the contact channel set by the deploying authority.

---

## 8. References

- `COMPLIANCE.md` — coding-standard checklist
- `internal/secrets/secrets.go` — envelope encryption
- `internal/storage/audit.go` — hash-chained audit log
- `internal/storage/connections.go` — tenant scoping enforcement
- `internal/mq/tls.go` — broker TLS dialer helper
- `internal/server/middleware.go` — security headers, CSP nonce
- `internal/server/middleware_audit.go` — audit + diff capture
- OWASP Top 10 (2021) — mappings noted inline in the modules above
