# API request flow — HTTP request lifecycle

How an HTTP request is handled, from TLS accept to JSON response. For the message-processing flow (broker → pipeline → broker), see [REQUEST_FLOW.md](REQUEST_FLOW.md).

---

## The 30-second version

```
Client
  │
  ▼
TLS accept (chi.Server, std net/http)
  │
  ▼
┌──────────────────────────────────────────────────────────┐
│ Middleware chain (every request, in order)               │
│                                                          │
│   1. Recover           panic → structured log + 500     │
│   2. Logger injection  slog attached to ctx              │
│   3. RequestID         X-Request-Id assigned / honored   │
│   4. TraceContext      W3C traceparent propagation       │
│   5. SecurityHeaders   HSTS, CSP, X-Frame-Options, etc.  │
│   6. LogRequests       structured access log on response │
│   7. CORS              prod-mode strict allowlist        │
│   8. MaxBodyBytes      4 MB default                      │
│   9. RequestContextTimeout  hard ctx deadline            │
└──────────────────────────────────────────────────────────┘
  │
  ▼
Public routes? ────────────────► handle directly
  │ no
  ▼
┌──────────────────────────────────────────────────────────┐
│ Authenticated group middleware                           │
│                                                          │
│  10. RequireSession    SimpleAuth cookie OR Bearer token │
│  11. rateLimitTenant   token-bucket per tenant_id        │
│  12. AuditAdminActions records mutations on response     │
└──────────────────────────────────────────────────────────┘
  │
  ▼
chi mux → handler
  │
  ▼
handler: decodeJSON → storage → encodeJSON
  │
  ▼
response unwound back through the middleware chain:
  AuditAdminActions records (if mutation)
  LogRequests emits access-log line
  RequestID copies to X-Request-Id response header
```

The chain is defined in [`internal/server/routes.go`](../internal/server/routes.go). The order is load-bearing — `Recover` first so a panic in any other middleware still produces a structured 500; `RequestID` early so every log line in the chain carries the same id.

---

## Listener

`internal/server/server.go` constructs a `http.Server` with:

| Knob | Default | Why |
|---|---|---|
| `ReadHeaderTimeout` | 5 s | Defends against slow-loris. |
| `ReadTimeout` | 30 s | Reading the full body. |
| `WriteTimeout` | 60 s | Total response send time. |
| `IdleTimeout` | 120 s | Keep-alive grace. |
| `MaxHeaderBytes` | 1 MB | Cap on request headers. |
| `TLSConfig.MinVersion` | TLS 1.2 | Lower is refused. |

TLS is mandatory in `mode: prod`. The server refuses to start without `cert_file` + `key_file`. In dev mode, plaintext HTTP is allowed.

The router (`chi`) is configured in `routes()`. Middlewares are `r.Use(...)` calls at the top; routes are chi `Route` / `Group` blocks below.

---

## Middleware chain

### 1. Recover — `internal/server/middleware_recover.go`

```go
func Recover(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if rec := recover(); rec != nil {
                logging.FromContext(r.Context()).Error("panic",
                    "err", fmt.Sprint(rec),
                    "stack", string(debug.Stack()))
                writeError(w, http.StatusInternalServerError, "internal error")
            }
        }()
        next.ServeHTTP(w, r)
    })
}
```

Catches panics and converts them to a JSON 500. **Outermost** middleware — must wrap every other layer so it can recover from middleware panics too.

### 2. Logger injection

Attaches the server's `*slog.Logger` to the request context. Every handler retrieves it via `logging.FromContext(ctx)` — there is no global logger. This is also where the `request_id` and `trace_id` get attached as logger attributes once those middlewares fire.

### 3. RequestID

```go
id := r.Header.Get("X-Request-Id")
if id == "" {
    id = uuid.NewString()
}
ctx := IntoContext(r.Context(), id)
w.Header().Set("X-Request-Id", id)
```

Reuses the inbound `X-Request-Id` if present (so an upstream load balancer can correlate); generates a new UUID otherwise. The id ends up on every log line and on the response header.

### 4. TraceContext — `internal/tracing/context.go`

Parses the W3C `traceparent` header (`00-<trace-id>-<parent-span-id>-<flags>`). If present, the trace_id propagates to:
- structured logs
- outbound webhook deliveries (`traceparent` re-emitted)
- outbound MQ messages with `traceparent` in headers (RabbitMQ x-headers, Kafka headers, MQTT user-properties on v5)

