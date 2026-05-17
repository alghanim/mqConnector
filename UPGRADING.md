# Upgrading mqConnector

Concrete steps for moving between versions. See `CHANGELOG.md` for the full per-version diff; this doc covers the operator actions required to land an upgrade safely.

## General procedure

The same five steps apply to every upgrade. Per-version notes below add extras when needed.

```sh
# 1. Snapshot. Always.
mqconnector backup --to=/var/backups/mqc-pre-upgrade-$(date -u +%Y%m%dT%H%M%SZ).db

# 2. Read CHANGELOG.md for the target version. Note any "Changed",
#    "Removed", or "Deprecated" entries between current and target.

# 3. Stop the running binary. Pipelines drain in-flight messages —
#    every receive either commits cleanly or lands in DLQ.
sudo systemctl stop mqconnector

# 4. Swap the binary, verify version.
sudo install -m 0755 /tmp/mqconnector /usr/local/bin/mqconnector
mqconnector -version

# 5. Start, watch /api/health for a clean boot.
sudo systemctl start mqconnector
sleep 5
curl -sk --cacert /etc/mqconnector/ca.pem \
     -b "session=<cookie>" \
     https://localhost:8443/api/health | jq .
```

Migrations are idempotent and apply in version order. A binary that's two minors ahead of the database walks every intervening migration on first boot. Rolling back is **NOT** automatic — restore the pre-upgrade snapshot if needed.

## Rollback procedure

```sh
sudo systemctl stop mqconnector
sudo install -m 0755 /usr/local/bin/mqconnector.previous /usr/local/bin/mqconnector
# Restore the pre-upgrade SQLite snapshot
sudo cp /var/backups/mqc-pre-upgrade-XXX.db /var/lib/mqconnector/mqconnector.db
sudo chown mqconnector:mqconnector /var/lib/mqconnector/mqconnector.db
sudo systemctl start mqconnector
```

If you've intentionally taken writes on the new version that you want to keep, rollback isn't safe — the new-schema rows will fail to load on the older binary. In that case, file a support ticket; we can hand-port specific tables if the diff is small.

## Per-version notes

### Upgrading to the next minor (current Unreleased section in CHANGELOG.md)

This release applies migrations 0013 (`tenant_memberships.system_admin`) and 0014 (`pipelines.max_msgs_per_minute`). Both are additive ALTER TABLEs with no destructive effect on existing data.

After upgrade, three behavioural changes can surface:

- **Cookie SameSite tightened to `Strict`.** A user who navigates to mqConnector from an external link in the same tab will be treated as unauthenticated; they re-login and proceed. No action needed; brief user-visible blip.
- **Container image is now distroless static.** If you have a custom `docker run` recipe that overrides `--user` or `--entrypoint`, audit it — the user is now `nonroot` (UID 65532), no shell exists in the image.
- **Audit cookie cascade.** New routes `/api/v1/admin/backup`, `/api/v1/admin/integrity`, `/api/v1/admin/pprof/*` exist. They're gated to system admins; existing default-tenant owners are auto-elevated by migration 0013. Verify with `curl -b "session=<cookie>" .../api/v1/admin/integrity` — should return `{"ok":true,...}`.

Recommended config additions (none mandatory — defaults are off):

```yaml
# Scheduled backup worker. Off by default.
storage:
  backup:
    dir: /var/backups/mqconnector
    interval: 24h
    keep: 7

# Session inactivity auto-logout. Off by default (legacy behaviour).
auth:
  idle_timeout: 30m

# Real-time SIEM forwarding for audit rows.
audit:
  syslog_url: tcp://siem.internal:514

# S3 audit archival (independent of syslog; both can run).
audit:
  s3:
    endpoint: https://s3.amazonaws.com
    region: us-east-1
    bucket: mqconnector-audit
    access_key: ${MQC_AUDIT_S3_KEY}
    secret_key: ${MQC_AUDIT_S3_SECRET}
```

### Upgrading to 1.0.0 (initial release)

No prior version. Fresh install.

## Helm-managed deployments

`helm upgrade mqconnector ./deploy/helm -f values.yaml` runs the same migration steps on first pod start. The chart's `Deployment` uses `Recreate` strategy when `persistence.enabled` is true — a new pod doesn't start until the old one stops, so two binaries don't fight over the SQLite file.

For multi-replica deploys (`replicaCount > 1`, `leadership.enabled: true`), the migration runs on the elected leader. Followers wait for the leader's first `/api/health` 200 before they accept traffic.

## Multi-replica zero-downtime

Strict zero-downtime requires the leadership lease:

```yaml
replicaCount: 2
config:
  leadership:
    enabled: true
    ttl: 30s
```

Upgrade procedure:

1. Snapshot.
2. `kubectl rollout restart deployment/mqconnector` — Kubernetes terminates the follower first (leader has the lease), the follower comes up on the new binary and runs schema migrations, then signs in as standby.
3. Force a leadership transition by deleting the leader pod: `kubectl delete pod -l app.kubernetes.io/instance=mqconnector,role=leader`.
4. The previously-standby (new binary) picks up the lease, the just-deleted pod restarts on the new binary as the new standby.

Total visible API downtime: the `~ttl/2` lease-handoff window, typically a few seconds.

## Verifying the upgrade

After every upgrade:

```sh
# Schema migrations all applied.
sqlite3 -readonly /var/lib/mqconnector/mqconnector.db \
  "SELECT max(version) FROM schema_migrations"

# Database integrity.
curl -sk --cacert ca.pem -b "session=<cookie>" \
     https://mqc.svc:8443/api/v1/admin/integrity | jq .

# Pipeline metrics still flowing.
curl -sk --cacert ca.pem -b "session=<cookie>" \
     https://mqc.svc:8443/api/metrics/prometheus | grep mqconnector_messages_processed_total
```

If any of these don't behave: restore the pre-upgrade snapshot, downgrade the binary, file a ticket.
