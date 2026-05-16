# mqConnector Helm chart

Install mqConnector on Kubernetes. Single Deployment + Service + ConfigMap + Secret + PVC — the binary is self-contained and ships its admin UI embedded, so the chart is short on purpose.

## Install

```sh
helm install mqc ./deploy/helm \
  --namespace mqconnector --create-namespace \
  --set image.repository=your-registry/mqconnector \
  --set image.tag=1.0.0 \
  --set secrets.masterKey=$(openssl rand -hex 32) \
  --set config.auth.simpleauthUrl=https://simpleauth.svc.cluster.local:8443 \
  --set tls.existingSecret=mqc-tls
```

## Required out-of-band setup

1. **TLS secret** — `kubectl create secret tls mqc-tls --cert=... --key=...` and pass `tls.existingSecret=mqc-tls`. The chart mounts the secret into `/etc/mqconnector/tls/` and points the config at it.
2. **SimpleAuth** — set `config.auth.simpleauthUrl` to wherever your SimpleAuth instance is reachable inside the cluster. The chart does not deploy SimpleAuth.
3. **Master key** — `secrets.masterKey` (64 hex chars) for envelope encryption of stored broker passwords. Generate once: `openssl rand -hex 32`.

## HA

`replicaCount > 1` requires `config.leadership.enabled: true`. Without the lease, multiple replicas would all consume from the source queue and double-deliver. The lease is SQLite-row-backed today; Phase 18 will swap it for Postgres `SELECT … FOR UPDATE` once the Postgres backend lands.

## Storage

By default the chart creates a `ReadWriteOnce` PVC for the SQLite DB at `/var/lib/mqconnector`. Switch to Postgres by:

```yaml
persistence:
  enabled: false
config:
  storage:
    dsn: "postgres://mqc:pass@pg.svc:5432/mqc?sslmode=require"
```

(Postgres backend is documented in `POSTGRES_MIGRATION.md`; chart values are already future-proof.)

## Prometheus

Set `prometheus.podMonitor: true` to add the pod-scrape annotations. The metrics endpoint requires authentication — mint an API token under the system tenant via `/api/v1/tokens` and pass it on the scrape job's `Authorization: Bearer …` header.

## Rolling out config changes

The Deployment template embeds a `checksum/config` annotation that changes whenever the rendered ConfigMap or Secret changes, so `helm upgrade` triggers a fresh rollout without any extra step.

## Image security

- `runAsNonRoot: true`, `readOnlyRootFilesystem: true`, all capabilities dropped.
- `terminationGracePeriodSeconds: 35` — comfortably above the binary's 30s drain budget so in-flight messages finish their send before `SIGKILL`.
- The PVC is the only writable mount; everything else is read-only or `emptyDir`.

## What's not in the chart

- **No Postgres deployment** — bring your own.
- **No SimpleAuth deployment** — bring your own (the project ships a separate Helm chart in the SimpleAuth repo).
- **No backup CronJob** — operators wire their own (a sidecar or a regular `velero` schedule both work; SQLite needs `.backup` semantics, Postgres has WAL shipping).
- **No service mesh adapters** — the binary serves plain TLS on 8443; let Istio / Linkerd inject as they would for any HTTPS pod.
