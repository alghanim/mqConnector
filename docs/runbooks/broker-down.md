# Broker connection refused / TLS handshake failed

Severity: page when the affected broker carries production traffic.

## Symptom

- Pipeline status shows `error` with `last_error` containing `Dial:` or `TLS handshake` or `connection refused`.
- Source-side pipelines back off and retry every 2s; logs show:
  ```
  WARN source error, backing off  err="rabbitmq Dial: ... connection refused"
  ```
- Destination-side failures show as DLQ rows with `error_reason` starting `send:`.

## Verify

```sh
# Which connection points at the broker?
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/v1/connections | \
  jq '.[] | {id, name, type, url, brokers}'

# Live-test the connection. Returns ok/error + a latency ms.
curl -sk -X POST -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/v1/connections/$CONNECTION_ID/test
```

If the test fails, the broker really is unreachable from the mqConnector pod. Confirm directly:

```sh
# Inside the pod / host
nc -zv broker.host 5672          # RabbitMQ
nc -zv broker.host 9092          # Kafka
openssl s_client -connect broker.host:5671 < /dev/null   # AMQPS
```

## Mitigate

1. **If TLS error**: check the broker presented a cert your CA recognises.
   - For Phase-17 mTLS: confirm `tls_ca_file`, `tls_cert_file`, `tls_key_file` paths are readable inside the pod. The dialer loads them at connect-time so a re-mount + reconnect (next failure cycle) picks up new files.
   - Temporary debug only: set `tls_insecure_skip_verify: true` via API to confirm the issue is cert validation. Never leave this on in production.
2. **If the broker is genuinely down**: nothing for mqConnector to do besides wait + retry. RabbitMQ holds messages while consumers are absent, so no data loss. Kafka commits offsets only on success, so re-consume from the last committed offset on recovery.
3. **If the broker is up but slow**: pipelines may pile up DLQ entries for send-side timeouts. Set the connection's TLS/connection-timeout (future work) or relax downstream until the broker recovers.

## Resolve

Once the broker is back:

- `POST /api/v1/connections/$CONNECTION_ID/test` returns ok.
- Live pipeline status (`/api/health`) flips back to `connected` within ~2s of the next receive attempt (Phase 15 self-heal).
- Replay any DLQ entries that backed up during the outage (see [`pipeline-errors.md`](pipeline-errors.md)).

## Postmortem

- Capture the SLO burn: `mqconnector:availability:ratio6h` for the affected pipeline.
- If TLS related: archive the cert/key fingerprints used at the time of the incident. SECURITY.md §4 has the rotation procedure.
