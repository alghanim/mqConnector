# Restore from backup

Severity: incident-only. You're here because the production data is unavailable.

## Inputs you need

1. The **latest snapshot** of the SQLite DB (or pg_dump for Postgres).
2. The **master key** that was current at the time of the snapshot — otherwise stored broker passwords are unrecoverable. See [`key-rotation.md`](key-rotation.md) for the multi-version arrangement.
3. The **TLS cert/key** for the server (if not provisioned out-of-band by a sealed-secrets / cert-manager pipeline).

## Procedure

### Cold restore (whole instance)

```sh
# 1. Stop the running instance.
docker compose stop mqconnector

# 2. Move the corrupted DB out of the way.
mv /var/lib/mqconnector/mqc.db /var/lib/mqconnector/mqc.db.corrupt

# 3. Copy the snapshot in.
cp /backups/mqc.db.<timestamp> /var/lib/mqconnector/mqc.db
chmod 0640 /var/lib/mqconnector/mqc.db

# 4. Start. The schema_migrations table is part of the snapshot, so
#    migrations are no-op unless you've also upgraded the binary.
docker compose up -d mqconnector
```

### Partial restore (one tenant via config bundle)

If only one tenant's pipelines are toast and you have a recent export from `GET /api/v1/config/export`:

```sh
# Wipe the tenant's existing rows (UI: tenant page → Delete; or API).
# Then re-import:
curl -sk -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/yaml" \
  --data-binary @config-<timestamp>.yaml \
  https://localhost:8443/api/v1/config/import
```

Broker passwords come back **empty** — they were stripped at export. Update each connection's password via the UI or the connection PUT endpoint.

### Audit replay from S3

If the SQLite audit table was lost but the S3 archive (Phase 20a) survived:

```sh
# Download the daily files for the affected window.
aws s3 sync s3://my-audit-bucket/audit/2026/05/ ./audit-restore/

# Each file is JSONL; cat them into something readable.
cat audit-restore/*.jsonl | jq

# Re-import into the audit table is intentionally NOT supported —
# the hash chain would have to be re-stitched, which would break the
# tamper-evident guarantee. The archive IS the authoritative record
# for "what happened before the restore"; the on-disk table is the
# authoritative record from the restore forward.
```

## Verify

After any restore:

```sh
# 1. Health
curl -sk https://localhost:8443/api/health

# 2. Pipelines back online
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/metrics | jq '.pipelines'

# 3. Audit chain intact (the restored DB carries its hash chain
#    forward exactly as it was at snapshot time, so verify passes).
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/v1/audit/verify | jq .status
```

## Postmortem checklist

- Why did the original DB fail? (Disk, OS, container, application bug, attack?)
- How recent was the snapshot? Is the cadence adequate?
- Did the restore require operator intervention you didn't have documented? Update this runbook.
- Was the master key recoverable? If not, the company has now lost broker credentials — fix the key custody process.
