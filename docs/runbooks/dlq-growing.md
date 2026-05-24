# DLQ growing rapidly

Severity: page only if the rate is sustained (> 100 msg/min for > 5 min) or a customer-visible pipeline is involved. Otherwise notify and investigate during business hours.

## Symptom

- DLQ badge in the UI shell shows a growing count.
- `mqconnector_messages_failed_total` rate is non-zero and rising.
- Alerts: `mqConnectorAvailabilityFastBurn` or `mqConnectorAvailabilitySlowBurn`.

## Verify

The DLQ index page has a 24h/7d filter. Group by pipeline + error reason:

```sh
# Top failure reasons in the last hour
curl -sk -H "Authorization: Bearer $TOKEN" \
  "https://localhost:8443/api/v1/dlq?per_page=500&since=$(date -u -v-1H +%Y-%m-%dT%H:%M:%SZ)" | \
  jq -r '.items[] | .pipeline_id + " | " + .error_reason' | \
  sort | uniq -c | sort -rn | head -10
```

Decide the class:

| Pattern | Likely cause | Runbook |
| --- | --- | --- |
| `validate:` | schema drift | [`pipeline-errors.md`](pipeline-errors.md) |
| `script:` resource limit | runaway script | [`pipeline-errors.md`](pipeline-errors.md) |
| `send:` connection refused | broker down | [`broker-down.md`](broker-down.md) |
| `translate:` decode failed | malformed source | check producer |
| `transform:` path missing | missing field | check producer |

## Mitigate

If the rate is high and the cause is a single pipeline, **disable it** as in `pipeline-errors.md` step 1. The DLQ keeps the failed messages — no data is lost — but the bleed stops.

If the failure mode is "the source itself is producing bad messages" and you don't want to disable the bridge, **add a filter stage** to drop the offending shape before the rest of the chain sees it:

```json
{
  "stage_type": "filter",
  "stage_config": "{\"paths\": [\"$.bad_field\"]}"
}
```

## Resolve

After fixing the upstream:

1. Replay the DLQ rows that *would have succeeded* under the new rules. The UI has bulk-select on `/dlq`.

   ⚠ **`max retries exceeded` on replay:** if the outage lasted longer than the DLQ reaper's retry budget (`pipelines.retry_max`, default 3), the rows are at the cap and `POST /api/v1/dlq/{id}/retry` returns `400`. Two options:

   - **Raise the retry budget first**, then replay:
     ```sh
     # bump retry_max to 10 for the affected pipeline
     curl -sk -b "$JAR" -H "X-CSRF-Token: $CSRF" \
       -X PUT "https://$HOST/api/v1/pipelines/$PIPELINE_ID" \
       -d '{...existing fields..., "retry_max": 10}'
     # then bulk-replay via the UI
     ```
   - **Republish from the original payload** (the DLQ row carries `original_msg` base64-encoded). Decode and publish back onto the source queue, then delete the DLQ row. Use this when the destination semantics are idempotent — the original producer is the source of truth and the DLQ row is just a paper trail.

2. Delete rows that can never succeed (e.g. truly malformed payloads that the producer has stopped emitting). The audit trail keeps the deletion record.

## Postmortem

- DLQ records carry `created_at` so an SLO miss can be back-calculated precisely.
- The Phase 17a hash-chain on the audit log means any deletion is non-repudiable. The chain verifier (`/api/v1/audit/verify`) should still return `ok` after cleanup.