Missing or malformed `traceparent` is a no-op — the request is logged with a random trace id derived from the request id.

### 5. SecurityHeaders

Sets on every response:

| Header | Value |
|---|---|
| `Strict-Transport-Security` | `max-age=63072000; includeSubDomains` (prod only) |
| `X-Frame-Options` | `DENY` |
| `X-Content-Type-Options` | `nosniff` |
| `Referrer-Policy` | `strict-origin-when-cross-origin` |
| `Permissions-Policy` | conservative — no camera, microphone, geolocation |
| `Content-Security-Policy` | strict source allowlist; `script-src 'self' 'nonce-<random>'` |

The CSP nonce is generated per-request and injected into the SvelteKit HTML output by a templating step on the embedded-fs handler.

### 6. LogRequests

Wraps the handler with a response interceptor and emits one structured log line on response:

```json
{
  "level":"INFO",
  "msg":"http",
  "method":"GET",
  "path":"/api/v1/pipelines",
  "status":200,
  "duration_ms":12,
  "request_id":"...",
  "trace_id":"...",
  "remote_ip":"...",
  "user_sub":"...",
  "tenant_id":"..."
}
```

`user_sub` and `tenant_id` are present only for authenticated requests (RequireSession populates them downstream of this middleware, but LogRequests reads them on the response, after the handler ran).

### 7. CORS

Configured by `server.cors_origins` in `config.yaml`. The allowlist is exact-match (no wildcards in prod). In dev mode the allowlist is permissive. The middleware emits `Access-Control-Allow-Origin` only when the inbound `Origin` matches.

### 8. MaxBodyBytes

Wraps `r.Body` with `http.MaxBytesReader`. Defaults to 4 MB. Hit limit → `413 Request Entity Too Large`. The cap protects every handler downstream from unbounded reads.

### 9. RequestContextTimeout

```go
ctx, cancel := context.WithTimeout(r.Context(), timeout)
defer cancel()
next.ServeHTTP(w, r.WithContext(ctx))
```

Hard deadline on the request context. Storage calls, MQ probes, downstream HTTP — all honour `ctx.Done()`. Without this, a stuck downstream pins a goroutine until `WriteTimeout` cycles the connection.

---

## Public routes (no auth)

| Method | Path | Handler |
|---|---|---|
| `GET` | `/api/health` | `handleHealth` — DB ping + metrics snapshot |
| `GET` | `/api/openapi.yaml` | `handleOpenAPI` — serves the embedded spec |
| `POST` | `/api/auth/login` | `handleLogin` — rate-limited per IP |
| `POST` | `/api/auth/refresh` | `handleRefresh` — rate-limited per IP |
| `GET` | `/*` (SPA fallback) | `internal/web` — embedded SvelteKit |

`rateLimitLogin` is a small in-memory token-bucket keyed by remote IP, with a 5 req/min budget. It bypasses the standard tenant bucket because the user isn't authenticated yet.

---

## Authenticated routes

Everything under `/api/v1/*` goes through three additional middlewares:

### 10. RequireSession — `internal/auth/middleware.go`

