# Pipeline returning errors

Severity: usually page-worthy when a customer-visible pipeline is affected.

## Symptom

- `mqConnectorAvailabilityFastBurn` alert firing.
- One or more pipelines show **danger** status on the dashboard.
- DLQ is growing for a specific pipeline.
- Logs contain `executor exited with error` or `stage failed, sending to DLQ`.

## Verify

```sh
# Which pipeline(s) are bad?
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/metrics | \
  jq '.pipelines | to_entries[] | select(.value.last_error != "") | {id: .key, err: .value.last_error}'

# Recent failures by reason
curl -sk -H "Authorization: Bearer $TOKEN" \
  "https://localhost:8443/api/v1/dlq?per_page=20" | \
  jq '.items[] | {pipeline_id, error_reason, created_at}' | head -50
```

In the UI: `/dlq` page, filter by `pipeline_id`, sort by recency.

## Mitigate (in order)

1. **Disable the bad pipeline** to stop the bleed:
   ```sh
   curl -sk -X PUT -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"enabled": false}' \
     https://localhost:8443/api/v1/pipelines/$PIPELINE_ID
   ```
   Confirms with a 200; the manager hot-reloads.
2. **Pause upstream producers** if you control them. The DLQ keeps growing until they pause OR the source connector reconnects (RabbitMQ holds messages while consumers are absent).
3. **If a stage is the culprit**: most-common failures are validate (schema mismatch) and script (bad expression). Toggle the stage off and redeploy — `PUT /api/v1/pipelines/{id}/stages` with the stage `enabled: false` in the list.

## Resolve

- **Schema drift**: the message shape changed upstream. Update the `validate` stage's schema, or relax the affected fields, then `enabled: true` again.
- **Broker disconnected**: see [`broker-down.md`](broker-down.md). The pipeline self-heals once the connector reconnects (Phase 15).
- **Bad transform / script**: open `/flow?pipeline=$PIPELINE_ID` in the UI, click the offending node, fix the config, **Save & Deploy**.

## Replay DLQ

After the pipeline is healthy:

```sh
# Replay one message
curl -sk -X POST -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/v1/dlq/$DLQ_ID/retry

# Replay a batch via the UI: /dlq, select rows, "Retry all"
```

DLQ entries that re-fail max-retries times stay as evidence.

## Postmortem

1. Capture the audit trail of changes leading up to the incident:
   ```sh
   curl -sk -H "Authorization: Bearer $TOKEN" \
     "https://localhost:8443/api/v1/audit?since=$(date -u -v-6H +%Y-%m-%dT%H:%M:%SZ)"
   ```
   For each PUT row, the diff is at `/api/v1/audit/{id}/diff`.
2. Verify the audit chain is intact — see [`audit-broken-chain.md`](audit-broken-chain.md).
3. Add a SLO regression to `deploy/prometheus/mqconnector-slos.yaml` if the failure mode wasn't covered.
