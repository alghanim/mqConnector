# mqConnector — Deployment Assets

Reference observability config for an operator running mqConnector against Prometheus + Grafana. Drop the files in place and tune to taste — none of this is required to run the binary, but it's the wiring an enterprise deployment typically asks for first.

## Layout

```
deploy/
├── prometheus/
│   └── mqconnector-slos.yaml          # recording + alerting rules
└── grafana/
    └── mqconnector-overview.dashboard.json
```

## Prometheus scrape config

Point Prometheus at the binary's `/api/metrics/prometheus` endpoint (TLS, with the operator's auth cookie or — preferred — a dedicated machine token; see Phase 21 roadmap). Example fragment:

```yaml
scrape_configs:
  - job_name: mqconnector
    scheme: https
    tls_config:
      ca_file: /etc/prometheus/mqc-ca.pem
    metrics_path: /api/metrics/prometheus
    static_configs:
      - targets: ['mqc-app.internal:8443']
rule_files:
  - /etc/prometheus/rules/mqconnector-slos.yaml
```

## SLOs encoded in the rules

| SLO            | Window | Target           | Alert behaviour                                    |
| -------------- | ------ | ---------------- | -------------------------------------------------- |
| Availability   | 7 days | 99.9%            | fast burn (14.4× / 2m page), slow burn (1× / 30m)  |
| Avg latency    | 10m    | < 500ms          | warning at 500ms, critical at 2s                   |
| p95 latency    | 5m     | < 1s             | warning at 1s                                      |
| p99 latency    | 5m     | < 5s             | critical at 5s                                     |
| Pipeline up    | 5m     | status=connected | critical on any pipeline reporting down            |
| Failure burst  | 1m     | < 25% fail ratio | critical when a single pipeline crosses 25%        |
| Scrape live    | 5m     | up               | critical when Prometheus can't scrape /api/metrics |
| Idle pipeline  | 15m    | ≥ 1 msg          | warning per pipeline                               |

Multi-burn alerting follows the Google SRE Workbook (Ch. 5) pattern: a fast-burn page when a brief outage is consuming the weekly budget too quickly, plus a slow-burn notification for creeping regressions.

### On-call response — alert quick reference

| Alert                                 | Likely cause                                | First moves                                                                                                              |
| ------------------------------------- | ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `mqConnectorScrapeFailed`             | Process or host is down                     | `systemctl status mqconnector`, check disk space, `journalctl -u mqconnector -n 200`                                     |
| `mqConnectorPipelineDown`             | Source/dest broker unreachable              | `GET /api/health` for per-broker status, check broker side, network policies, recent TLS rotation                        |
| `mqConnectorFailureBurst`             | Bad config push / destination outage        | `/api/v1/audit?resource=/api/v1/pipelines/<id>` for recent changes, then drain DLQ retries with backoff                  |
| `mqConnectorAvailabilityFastBurn`     | Active incident                             | Page primary on-call. Begin incident; customer-facing                                                                    |
| `mqConnectorAvailabilitySlowBurn`     | Gradual regression (flaky network, leak)    | Investigate before the budget exhausts. Look at p95/p99 trend over the last 24h                                          |
| `mqConnectorP95LatencyHigh`           | Stage cost ↑ or destination broker slow     | Inspect histogram buckets per pipeline; reduce workers if upstream is the bottleneck                                     |
| `mqConnectorP99LatencyHigh`           | Tail outliers                               | Same as p95 but more urgent — downstream timeouts are likely already firing                                              |
| `mqConnectorNoMessagesFlowing`        | Upstream empty or consumer stuck            | Verify upstream is publishing; if so, restart the pipeline via admin UI                                                  |
| `mqConnectorLatencyHigh` / `Critical` | Sustained avg latency over threshold        | Same as p95 path; this is the coarser "something's wrong" trigger                                                        |

### Backups & DR alerts

The scheduled backup worker emits an `ERROR scheduled backup failed` log line on any snapshot failure. Set a log-based alert (Loki / Splunk) keyed on that string. Combine with a Prometheus alert on `node_filesystem_avail_bytes` for `storage.backup.dir` to catch silent stalls due to disk pressure.

## Grafana dashboard

Import `grafana/mqconnector-overview.dashboard.json`. The Prometheus data source UID is parameterised as `${DS_PROMETHEUS}` — pick your data source on import.

Panels:

- Active pipelines / uptime / msg-per-second / current fail rate (status row)
- Per-pipeline throughput + failures (time series)
- Avg latency + bytes processed (time series)
- Availability SLO at 1h and 6h windows (gauges)
- Error-budget burn ratio (stat)
- **p95 + p99 latency (time series)** — derived from the `mqconnector_pipeline_latency_ms` histogram via the recording rules
- **Pipeline status** (stat, one tile per pipeline, green/red on the `mqconnector_pipeline_up` gauge)

The `pipeline_id` template variable lets the operator narrow each chart to one or more pipelines.

## Tracing

The binary emits structured logs with `trace_id` / `span_id` per request (W3C trace-context propagation; see [`internal/tracing`](../internal/tracing)). Point Loki / Splunk / Elastic at stdout and the dashboard's request-id column links across services without an OTLP pipeline.

A full OTLP exporter is a follow-up — the in-process span sink is structured to swap for `otelhttp.NewHandler` and `otel.GetTracerProvider().Tracer(...)` without changing call sites.

## What's not yet here

- Per-tenant dashboard. The tenant id appears on audit + DLQ rows but the SLO recording rules are pipeline-scoped; per-tenant SLOs need a `tenant_id` label on the metrics first.
- Helm chart. Phase 20.
- Debian package. Phase 20.