```go
func (a *Auth) RequireSession(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        user, err := a.userFromRequest(r)
        if err != nil {
            writeError(w, http.StatusUnauthorized, "auth required")
            return
        }
        ctx := IntoContext(r.Context(), user)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

Two acceptable credential types:

1. **Cookie session** — `mqc_session=<token>`. The middleware verifies the token with SimpleAuth (HTTP call to `<simpleauth_url>/api/auth/verify`), caches the result for 30 s.
2. **Bearer API token** — `Authorization: Bearer mqct_<hex>`. Looked up directly against the `api_tokens` table; the token row carries `tenant_id` + `role` + optional expiry. No external call.

The resolved `*User` (with sub, name, roles, active tenant) is injected into the request context. Downstream handlers retrieve it via `ContextUser(ctx)`.

The active tenant is resolved by `internal/server/tenant_resolver.go`:
1. `X-Tenant-Id` header if present and user is a member.
2. The user's last-active tenant from the session.
3. The user's first membership (alphabetical) as fallback.

### 11. rateLimitTenant — `internal/server/ratelimit_tenant.go`

Token-bucket keyed by tenant_id. Budget comes from the tenant row's `max_msgs_per_minute`. A 429 response is the only path that doesn't reach the handler — and intentionally doesn't reach the audit middleware below either (avoids spurious audit rows for throttled requests).

### 12. AuditAdminActions — `internal/audit/middleware.go`

Records every successful mutation (`POST`, `PUT`, `DELETE`) on a `/api/v1/*` path to the `audit` table:

```
{
  id, tenant_id, at, actor, actor_sub, action, resource,
  status, request_id, remote_ip,
  prev_hash, hash    -- SHA-256(prev_hash || row_payload)
}
```

For `PUT` requests, the audit row also captures a structured before/after diff in the `audit_diff` table, queryable via `/api/v1/audit/{id}/diff`. The diff is computed by re-reading the row pre-mutation in the same transaction.

The chain is verifiable: `/api/v1/audit/verify` walks the table in order, recomputing `hash` at every step. A mismatch means tampering or storage corruption.

---

## Per-route detail

The full route table lives in [`internal/server/routes.go`](../internal/server/routes.go). Highlights:

### Connections — CRUD MQ connections

```
GET    /api/v1/connections           list
POST   /api/v1/connections           create
GET    /api/v1/connections/{id}      get one
PUT    /api/v1/connections/{id}      update
DELETE /api/v1/connections/{id}      delete
POST   /api/v1/connections/{id}/test probe broker
```

Create / update encrypt the broker password with the current encryption key (the new row stores `password_kid` = key version). Get returns the row with the password redacted to a placeholder. Test opens a transient connection, sends a no-op (e.g. RabbitMQ exchange declare, Kafka metadata fetch), reports back.

### Pipelines — pipeline definitions

```
GET    /api/v1/pipelines             list
POST   /api/v1/pipelines             create
GET    /api/v1/pipelines/{id}        get one
PUT    /api/v1/pipelines/{id}        update (hot-reload trigger)
DELETE /api/v1/pipelines/{id}        delete

GET    /api/v1/pipelines/{id}/stages, transforms, routing-rules
PUT    /api/v1/pipelines/{id}/stages, transforms, routing-rules   replace
```

Replacement is full-set replacement: the request body is the new ordered list, the handler diffs against the existing rows in a transaction. PUT on a `/stages` endpoint triggers a hot reload of just the affected pipeline.

### DLQ

```
GET    /api/v1/dlq                   list (filterable, paginated)
POST   /api/v1/dlq/{id}/retry        re-publish through the pipeline
DELETE /api/v1/dlq/{id}              delete
```

Retry doesn't shortcut the pipeline — it pushes the original message back into the source's logical position (via a private API on the pipeline manager). The pipeline's stages run from scratch.

### Bridge — REST ↔ MQ

```
POST /api/v1/bridge/publish/{connectionId}   REST body → MQ publish
POST /api/v1/bridge/consume/{connectionId}   MQ receive → REST body
```

Useful for: testing pipelines without a real publisher; ad-hoc message generation; one-off consume during a debug session.

### Server-sent events

```
GET /api/v1/events       -> text/event-stream
```

The admin UI subscribes here for live metrics, DLQ totals, and health changes. The handler:
1. Sets `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`.
2. Subscribes the response writer to `internal/events.Publisher`.
3. Heartbeats every 15 s.
4. Closes the subscription on ctx.Done.

Browser EventSource auto-reconnects with exponential backoff — the UI falls back to polling if it can't establish the stream.

### OpenAPI

```
GET /api/openapi.yaml
```

Returns the contents of `internal/server/openapi.yaml` (embedded). All `/api/v1/*` routes are documented there. SDK generators can pull it directly:

```
npx @openapitools/openapi-generator-cli generate \
  -i https://your.host/api/openapi.yaml \
  -g typescript-fetch -o sdk/ts
```

---

## Handler shape

Almost every handler follows the same shape:

```go
func (s *Server) handleCreatePipeline(w http.ResponseWriter, r *http.Request) {
    user := ContextUser(r.Context())
    logger := logging.FromContext(r.Context())

    var dto pipelineDTO
    if err := decodeJSON(r, &dto); err != nil {
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    if err := validatePipelineDTO(dto); err != nil {
        writeError(w, http.StatusUnprocessableEntity, err.Error())
        return
    }

    pipeline := dtoToPipeline(dto, user.TenantID)
    if err := s.store.Pipelines.Insert(r.Context(), pipeline); err != nil {
        logger.Error("insert pipeline", "err", err)
        writeError(w, http.StatusInternalServerError, "insert failed")
        return
    }

    // Trigger hot reload — async, doesn't block the response.
    s.manager.ReloadAsync()

    writeJSON(w, http.StatusCreated, pipelineToDTO(pipeline))
}
```

Conventions:
- `decodeJSON` uses `DisallowUnknownFields()`. Stray fields in the body → 400.
- `writeError` is a JSON shape: `{"error":"...","request_id":"..."}`.
- `writeJSON` sets the content-type and writes via `encoding/json`.
- Handlers do not call `panic` for control flow — every error path is an explicit write.
- Handlers do not call out to the MQ pool synchronously; long operations either use a worker queue (webhook dispatch) or return early with a 202.

---

## Response flow

The middleware chain unwinds in reverse order:

```
handler writes response
  ↓
AuditAdminActions:
  if status >= 200 && status < 400 && method in {POST,PUT,DELETE}:
    insert audit row + diff (for PUT)
  ↓
rateLimitTenant: no-op on response
  ↓
RequireSession: no-op on response
  ↓
RequestContextTimeout: no-op (cancel fires on defer)
  ↓
MaxBodyBytes: no-op on response
  ↓
CORS: response headers written
  ↓
LogRequests: emits the structured access log
  ↓
SecurityHeaders: response headers written
  ↓
TraceContext: no-op on response
  ↓
RequestID: copies request_id to response header
  ↓
Logger injection: no-op on response
  ↓
Recover: no-op unless panic recovered
  ↓
chi flushes response to client
```

---

## Error envelopes

Every error response is JSON:

```json
{
  "error": "human-readable summary",
  "request_id": "..."
}
```

Status codes follow the HTTP norms:

| Status | When |
|---|---|
| 400 | Malformed JSON, unknown fields, validation failure on the request shape |
| 401 | Auth missing or invalid |
| 403 | Auth valid but role insufficient |
| 404 | Resource not found in current tenant |
| 409 | Unique-constraint violation (e.g. duplicate connection name) |
| 413 | Body over MaxBodyBytes |
| 422 | Request shape valid, semantic invariant violated (e.g. routing rule references a non-existent connection) |
| 429 | Tenant rate-limit exhausted, or login-bucket exhausted |
| 500 | Unexpected — stack already logged with the request_id |
| 503 | Storage unavailable (DB connection lost) |

---

## Tracing the path of a single request

A `POST /api/v1/pipelines/<id>` from a typical UI session produces this structured-log sequence (interleaved with the actual handler logs):

```
{"msg":"http","method":"POST","path":"/api/v1/pipelines/<id>","request_id":"r1","status":200,"duration_ms":18}
   ↑                                                                 ↑
   LogRequests on response                                            from RequestID middleware

# during the request, the same request_id appears on:
{"msg":"auth verify","sub":"u-...","tenant_id":"t-...","request_id":"r1"}
{"msg":"audit row","resource":"pipeline:<id>","action":"PUT","status":200,"request_id":"r1"}
{"msg":"pipeline reload triggered","reason":"pipeline-updated","request_id":"r1"}
{"msg":"pipeline starting","pipeline_id":"<id>","request_id":"r1"}
```

The `request_id` is the single thread that ties the HTTP call to the manager reload it triggered and the executor start. Same on `traceparent` if upstream supplies it.

---

## Where the bodies are buried

- **Login is unauthenticated but rate-limited.** A per-IP bucket sits in front of `/api/auth/login` AND `/api/auth/refresh`. Same budget — refresh isn't free.
- **`AuditAdminActions` is post-response.** If the response is 200 but the audit insert fails (DB issue), the user already saw "success". We log the failure but don't fail the request. The audit table is for forensics, not for transactional correctness — the action already happened.
- **Tenant resolution is sticky.** Once `X-Tenant-Id` is set on a session, subsequent requests without the header keep using the same tenant via the session. Switching tenants requires either a header or a `POST /api/v1/tenants/{id}/switch`.
- **The SSE handler holds a goroutine per client.** If a UI session gets stale and the TCP keepalive doesn't catch it, the connection sits open until `WriteTimeout` cycles it (60 s default). Watch goroutine count on `/api/health/debug` (admin only) when scaling.
- **CORS is whitelist-only in prod.** No wildcard fallback. If your UI is on a different origin than the API, name it explicitly in `server.cors_origins`.
