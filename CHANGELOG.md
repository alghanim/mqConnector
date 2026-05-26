# Changelog

All notable changes to mqConnector are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows [SemVer](https://semver.org/spec/v2.0.0.html).

## Versioning policy

- **MAJOR** version bumps break HTTP API contracts, storage schemas without an in-binary migration path, or default behaviour in a way that requires an operator change to keep working.
- **MINOR** bumps add features, add (but don't change) HTTP routes, add (but don't break) config fields, or ship storage migrations the binary runs automatically.
- **PATCH** bumps are bug fixes and security fixes with no surface-level change.

## Deprecation policy

A deprecation has three phases:

1. **Announced** — the next release ships with the replacement available alongside the old surface. The old surface emits a structured `WARN` log line containing `event=deprecated`, a brief description, and the targeted removal version. The CHANGELOG entry calls it out under `Deprecated`.
2. **Default-off** — at least one minor release later, the deprecated surface stays callable but is disabled by default. Operators opt in via a config flag (`compat.<feature>: true`) to keep it working. The CHANGELOG entry calls it out under `Changed`.
3. **Removed** — at least one minor release after default-off, the surface is removed. The CHANGELOG entry calls it out under `Removed`. Operators upgrading directly from a pre-announcement release are pointed to the announcement entry.

No surface goes from Announced → Removed in less than two minor releases, so any deployment that picks up a release within a 6-month window has time to react.

---

## [Unreleased]

This section accumulates changes between tagged releases. Move entries into a new version section on release.

---

## 1.6.0 — DLQ Intelligence Console + AI workstream foundation — 2026-05-26

### Added — Operator experience

- **`/dlq` rebuilt as the Intelligence Console.** Three-pane layout: filter rail + cluster list on the left, cluster detail in the center, action drawer on the right. Tab strip toggles between the new **Clusters** view (default) and the legacy **All entries** flat list. Empty states explicitly tell the operator nothing is on fire when there are no clusters and the queue is clean.
- **Fuzzy clustering of DLQ failures.** Errors collapse to stable fingerprints regardless of variable parts (UUIDs / timestamps / IPs / IDs / paths / email / numbers / field references all replaced with placeholders). A typical failure cluster shows the canonical template (e.g. `"[stage:validate] validation: missing field <field>"`) with a count, first/last seen, affected pipelines, and the failing stages — collapsing dozens of nearly-identical errors into one triage row.
- **Replay simulation against the live revision.** From the action drawer, click "Replay simulation" on any entry → re-runs the original payload through the pipeline's currently-deployed revision in preview mode (no broker sends). Returns per-stage outcomes, a retry-confidence pill (`would-succeed` / `would-fail at <stage>`), and an "explain why this failed" hint backed by the structured run trace.
- **Side-by-side payload diff.** Pick "Compare to..." on any entry to diff its body against another entry in the same cluster. The page renders a hand-rolled LCS line diff with `--success-bg` / `--danger-bg` highlights — useful for telling apart "same failure" vs "payload shape drifted."
- **AI cluster names (opt-in, off by default).** A switch in the page header turns on `?ai=names` on the clusters endpoint. When the operator's self-hosted LLM endpoint is configured + the `dlq_cluster_naming` capability is allowlisted, each cluster gets an `ai_name: {title, summary, suggestion}` rendered via a small "AI" badge. The deterministic template stays as the fallback when AI is off or the provider is unreachable. A 60-second in-memory cache keeps repeat renders from re-costing the LLM.
- **`?pipeline=<id>` deep links** preserved from Wave 1.4.0 polish — the Metrics drilldown's "View DLQ" button still lands on a pre-filtered console. New `?tab=all` switches straight to the legacy view for operators with a fixed workflow.

### Added — Backend primitives

- **DLQ schema extension (migration 0023).** Four new columns on the `dlq` table — `error_fingerprint`, `error_template`, `failing_stage_name`, `failing_stage_index` — plus two indexes (`(tenant_id, error_fingerprint)` + `(tenant_id, failing_stage_name)`). Existing rows carry empty fingerprints; new rows are populated by the push path.
- **`internal/dlq/cluster` package.** Hand-rolled SimHash fingerprinter over a tokenised-template projection of the error string. Nine ordered substitutions (UUID → `<uuid>`, ISO timestamps → `<time>`, ≥3-digit integers → `<int>`, email → `<email>`, IPs → `<host>`, file paths → `<path>`, JSON-pointer field refs → `<field>`, quoted strings → `<str>`, whitespace collapse). `Fingerprint(errStr)` + `FingerprintWithStage(errStr, stage)` — the stage variant scopes the cluster so identical errors at different stages don't collapse together.
- **`internal/ai/` workstream foundation.** `LLMProvider` interface + `OpenAIProvider` (pure `net/http` + `encoding/json`; chat-completions endpoint with optional `response_format.json_schema` structured outputs) + `NoopProvider` sentinel for off-by-default deployments + `FakeProvider` for tests. Per-call `AuditLogger` writes to a new `ai_audit` table (migration 0024) keyed by feature/tenant/prompt-hash. `mqconnector_ai_calls_total{feature, model, outcome}` Prometheus counter exposes provider health.
- **`internal/dlq/cluster/naming.go`.** `Name(ctx, provider, audit, cfg, NameRequest)` → `NameResult{Title, Summary, Suggestion}`. System prompt + JSON schema are Go constants (versioned with the code). Best-effort: returns `ErrAINotAvailable` on every failure mode (feature disabled, provider error, bad JSON, timeout); caller decides whether to fall back to the deterministic template.
- **3 new HTTP endpoints**:
  - `GET /api/v1/dlq/clusters` — fingerprint-aggregated triage view with filters (`pipeline_id`, `since`, `limit`, `min_count`) and an opt-in `?ai=names` enrichment query
  - `POST /api/v1/dlq/{id}/replay-sim` — preview replay against the live revision; returns per-stage outcomes + would-succeed flag
  - `GET /api/v1/dlq/{id}/diff?against={other}` — server-computed LCS line diff between two entries

### Added — Frontend primitives

- **`web/src/lib/components/charts/Heatmap.svelte`** — calendar-heatmap of cluster occurrences (7 days × 24 hours). Hand-rolled SVG; 5-quintile fill scale on brand tokens; `<title>` per cell for hover tooltips.
- **`web/src/lib/components/charts/PayloadDiff.svelte`** — side-by-side diff renderer consuming the server-computed `[{op, text}]` operations array. Reusable: the Studio dry-run dock's payload-diff modal can move to this component in a follow-up.
- **`web/src/lib/components/dlq/`** — `ClusterCard`, `ClusterDetail`, `ActionDrawer`, `AINameBadge`, `StageOutcomeStrip` — the composable pieces of the new console. Each is small, focused, brand-token-clean, and individually testable.

### Migrations

- **Migration 0023** — `dlq` fingerprint + template + failing-stage columns + 2 indexes.
- **Migration 0024** — `ai_audit` table + 3 indexes.

### Configuration

A new `ai:` section in `config.example.yaml` (commented out, `enabled: false` by default). Operators wire up their self-hosted LLM (OpenAI-compatible — vLLM, llama.cpp server, TGI, Ollama, etc.) by setting `endpoint`, `model`, optional `auth_header`, and the explicit `features` allowlist. Capabilities declared but not yet consumed: `transformation_from_example`, `explain_why_summary`, `redaction_pattern_detect` (Wave 4+).

### Deferred to Wave 4

- Real per-cluster heatmap data — currently derived from cluster `first_seen` + `last_seen` + `count` via a rough binning. Wave 4 will add a proper per-cluster recent-activity SQL.
- Explainer-engine consumers for `explain_why_summary` capability (already declared in `internal/ai/config.go`).

### No breaking changes

Existing `/api/v1/dlq` list + `GET /api/v1/dlq/{id}` + retry + delete + bulk endpoints unchanged. The flat-list UI survives as the "All entries" tab.

---

## 1.5.0 — Live Topology + UX consistency sweep — 2026-05-25

### Added — Operator experience

- **`/topology` — Live Topology & Flow Command Center.** A new top-level operator surface: hand-rolled force-directed SVG graph of every broker + every pipeline running in the tenant. Edges are coloured + stroke-weighted by circuit state and `msg/min`, with marching-ants animation on open circuits (motion-safe). Side detail panel resolves selection to a connection or pipeline card with quick-action links into Studio, the metrics drilldown, and the DLQ filter. Polls every 5s with visibility-aware pause and a stale-data indicator. Sidebar entry `Topology` + CommandPalette nav entry.
- **Studio elevated** with per-stage type-coloured accents on the palette, hairline brand-gradient strip on the header, broker glyphs on Source/Destination canvas nodes, marching-ants edge flow when dry-running, informative Pipeline Overview empty state in the Inspector (composition counts + last deployed + throughput + quick-add buttons), compact DryRunDock sample chips with a Last-run status line, and a friendlier VersionRail empty state.
- **Overview dashboard fixed**: pipeline cards now resolve `pipeline_id` UUIDs to names, render broker glyphs on source/destination, and carry a soft shadow + hover lift. Throughput chart got a friendlier empty state.
- **Metrics page rebuilt**: status dots, name-as-link, broker glyphs in the Flow column, larger status-tinted sparkline, and per-pipeline drilldown panel (inline expand) with throughput chart, latency pills, and three quick-action buttons (Open in Studio, View DLQ, Edit).
- **Connections list polished**: name is a clickable button that opens the Edit dialog, broker glyphs bumped to 16px, "Test" became a labelled pill, endpoint cells truncate with full-value tooltip.
- **DLQ page polished**: pipeline names resolved (no more UUIDs in the column or the detail drawer), filter dropdown labels match, relative-time + retry-count badge per row, friendlier empty-state copy, and `?pipeline={id}` query string pre-applies the filter (the Metrics drilldown's "View DLQ" button lands pre-filtered).
- **Pipelines list**: name is now a clickable link directly to Studio; a labelled `Studio` pill replaces the prior icon-only gear button.
- **Top-of-app nav** moved from a left sidebar to a horizontal top bar to reclaim full content width (clipped at viewport so the page can never horizontal-scroll).

### Added — Backend primitives

- **`GET /api/v1/topology`** — single aggregator returning connections (with type, topic, depth, connected flag) + pipelines (with status, msg/min, processed, failed, avg latency, DLQ depth, circuit state, shadow + routing destinations) in one snapshot. Best-effort: a failing sub-source (DLQ count, routing rules) logs `slog.Warn` and zero-fills rather than 500'ing.
- **`Pool.Has(key)` + `Pool.Keys()`** — read-only accessors on the MQ pool so the topology aggregator can determine `connected` without opening a new connection.
- **`DLQRepo.CountByPipeline(ctx, tenantID)`** — single grouped query (`SELECT pipeline_id, COUNT(*) GROUP BY pipeline_id`) for the topology DLQ depth column.
- **`Manager.CircuitStateForPipeline(pipelineID)`** — exposes the outbound circuit-breaker state (`closed | open | half-open | unknown`) per pipeline. Wired via a `CircuitStatePublisher` hook the manager sets on each executor; state is cleared in the executor's unwind defer so stopped pipelines report `unknown` instead of stale.

### Added — Frontend primitives

- **`web/src/lib/charts/force.ts`** — ~200-line hand-rolled force-directed layout (repulsion + spring + centering, Euler integration, settles in 30 iterations + ~2s of per-frame refinement, honors `prefers-reduced-motion`).
- **`web/src/lib/components/charts/TopologyGraph.svelte`** — SVG graph with node drag, edge selection, marker-arrow definitions, dotted/dashed/solid/marching-ants edge styling per circuit state, and a `ResizeObserver` for parent-column resize.
- **`web/src/lib/stores/catalogue.ts`** — lifted name + connection resolution helpers from the Dashboard + Metrics duplication into a shared module; DLQ + Topology side panel now reuse the same `pipelineLabel` / `endpointFor` / `endpointType` / `endpointName` lookups.
- **`ConnectionTypeIcon.svelte`** — shared broker glyph component (rabbitmq / kafka / ibm / mqtt / nats / amqp1) used by Studio, Dashboard, Metrics, Connections, DLQ filter, Topology, and the side detail panel.

### Migrations

None. All new functionality is read-only over existing schemas.

### Deferred to Wave 3

- SSE delta channel for circuit-state changes — the `/topology` page polls every 5s instead. Add when topology page performance dictates.
- Standalone `EdgePulse` / `LiveSankey` chart primitives — the equivalent edge-pulse rendering is inlined into `TopologyGraph` for now; extract into reusable primitives when the dashboard flow strip lands.
- Overview dashboard flow strip + alert ribbon — defer until SSE delta channel exists so the strip doesn't double-poll.

### No breaking changes

All Wave 1.x routes and contracts continue to work. Two surfaces evolved (Pipelines list link target, Pipeline detail redirect to Studio) but both remain reachable; legacy form view is still gated behind `?legacy=1`.

---

## 1.4.0 — Pipeline Studio (Wave 1) — 2026-05-25

### Added — Operator experience

- **Pipeline Studio** — a new visual pipeline workflow at `/pipelines/{id}/studio`. Replaces forms-first config with a three-pane shell: drag-drop stage palette, SVG canvas (source → stages → destination + routing branches), structured per-stage editors (Filter / Transform / Translate / Route / Script / Validate / WASM) replacing raw-JSON textareas.
- **Live dry-run dock** — paste a sample message and inspect per-stage outcomes (timing, body, format, error) with click-to-diff between adjacent stages. Hand-rolled LCS diff renders side-by-side with line-level highlights.
- **Version rail + diff viewer + deploy ceremony** — every save snapshots a `pipeline_revisions` row; explicit "Deploy" dialog shows the diff vs live, captures a change summary, and enforces an optional approver gate. "Rollback to revision N" creates a new revision from the target snapshot and write-throughs transactionally.
- **CommandPalette Studio entries** — `Deploy`, `Compare to live`, `Discard draft`.
- **Legacy form view** preserved at `/pipelines/{id}?legacy=1` (one-release safety net); `/pipelines/{id}` now 307-redirects to `/studio`.

### Added — Backend primitives

- **`pipeline_revisions` table** — append-only snapshots (pipeline + stages + transforms + routing rules). Canonical-JSON hash dedup (excludes child IDs and timestamps from the hash projection so identical PUTs collapse). Per-pipeline mutex makes revision-number assignment race-free.
- **6 new HTTP endpoints**:
  - `GET /api/v1/pipelines/{id}/revisions` (paginated list)
  - `GET /api/v1/pipelines/{id}/revisions/current` (latest deployed)
  - `GET /api/v1/pipelines/{id}/revisions/{rev}` (single)
  - `GET /api/v1/pipelines/{id}/revisions/{rev}/diff?against={other}` (structured diff with positional child matching)
  - `POST /api/v1/pipelines/{id}/revisions/{rev}/rollback` (creates new revision from target, transactional write-through, Manager.Reload)
  - `POST /api/v1/pipelines/{id}/deploy` (promotes existing revision; latent `requires_approval` gate returns 409)
- **`StageRun` extension** — preview engine now captures per-stage `body`, `format`, `err`; surfaced as `stage_runs[]` on `POST /v1/preview`. Body is `bytes.Clone`d so the dry-run dock can render without read corruption.
- **Tx-aware repo variants** — `ReplaceForPipelineTx` on stages/transforms/routing rules and `UpdateTx` on pipelines, sharing one `storage.Tx` for atomic deploy/rollback writes.
- **`Pipeline.RequiresApproval` field** — surfaced on the Go model; column already existed from migration 0022.
- **`CreateForce`** — bypasses hash dedup for operator-intent inserts (rollback).
- **Snapshot helper** — async via background goroutine with 5s timeout (replaces the synchronous request-context path), tracked by a `pendingBackgroundOps` WaitGroup so tests can wait deterministically and the shutdown drain blocks for in-flight writes (bounded 5s).

### Added — Hygiene

- **`scripts/check-no-hex.sh`** — brand-token discipline CI lint. Comment-aware, perl-stripping, with an `# check-no-hex: ignore` escape hatch for intentional exceptions (e.g. `<meta name="theme-color">`).

### Deferred to Wave 2

- Proper draft-vs-deploy separation. Today the Studio's Deploy button re-deploys the latest existing revision; legacy PUTs still auto-deploy via the snapshot helper.
- Tenant-saved samples library in DryRunDock (Wave 1 ships fixtures + paste only).
- Recent-traffic sample picker tab (Wave 1 omits; requires a Manager ring buffer).
- PathPicker integration in the wrapped TransformEditor / RouteEditor row UIs (wrappers preserved; row UIs untouched).
- Approval-required-to-deploy UI (column exists; toggle to flip `requires_approval` is not yet exposed).
- Deploy button auto-disable when any per-stage editor reports `valid=false` (today: visual `!` indicator in the Inspector, not button-gated; server still rejects).

### Migrations

- Migration 0022 — new `pipeline_revisions` table + `pipelines.requires_approval` column.

### No breaking changes

Existing PUT handlers continue to work; their semantics (save = deploy) are preserved. Legacy `/pipelines/{id}` URL still serves the form view when `?legacy=1` is appended.

---

## [1.3.0] — 2026-05-24

Operability + compliance release. Ten additive features layered on the 1.2.0 enterprise-hardening base: payload-level PII protection on the DLQ, a destination-side dedup window for non-idempotent downstreams, pluggable key custody (Vault), schema-drift detection earlier than DLQ-depth alarms, per-stage latency observation without a trace backend, CEF for SIEM integrations, tenant-aggregate fairness, broker-side mTLS hot reload, bulk DLQ triage, and shadow-destination canarying for broker migrations. No breaking changes — every new field is default-off and preserves prior behaviour.

### Added

- **DLQ payload redaction with sealed raw** (migration 0019): per-pipeline jsonpath/regex rules applied at DLQ Push time. The redacted form lives in `original_msg`; the pre-redaction bytes are envelope-encrypted into `raw_msg` under the existing `MQC_MASTER_KEY`. New admin-gated endpoint `GET /api/v1/dlq/{id}/raw` decrypts on demand and emits a dedicated `action=dlq_raw_view` audit row for every read. `PUT /api/v1/pipelines/{id}/dlq-redaction-rules` (admin) validates patterns at edit time and refuses with 412 when no master key is configured — redacting without a sealed raw would silently destroy the original. Retry uses the raw payload so redacted bytes never reach the destination.
- **Destination-side dedup window** (migration 0020): per-pipeline `dedup_window_seconds` (default 0 = disabled). When set, the executor SHA-256s the post-stage payload and skips outbound sends for byte-identical payloads observed within the window. The source is still committed (operator opted into "treat as same message"), upgrading the global at-least-once contract to effectively-once for the configured window. New `mqconnector_dedup_skipped_total` counter. 15-min prune sweeper.
- **Pluggable secrets provider**: new `secrets.source = env | file | vault` config block. `EnvProvider` wraps the historical `MQC_MASTER_KEY`/`MQC_MASTER_KEYS` path unchanged. `FileProvider` reads a key file (for Vault Agent sidecar, k8s Secret mount, sealed-secrets, SOPS-decrypted artifacts). `VaultProvider` fetches a HashiCorp Vault KV v2 secret over a minimal HTTP client (no hashicorp/vault dep added) — each `v{N}` field becomes one envelope key version; rotation = `vault kv put` + `POST /api/v1/secrets/rotate`.
- **Schema drift alarm**: new `mqconnector_validate_attempts_total` / `_failures_total` counters wired off a new per-stage `StageRun` observation log returned by `RunStages`. Two new alert rules in `deploy/prometheus/mqconnector-slos.yaml`: `mqConnectorSchemaDriftSuspected` (>20% validate failure rate, 3 min, attempts > 0.1/s) and `mqConnectorSchemaDriftSevere` (>80%, 1 min). Catches producer-side schema changes at the inflection point, before DLQ-depth alarms fire on the downstream effect.
- **Per-stage latency histogram**: `mqconnector_stage_duration_ms_{bucket,sum,count}` labelled by `(pipeline_id, source, dest, stage)`. Lets air-gapped operators answer "which stage is slow?" from the embedded Grafana dashboard without a Tempo/Jaeger backend. Same observation hook the drift alarm uses; zero hot-path cost beyond a `time.Since` call.
- **CEF audit-log wire format**: new `audit.syslog_format = rfc5424 | cef` option on the existing syslog forwarder. CEF (ArcSight Common Event Format) lets the audit feed plug into ArcSight ESM / QRadar DSM pipelines that key on the CEF schema. Pipes and equals signs in extension values are escaped per spec so an injected actor name can't smuggle k/v pairs into the parsed event.
- **Tenant-aggregate fairness budget**: activates `tenant.MaxMsgsPerMinute` for the pipeline path (until 1.3.0 it was wired only to the HTTP rate limiter). The manager caches one shared `*budget` per `tenant_id` and hands it to every executor that serves a pipeline in that tenant; the executor takes the tenant slot BEFORE the per-pipeline slot. Closes the prior fairness gap where a runaway pipeline under tenant T could monopolise broker bandwidth allocated to T, starving sibling pipelines.
- **Broker mTLS hot reload**: `BuildTLSConfig` now wires `GetClientCertificate` to a per-(`certPath`, `keyPath`) cached `clientCertReloader` that stats the files on every handshake and reloads on mtime change. An external cert rotator (cert-manager, certbot, internal CA renewer) can swap the PEM files without a process restart; new broker connections pick up the rotated keypair on the next handshake. Connections established before the rotation keep using the previous cert until they reconnect.
- **Bulk DLQ triage**: new operator-gated `POST /api/v1/dlq/bulk/retry` and `POST /api/v1/dlq/bulk/delete` endpoints that take the same filter shape as the list endpoint (query params or JSON body) with a configurable `max_rows` cap. New `GET /api/v1/dlq/groups` aggregation returns top-N error-reason buckets. The `/dlq` page in the SvelteKit UI surfaces a top-patterns chip strip (click to filter) and an additional bulk action bar when the filter matches more rows than fit on the visible page.
- **Pipeline shadow/canary mode** (migration 0021): per-pipeline `shadow_destination_id` + `shadow_percent`. After a successful prod send, the executor ALSO publishes the same payload to the shadow destination for the configured fraction of messages, sampled deterministically by payload hash so redeliveries hit the same decision twice. Failures on the shadow path are counted (`mqconnector_shadow_sent_total` / `_failed_total`) but NEVER affect the prod commit-to-source decision. Use cases: rehearse a broker migration with a parallel candidate cluster, validate a new downstream consumer against real traffic before cutover.

### Changed

- **`RunStages` signature**: `StageOutcome` grows a `Runs []StageRun` field carrying per-stage `Name`, `Duration`, and `Failed` flag. Existing callers (preview handler, replay tool) get the new field for free; the executor consumes it to feed validate-attempt and per-stage-duration metrics without parsing error strings.
- **History hygiene**: residual brand-guide phrasing removed from the frontend; the local main history was rewritten to eliminate brand-context references (the IBM software license country list was intentionally preserved — it's a third-party document, not a brand reference).

### Upgrade notes

- No config changes are required to upgrade — every new feature is opt-in. The default behaviour of every existing field is unchanged.
- To turn on dedup for a pipeline: `PUT /api/v1/pipelines/{id}` with `dedup_window_seconds > 0`.
- To turn on Vault-backed key custody: set `secrets.source: vault` plus the Vault address/mount/path in config, supply `VAULT_TOKEN`, and place the master key as `v1: <hex>` fields under the secret. `MQC_MASTER_KEY` continues to work for sites that aren't using Vault.
- The new schema-drift alerts need the updated `mqconnector-slos.yaml` reloaded into Prometheus.

---

## [1.2.0] — 2026-05-21

Enterprise-hardening release. Closes every outstanding item from the May 2026 production-readiness audit, completes the Postgres production-supported backend, lands per-pipeline RBAC, and rounds out observability for the full operational surface (DLQ, leadership, encryption-at-rest, broker backlog). Breaking-for-operators: prod-mode startup now refuses without `MQC_MASTER_KEY` — set the env var or stay in dev mode.

### Added

- **Operational metrics** at `/api/metrics/prometheus`: `mqconnector_dlq_depth`, `mqconnector_dlq_oldest_age_seconds`, `mqconnector_source_depth` (RabbitMQ queue depth or Kafka consumer-group lag, sampled every 30 s by an in-process depth sampler), `mqconnector_leader{self,holder,mode}`, `mqconnector_leader_lease_remaining_seconds`, `mqconnector_master_key_version`. Seven new alerting rules in `deploy/prometheus/mqconnector-slos.yaml` covering DLQ growth, stuck DLQ rows, leader split-brain, lease-renewal lag, encryption disabled, and source-broker backlog.
- **`mq.DepthReporter` interface**: optional capability on a `Connector`. RabbitMQ implements via passive `QueueDeclare`; Kafka implements via a held-open admin client that sums per-partition `(high water mark − committed offset)` across the topic. The in-memory test connector implements it via channel length.
- **Outbound circuit breaker** (`internal/pipeline/breaker.go`): per-pipeline 3-state (Closed → Open → HalfOpen) gate on the destination broker. 5 consecutive failures opens with a 30 s cool-down; on Open the worker Nacks to source for redelivery instead of flooding the DLQ. Probe-on-recovery races safely across workers via a half-open token.
- **Per-pipeline RBAC** (migration 0018 + `internal/storage/pipeline_grants.go`): per-(pipeline, user) role grants that ESCALATE the tenant role for a specific pipeline only — never demote. New `PipelineGrantsRepo` with `Set` / `Get` / `Delete` / `ListForPipeline` / `ListPipelinesForUser` / `EffectiveRole`. New endpoints `GET /api/v1/pipelines/{id}/grants`, `PUT /api/v1/pipelines/{id}/grants/{userSub}`, `DELETE /api/v1/pipelines/{id}/grants/{userSub}` (effective-admin gated). The list endpoint now carries `effective_role` per pipeline; update/delete/stage/transform/routing handlers gate on the effective role so a viewer-with-admin-grant can manage just their pipeline without tenant-wide privileges.
- **`PipelineGrantsEditor` UI** at `/pipelines/[id]`: in-line role-change, revoke, add-grant form. Read-only view when the caller's effective role is below admin.
- **`/account` page**: identity claims, tenant memberships with role badges, switch-tenant + sign-out actions. Closes the historical 404 from the ProfileMenu's account link.
- **Phase 1 script-stage timeout**: every `script` stage now defaults to a 5 s wall-clock cap (`DefaultScriptTimeoutMs`). Override per-stage via `stage_config.timeout_ms`. The script runner polls `ctx.Done()` on every line so a deadline fires within at most one op-cycle.
- **Phase 1 master-key hard requirement**: prod-mode startup refuses without `MQC_MASTER_KEY` or `MQC_MASTER_KEYS`. Dev mode still tolerates an unset key and emits a warning.
- **`mqconnector kafka-offsets` subcommand**: operator-facing helper for the pre-1.1 → 1.1+ Kafka upgrade hazard. Derives the auto-group id, supports `--to=latest`/`--to=earliest`/`--dry-run`/`--print-group-id`. UPGRADING.md walks the seed-before-start flow.
- **Postgres production support**:
  - Migrations 0010 and 0016 ship Postgres-native bodies via `postgresMigrationOverrides` — `ALTER TABLE … DROP/ADD CONSTRAINT` instead of the SQLite recreate-and-rename idiom that Postgres rejects under FK references.
  - Leadership lease gets a Postgres path: `leadership.NewWithDialect` wraps the upsert in `pg_try_advisory_xact_lock` for explicit cross-replica serialisation. SQLite path keeps the existing INSERT…ON CONFLICT semantics.
  - Multi-replica failover regression test (`TestPostgresLeadership_AdvisoryLockSerialisesClaim`): three replicas, sampling proves ≤ 1 leader at every observation.
- **Storage backend load test** (`cmd/loadtest` + `internal/storage/loadtest` + `scripts/load-test.sh`): runs the same Create / Get / Update / List / Delete mix against either backend, emits JSON with p50/p95/p99 + per-op breakdown, prints PASS / FAIL against a configurable p99 ceiling (default 1.2× the SQLite baseline per `POSTGRES_MIGRATION.md` §6).
- **Kafka rebalance chaos test** (`TestIntegration_Kafka_RebalanceRedeliversInflight`): 2-partition topic, 2-consumer group, consumer A receives + crashes uncommitted, asserts every produced message is redelivered to consumer B post-rebalance.
- **Helm install-time guards** (`deploy/helm/templates/_validate.tpl`): `helm install` / `helm upgrade` fails with a clear error when prod mode is configured without a master key, when `replicaCount > 1` without `leadership.enabled`, or when `auth.insecure_skip_verify` is true in prod. Prevents CrashLoopBackOff diagnostics at 3 am.
- **Helm startup probe**: 60 s startupProbe gates the liveness probe so populated-DB first-boot migrations + audit-chain verification don't trip premature liveness failures.

### Changed

- **Container healthcheck**: docker-compose's `mqconnector` service swaps the broken `wget` probe (distroless ships no shell, no wget) for `["CMD", "/usr/local/bin/mqconnector", "healthcheck"]`. The "unhealthy" state from the previous compose stack is gone. Helm chart already used kubelet's httpGet probe — unaffected.
- **Container drain budget**: docker-compose adds `stop_grace_period: 35s` to match the Helm chart's `terminationGracePeriodSeconds: 35`. Aligns Docker's SIGKILL deadline with the binary's 30 s `pipeline.Manager.StopAndWait` drain budget so in-flight messages aren't lost on `docker compose restart`.
- **Membership adoption resilience** (`internal/storage/memberships.go`): SimpleAuth re-bootstraps that hand out a fresh `sub` claim for the same admin username no longer strand the operator's tenants. `adoptBootstrap` now also salvages stale-sub rows when they match the bootstrap-admin signature (owner of the default tenant), with collision-safe semantics that prevent two regular users with the same username from stealing each other's grants. Four new tests in `memberships_test.go` lock in the behaviour, including the security regression.
- **Grafana dashboard**: 7 new panels for operational health — DLQ depth, DLQ oldest age, source backlog, leader state, lease remaining, master key version.

### Security

- Phase 1 prod-mode hard refusal to start without `MQC_MASTER_KEY` makes the at-rest encryption posture mandatory rather than recommended.
- Per-pipeline RBAC grants are escalation-only by design and gated on the bootstrap-admin signature when adopting stale subs; documented in the `PipelineGrant` doc comment and `EffectiveRole` resolver. Includes a regression test that locks in the no-steal invariant.

### Upgrade notes

See `UPGRADING.md`. Key items:

- Set `MQC_MASTER_KEY=$(openssl rand -hex 32)` in your environment / Helm secret store before upgrading a prod deployment. If you skip it the bridge refuses to start.
- Existing scripted stages keep working — the new 5 s default timeout is generous for typical per-message transforms. Long-running scripts set `stage_config.timeout_ms` explicitly.
- Per-pipeline grants are additive — no migration step is required for existing deployments to keep working. New endpoints are admin-gated.

---

## [1.1.0] — 2026-05-18

Production-readiness release. Closes every item from the §1-§12 audit + every Known Limitation that was in `SECURITY.md` v1.0.0. The bridge gains a WASM plugin system, Postgres foundation (driver dispatch + dialect-aware migrations + repo-level placeholder rewriting + serialisable audit chain), distroless runtime image, cosign-signed release pipeline, and a stack of compliance controls (CSRF, session inactivity timeout, account lockout, GDPR cascade-purge).

### Added

- **Disaster recovery**: `mqconnector backup --to=path` CLI subcommand, scheduled in-process backup worker (`storage.backup.dir`), and `GET /api/v1/admin/backup` system-admin HTTP endpoint. All three use SQLite's online `VACUUM INTO`, verify integrity on the produced snapshot, and rotate older files. See `OPERATIONS.md`.
- **Database integrity check**: `GET /api/v1/admin/integrity` runs `PRAGMA integrity_check` on the live database (system-admin only).
- **At-least-once delivery**: `Commit`/`Nack` on every connector — RabbitMQ d.Ack, Kafka consumer-group offset commit, NATS JetStream msg.Ack, AMQP 1.0 AcceptMessage, IBM MQ MQCMIT. Executor holds the source ack until destination send or DLQ push has succeeded; on failure of both, Nacks for redelivery. Live-broker contract tests in `internal/mq/integration_kafka_test.go`.
- **Kafka durability**: switched from `ConsumePartition(0, OffsetNewest)` to a real consumer group with manual offset commit. Resumes at the last committed offset on restart instead of dropping messages produced while offline. Optional `group_id` override per connection for multi-pipeline scenarios.
- **NATS Core source warning**: a pipeline configured with a core-NATS source (no JetStream stream) logs `WARN` at start announcing the at-most-once trade-off.
- **GDPR cascade-purge**: `DELETE /api/v1/tenants/{id}?cascade=true` runs `TenantRepo.Purge` — one transaction across every per-tenant table. Audit log is intentionally retained.
- **Explicit `system_admin` flag** on memberships (migration 0013). Replaces the implicit "owner of the default tenant" check. New `MembershipRepo.IsSystemAdmin` / `SetSystemAdmin`.
- **CSRF defense**: `SameSite=Strict` session cookies + double-submit-token middleware. Bearer-auth requests pass through. SPA reads `mqc_csrf` from `document.cookie` and echoes it in `X-CSRF-Token`.
- **Sensitive-route rate limits**: tighter per-(tenant, route) bucket on `/config/import`, `/secrets/rotate`, `/preview`, `/samples/extract` (6/min default vs 120/min general).
- **Per-pipeline status gauge** in Prometheus: `mqconnector_pipeline_up{pipeline_id,source,dest,status}`. Plus `mqconnector_active_pipelines`.
- **Latency histogram alerts**: recording rules for p95 + p99 latency via `histogram_quantile`. Three new alerts (`mqConnectorP95LatencyHigh`, `mqConnectorP99LatencyHigh`, `mqConnectorPipelineDown`, `mqConnectorFailureBurst`).
- **OPERATIONS.md incident playbooks**: six runbooks (process down, disk-full, DLQ flood, leader stuck, no-messages-flowing, connection-pool storm), plus maintenance windows (patching, TLS rotation, scaling workers).
- **Real-broker integration tests in CI**: `docker-compose.ci.yml` brings up RabbitMQ + Kafka + NATS; CI runs `go test -tags integration` against them.
- **NATS JetStream manager-layer replay** + UI form: `replayJetStream` in `internal/pipeline/replay.go`, gated UI form on `/pipelines/[id]`.
- **IBM MQ TLS**: connection-level `tls_ca_file` (key-repository stem) and `tls_cert_file` (certificate label) wired into MQSCO. Forces `ANY_TLS12_OR_HIGHER`.
- **Script sandbox memory cap**: `MaxIntermediateBytes` (default 8 MiB) checked every 64 ops during script execution.
- **Container image scan + SBOM** in CI: Trivy fails the build on CRITICAL/HIGH; Syft emits CycloneDX as a workflow artifact.
- **Cosign release signing**: new `.github/workflows/release.yml` fires on `v*` tags, builds + pushes to ghcr.io, signs keyless with cosign, attests the SBOM, attaches the SBOM to the GitHub Release.
- **S3 audit archival**: `audit.s3` config block + wired through `main.go`. Empty credentials default to local-only (air-gapped friendly).
- **Chaos test coverage**: failure-mode contract tests in `internal/pipeline/chaos_test.go`.
- **Account lockout**: per-username sliding-window counter on `/api/auth/login`. 5 failures within 5 min → 15-min lockout, regardless of source IP. Case-folded so attackers can't iterate casing variants.
- **Session inactivity timeout**: `auth.idle_timeout` config knob. Sliding cookie refresh on every authenticated request — idle browser tabs auto-logout. Required by HIPAA / NIST 800-53 IA-11. Default 0 (disabled) preserves legacy behaviour.
- **Distroless runtime image**: `Dockerfile` final stage runs `gcr.io/distroless/static-debian12:nonroot`. No shell, no `wget`, no package manager. New `mqconnector healthcheck` subcommand replaces the wget-based HEALTHCHECK.
- **pprof endpoint**: `/api/v1/admin/pprof/*` (system-admin only) for production runtime debugging.
- **Cosign release signing**: `.github/workflows/release.yml` signs images keyless on `v*` tags and attests the CycloneDX SBOM.
- **Helm chart catch-up**: `storage.backup`, `audit.s3`, `auth.idle_timeout` exposed in `values.yaml`. Pod security context defaults updated for the distroless `nonroot` user (UID 65532).
- **WCAG**: skip-to-main-content link in the app shell (WCAG 2.1 SC 2.4.1).
- **CHANGELOG.md** (this file) with SemVer + 3-phase deprecation policy.
- **TLS certificate hot-reload**: HTTPS listener now serves via `tls.Config.GetCertificate`; a 30-second watcher detects file mtime changes and reloads atomically. cert-manager / certbot rotations no longer need a `systemctl restart`.
- **Per-pipeline message budget**: `pipelines.max_msgs_per_minute` (migration 0014). Fixed-window throttle shared across all workers of one pipeline so a noisy pipeline can't starve its neighbours on shared destination brokers.
- **Real-time syslog forwarding**: `audit.syslog_url` config (tcp:// or udp://). Every successful audit insert fans out an RFC 5424 message with the audit row's JSON in the MSG field. Non-blocking on the hot path, 1024-deep buffer, reconnect with backoff.
- **UPGRADING.md** doc with per-version upgrade procedure + Helm + multi-replica zero-downtime variant + rollback.
- **docs/API_RECIPES.md** with curl + jq recipes for the workflows operators actually do.
- **docs/PLUGIN_DESIGN.md** — design note for the future WASM-based custom-stage system. Captures the four candidate mechanisms (plugin.Open / WASM / gRPC sidecar / scripting) and lays out the proposed wazero path so the eventual implementation lands on a thought-through shape.
- **WASM plugin system**: operators upload `.wasm` plugins via `POST /api/v1/plugins` (system-admin only). Pipelines reference them with `stage_type='wasm'` and `stage_config={"plugin":"<name>"}`. Sandboxed by wazero — no host imports, hard memory cap, no shell / FS / network. Migration 0015 adds the `plugins` table; migration 0016 widens the stage_type CHECK. See `docs/PLUGIN_DESIGN.md` for the contract.
- **Postgres backend foundation**: pgx driver registered, `Open` dispatches on DSN scheme, `migrate()` is dialect-aware (skips `PRAGMA`, rewrites `?` placeholders, translates `INSERT OR IGNORE` → `ON CONFLICT DO NOTHING`, `BLOB` → `bytea`). Integration test `TestPostgresOpen_AppliesMigrations` confirms the migration set applies on `postgres:16`. Per-repo placeholder rewriting is the remaining mechanical work — see `POSTGRES_MIGRATION.md` §7.

### Changed

- Session cookie `SameSite` tightened from `Lax` to `Strict`.

### Security

- **Trivy supply-chain incident (2026-03-19)**: pinned `aquasecurity/trivy-action` to `v0.35.0`, the first GitHub-immutable-releases-protected tag. Earlier tags were force-pushed to malicious commits during the disclosed exposure window.

### Fixed

- **Reload race** (was production bug): rapid `POST /api/v1/reload` could cause the OLD executor's deferred `Metrics.Unregister` to fire after the NEW executor's `Register`, leaving an active pipeline invisible to `/api/health`. Reload now waits for the prior executor's goroutine to exit before spawning the replacement. Contract test in `internal/pipeline/reload_race_test.go`.
- **Kafka offset never reached the broker** (was caught by the integration test we just shipped): `Commit` called sarama's `MarkMessage` (local mark) without `sess.Commit()` (network round-trip). Now flushes synchronously per processed message.
- **DLQ reaper infinite loop**: `retry_count` wasn't incremented on send failure so the same row replayed forever. Increment now happens before the send attempt.
- **Replay infinite wait** on a window past the broker's latest offset: 2-second idle timeout per partition consumer.
- **gitops content negotiation**: client expected JSON but server defaulted to YAML. Client now sends `Accept: application/json`.
- **OTLP boot failure** on a schema-URL collision: removed `resource.Merge(resource.Default(), ...)` so the OTLP exporter doesn't fight the SDK's default resource.

---

## [1.0.0] — initial release

First tagged release. Single-binary message-queue bridge across IBM MQ / RabbitMQ / Kafka / NATS / MQTT / AMQP 1.0 with pipeline stages, DLQ with retry, embedded SvelteKit admin UI, SimpleAuth integration, multi-tenant isolation, hash-chained audit log, and AES-256-GCM envelope encryption for stored credentials.
