# Upgrade mqConnector

Severity: planned. Schedule outside peak hours when possible.

## Pre-flight

1. **Snapshot the database**:
   ```sh
   sqlite3 /var/lib/mqconnector/mqc.db ".backup '/var/lib/mqconnector/pre-upgrade-$(date +%s).db'"
   ```
   For Postgres deploys: `pg_dump` to a versioned file.
2. **Export your config bundle** as a recoverable plaintext (Phase 21c):
   ```sh
   curl -sk -H "Authorization: Bearer $TOKEN" \
     https://localhost:8443/api/v1/config/export > config-$(date +%s).yaml
   ```
3. **Confirm the audit chain is intact**:
   ```sh
   curl -sk -H "Authorization: Bearer $TOKEN" \
     https://localhost:8443/api/v1/audit/verify | jq .status
   # should be "ok"
   ```
4. **Read the release notes** for the version you're moving to. Migrations are automatic, but a change in `migrations.go` is irreversible without the snapshot.

## Docker compose

```sh
docker compose pull mqconnector
docker compose up -d --no-deps mqconnector
```

The 30s drain budget (Phase 18a) gives the old container time to finish in-flight messages before SIGKILL.

## Kubernetes (Helm)

```sh
helm upgrade mqc deploy/helm \
  --namespace mqconnector \
  --set image.tag=<new-version> \
  --reuse-values
```

The chart's `Recreate` strategy keeps SQLite single-writer. Pod terminationGracePeriodSeconds = 35 (chart default), comfortably above the drain budget.

## Verify after upgrade

```sh
# 1. Health responds
curl -sk https://localhost:8443/api/health | jq

# 2. Schema migrations applied
sqlite3 /var/lib/mqconnector/mqc.db \
  "SELECT MAX(version) FROM schema_migrations"

# 3. Audit chain still ok (catches any subtle row corruption)
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/v1/audit/verify | jq .status

# 4. A representative pipeline is processing
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/metrics | \
  jq '.pipelines | to_entries[0].value.messages_processed'
```

## Rollback

```sh
# Stop the new version
docker compose stop mqconnector

# Restore the pre-upgrade snapshot
mv /var/lib/mqconnector/mqc.db /var/lib/mqconnector/mqc.db.failed
cp /var/lib/mqconnector/pre-upgrade-<ts>.db /var/lib/mqconnector/mqc.db

# Pin to the old image
docker compose up -d --no-deps mqconnector  # with old tag
```

Rollback may LOSE rows committed between the snapshot and now. If you can't afford that, prefer fixing forward.
