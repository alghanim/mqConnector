# Audit chain verifier reports broken

Severity: page. A broken audit chain is a compliance incident and may indicate active tampering.

## Symptom

```sh
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/v1/audit/verify | jq

# Healthy:
# { "status": "ok", "tenants": [ { ..., "status": "ok", "checked": 1059 } ] }

# Broken:
# { "status": "broken",
#   "tenants": [{
#     "tenant_id": "...",
#     "status": "broken",
#     "checked": 1059,
#     "first_broken_id": "abc-123",
#     "first_broken_at": "2026-05-16T10:43:21Z",
#     "first_broken_why": "row hash does not match canonical recomputation"
#   }] }
```

The chain protects against single-row mutations. A break means **someone modified the row directly in the database**, bypassing the API. (A power-failure crash mid-insert leaves a *missing* row, not a divergent hash — Phase 17a's chain mutex prevents that.)

## Verify

1. Fetch the broken row and the row immediately before it:
   ```sh
   curl -sk -H "Authorization: Bearer $TOKEN" \
     "https://localhost:8443/api/v1/audit?per_page=500&since=2026-05-16T10:43:00Z&until=2026-05-16T10:43:22Z" | \
     jq '.items[]'
   ```
2. Compare expected vs stored hash for the broken row. The verifier reports `why` — either:
   - `row hash does not match canonical recomputation`: someone changed a column in this row.
   - `row prev_hash does not match prior row's hash`: someone removed or replaced an earlier row.

## Mitigate

1. **Suspend mutation traffic**: revoke any API tokens that may be implicated, disable any leaders if HA, isolate the affected instance.
2. **Snapshot the database**:
   ```sh
   sqlite3 /var/lib/mqconnector/mqc.db ".backup '/var/lib/mqconnector/forensic-$(date +%s).db'"
   ```
   For Postgres deploys (future Phase 18): `pg_dump` the audit + audit_log_diffs tables.
3. **Check the S3 archive** (if configured per [`deploy/README.md`](../../deploy/README.md)): rows older than `audit.max_age` are uploaded as immutable JSONL. Compare the broken row's content against the archived copy.

## Resolve

There is no "fix the chain" path — the chain is supposed to refuse to lie. Options:

- **If the divergence is small and known-good**: leave the chain broken and update internal evidence to reflect what *should* have been there. The verifier will continue to report broken from that row forward; new rows are still chain-verifiable from the next row onward (the verifier resets `expectedPrev` when it sees a row with empty hash, but a broken hash leaves the next row stamped with a real prev_hash — `Verify` reports the FIRST broken row only).
- **If the divergence is the result of an attacker**: treat as a full security incident. The S3 archive is the source of truth for "what really happened before the tampering"; the on-disk table is suspect from `first_broken_at` forward.

## Postmortem

- Identify the access path: who had direct DB access? (Container exec, file-system access, backup restore overlapping a live run?)
- Tighten controls: read-only DB mount where possible, restrict shell access, route every audit-affecting change through the API.
- The hash-chain is *evidence*, not protection — it only tells you something happened. Combine with file-integrity monitoring (AIDE, OSSEC) on the DB file to detect tampering at write time.
