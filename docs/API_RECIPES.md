# API Recipes

curl + `jq` recipes for the operations you actually do against the mqConnector REST API. The OpenAPI spec at `/api/openapi.yaml` is the source of truth; this is the operator's cheatsheet.

## Authentication

Two paths: session cookie (browser-style) or API token (headless / CI).

### Session cookie

```sh
# Login. The response sets mqc_session + mqc_csrf cookies.
curl -sk --cacert ca.pem -c cookies.txt -b cookies.txt \
     -H 'Content-Type: application/json' \
     -d '{"username":"alice","password":"$PASSWORD"}' \
     https://mqc.svc:8443/api/auth/login

# Every subsequent state-changing call needs the CSRF token from
# the mqc_csrf cookie echoed back as a header.
CSRF=$(awk '$6=="mqc_csrf" {print $7}' cookies.txt)

# Read-only calls only need the cookie.
curl -sk --cacert ca.pem -b cookies.txt \
     https://mqc.svc:8443/api/v1/connections | jq .

# State-changing calls need the cookie AND the X-CSRF-Token header.
curl -sk --cacert ca.pem -b cookies.txt \
     -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
     -d '{"name":"prod-rabbit","type":"rabbitmq","url":"amqp://rabbit:5672","queue_name":"events"}' \
     https://mqc.svc:8443/api/v1/connections
```

### API token (preferred for CI / scripting)

API tokens are minted via the UI at `/tokens` or programmatically via `POST /api/v1/tokens`. The plaintext secret is returned **once** at creation.

```sh
export MQC_TOKEN="mqct_<hex>"
# Bearer requests bypass CSRF (not browser-driven, can't be forged
# cross-site). No cookie required, no X-CSRF-Token needed.
curl -sk --cacert ca.pem \
     -H "Authorization: Bearer $MQC_TOKEN" \
     https://mqc.svc:8443/api/v1/connections | jq .
```

## Common workflows

### Create a pipeline from scratch

```sh
MQC=https://mqc.svc:8443
H="Authorization: Bearer $MQC_TOKEN"

# 1. Source connection
SRC=$(curl -sk --cacert ca.pem -H "$H" -H 'Content-Type: application/json' \
     -d '{"name":"orders-in","type":"kafka","brokers":"kafka:9092","topic":"orders"}' \
     $MQC/api/v1/connections | jq -r .id)

# 2. Destination connection
DST=$(curl -sk --cacert ca.pem -H "$H" -H 'Content-Type: application/json' \
     -d '{"name":"orders-out","type":"rabbitmq","url":"amqp://rabbit:5672","queue_name":"orders-processed"}' \
     $MQC/api/v1/connections | jq -r .id)

# 3. Pipeline
PIPE=$(curl -sk --cacert ca.pem -H "$H" -H 'Content-Type: application/json' \
     -d "{\"name\":\"orders-bridge\",\"source_id\":\"$SRC\",\"destination_id\":\"$DST\",\"output_format\":\"same\",\"enabled\":true}" \
     $MQC/api/v1/pipelines | jq -r .id)

# 4. Stages — strip a sensitive field, transform another
curl -sk --cacert ca.pem -H "$H" -H 'Content-Type: application/json' \
     -d '[
       {"stage_order":1,"stage_type":"filter","stage_config":"{\"paths\":[\"customer.ssn\"]}","enabled":true},
       {"stage_order":2,"stage_type":"transform","stage_config":"{}","enabled":true}
     ]' \
     $MQC/api/v1/pipelines/$PIPE/stages

# 5. Reload — pick up the new pipeline immediately rather than wait
# for the next file-watcher sweep.
curl -sk --cacert ca.pem -H "$H" -X POST $MQC/api/v1/reload
```

### Drain the DLQ for one pipeline

```sh
PIPE=<pipeline-id>
for id in $(curl -sk --cacert ca.pem -H "$H" \
            "$MQC/api/v1/dlq?pipeline_id=$PIPE&limit=1000" | jq -r '.[].id'); do
  curl -sk --cacert ca.pem -H "$H" -X POST \
       "$MQC/api/v1/dlq/${id}/retry" > /dev/null
done
```

### Inspect a single DLQ entry

```sh
curl -sk --cacert ca.pem -H "$H" "$MQC/api/v1/dlq/<id>" | jq .
```

### Export + import config (gitops)

```sh
# Export the current state for a tenant
curl -sk --cacert ca.pem -H "$H" -H 'Accept: application/json' \
     "$MQC/api/v1/config/export?tenant_slug=default" > current.json

# Edit, then import back
curl -sk --cacert ca.pem -H "$H" -H 'Content-Type: application/json' \
     --data @desired.json \
     "$MQC/api/v1/config/import"
```

The supplied `mqconnector gitops` subcommand wraps this with diff + dry-run.

### Replay a Kafka or JetStream source

