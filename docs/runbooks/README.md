# Operational Runbooks

Each runbook below covers one situation an on-call operator might face. Format is consistent: **Symptom → Verify → Mitigate → Resolve → Postmortem**.

Use these in order of how likely they are during a normal shift:

| Page | Page-worthy? | Runbook |
| --- | --- | --- |
| 1 | yes | [Pipeline returning errors](pipeline-errors.md) |
| 2 | yes | [Broker connection refused](broker-down.md) |
| 3 | sometimes | [DLQ growing rapidly](dlq-growing.md) |
| 4 | yes | [Audit log verifier reports broken chain](audit-broken-chain.md) |
| 5 | yes | [Master key rotation](key-rotation.md) |
| 6 | no  | [Upgrade mqConnector](upgrade.md) |
| 7 | no  | [Restore from backup](restore.md) |

## Common context

Everything below assumes:
- The binary is running as a systemd unit, a Docker container (compose), or a Kubernetes Pod from the chart in [`deploy/helm/`](../../deploy/helm).
- Operator access is via the admin UI at `https://<host>:8443/` (cookie session) or via a long-lived API token (`Authorization: Bearer mqct_…`).
- The Prometheus rules from [`deploy/prometheus/mqconnector-slos.yaml`](../../deploy/prometheus/mqconnector-slos.yaml) are loaded.

## Where the logs live

- **systemd**: `journalctl -u mqconnector -f`
- **Docker compose**: `docker compose logs -f mqconnector`
- **Kubernetes**: `kubectl logs -n mqconnector -l app.kubernetes.io/name=mqconnector -f`

All logs are structured JSON with `trace_id` + `span_id` (Phase 19a) so you can `jq` filter or grep by request id.

## Quick triage

```sh
# 1. Is the server reachable?
curl -sk https://localhost:8443/api/health | jq

# 2. Is the metric scrape healthy?
# (Requires a token. See `key-rotation.md` or POST /api/v1/tokens.)
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/metrics/prometheus | head -20

# 3. Recent admin actions?
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/v1/audit?per_page=20 | jq '.items[]'

# 4. Chain still intact?
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/v1/audit/verify | jq
```