```sh
curl -sk --cacert ca.pem -H "$H" -H 'Content-Type: application/json' \
     -d '{"since":"2026-05-17T13:00:00Z","until":"2026-05-17T14:00:00Z"}' \
     "$MQC/api/v1/pipelines/$PIPE/replay" | jq .
# → {"messages_read":150,"messages_sent":150,"messages_dropped":0,"duration":...}
```

### Pull a sample for the pipeline editor

```sh
curl -sk --cacert ca.pem -H "$H" -H 'Content-Type: application/json' \
     -d "{\"connection_id\":\"$SRC\"}" \
     "$MQC/api/v1/samples/extract"
```

### Preview stage output (no broker side-effects)

```sh
curl -sk --cacert ca.pem -H "$H" -H 'Content-Type: application/json' \
     -d '{
       "stages":[{"stage_type":"filter","stage_config":"{\"paths\":[\"ssn\"]}"}],
       "input":"{\"id\":1,\"ssn\":\"123-45-6789\",\"name\":\"alice\"}",
       "input_format":"json"
     }' \
     "$MQC/api/v1/preview" | jq .
```

## Admin / operational

### Backup the database (system-admin only)

```sh
curl -sk --cacert ca.pem -H "$H" -o snapshot.db \
     "$MQC/api/v1/admin/backup"
sqlite3 -readonly snapshot.db 'PRAGMA integrity_check'
```

### Check database integrity

```sh
curl -sk --cacert ca.pem -H "$H" "$MQC/api/v1/admin/integrity" | jq .
# → {"ok":true,"duration_ms":42}
```

### Verify the audit chain

```sh
# Scope=all is system-admin only; otherwise scoped to current tenant.
curl -sk --cacert ca.pem -H "$H" "$MQC/api/v1/audit/verify?scope=all" | jq .
```

### Rotate the master encryption key

```sh
# Soft check: confirms a rotation is needed.
curl -sk --cacert ca.pem -H "$H" "$MQC/api/v1/secrets/status" | jq .

# Apply (system-admin only; rate-limited to 6 calls/min/tenant).
curl -sk --cacert ca.pem -H "$H" -X POST "$MQC/api/v1/secrets/rotate" | jq .
```

The CLI `mqconnector rotate-secrets` is preferred when the new key isn't already in `MQC_MASTER_KEY`; the HTTP endpoint assumes the new key is loaded.

### pprof (system-admin only)

```sh
# Heap profile, 30s CPU profile, goroutine dump
curl -sk --cacert ca.pem -H "$H" -o heap.pb.gz \
     "$MQC/api/v1/admin/pprof/heap"
go tool pprof -http=:9999 heap.pb.gz

curl -sk --cacert ca.pem -H "$H" -o cpu.pb.gz \
     "$MQC/api/v1/admin/pprof/profile?seconds=30"

curl -sk --cacert ca.pem -H "$H" \
     "$MQC/api/v1/admin/pprof/goroutine?debug=2"
```

## Tenant management

### Create a new tenant (system-admin only)

```sh
curl -sk --cacert ca.pem -H "$H" -H 'Content-Type: application/json' \
     -d '{"slug":"team-payments","name":"Payments Team","max_pipelines":50}' \
     "$MQC/api/v1/tenants"
```

### Switch the active tenant for the current session

```sh
curl -sk --cacert ca.pem -b cookies.txt -H "X-CSRF-Token: $CSRF" \
     -X POST "$MQC/api/v1/tenants/<id>/switch"
```

### Cascade-delete a tenant (GDPR right-to-erasure)

```sh
# Owner of the target tenant.
curl -sk --cacert ca.pem -H "$H" -X DELETE \
     "$MQC/api/v1/tenants/<id>?cascade=true"
# → {"status":"deleted","mode":"cascade"}
```

Audit log is intentionally preserved — archive it separately if compliance retention runs out.

## Watching events in real time

```sh
# Server-sent event stream of pipeline lifecycle / DLQ activity.
curl -sk --cacert ca.pem -H "$H" -N \
     "$MQC/api/v1/events"
# → data: {"type":"pipeline_started","pipeline_id":"...","tenant_id":"..."}
```

Useful for piping into a terminal dashboard during a deploy or incident.

## Troubleshooting recipes

### Why is my pipeline showing status=error?

```sh
curl -sk --cacert ca.pem -H "$H" "$MQC/api/health" \
  | jq '.connections[] | select(.status != "ok")'
```

### Which pipelines are processing right now?

```sh
curl -sk --cacert ca.pem -H "$H" "$MQC/api/metrics" \
  | jq '.[] | select(.messages_processed > 0) | {pipeline_id, source, dest, messages_processed, avg_latency_ms}'
```

### What changed in the last hour?

```sh
SINCE=$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)
curl -sk --cacert ca.pem -H "$H" \
     "$MQC/api/v1/audit?since=$SINCE&limit=200" \
  | jq '.[] | {at, actor, action, resource, status}'
```
