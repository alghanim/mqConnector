# mqConnector — Platform Evolution

> Canonical multi-wave strategy document. Ratifies what shipped in Wave 1 (Pipeline Studio, v1.4.0) and pins scope, dependencies, and primitives for Waves 2–6.
>
> Per-wave detailed implementation plans live alongside this document under `/Users/ag-work/.claude/plans/` and are written **when that wave starts**, not up-front.

---

## 1. Mission

mqConnector is not an admin panel for a message bridge — it becomes the **enterprise integration operating system** for the department.

The product covers three years of feature work that turns a mature single-binary message-queue bridge (IBM MQ / RabbitMQ / Kafka / MQTT / NATS / AMQP 1.0, multi-broker hot-reload, tamper-evident audit, multi-version envelope encryption, per-pipeline RBAC, tenant fairness, shadow/canary, broker mTLS hot-reload, payload redaction, per-stage latency histograms, Vault custody, S3 audit archival, CEF syslog) into a coherent operational platform.

Every persona — pipeline designer, on-call operator, security / compliance reviewer, tenant owner, platform engineer, brand-new operator — gets a purpose-built surface that turns latent telemetry, audit, DLQ, and RBAC data into **decisions**. The AI layer is connective tissue — air-gapped, audit-logged, human-in-the-loop, never auto-apply.

The wedge is **leverage**. The 1.3.0 release closed the last enterprise-readiness gaps on the backend. The platform now collects 1.3.0-grade telemetry that the UI didn't surface. The evolution converts that latent data into operator confidence — without a re-architecture.

**Intended outcome.** One pane where regulated message flows are *designed, governed, observed, explained, and remediated*. Six waves, months-long, each independently shippable. Air-gapped throughout. Self-hosted LLM as the connective tissue.

**Non-negotiables (carry through every wave):**

- Single Go binary, SvelteKit embedded via `go:embed`.
- SimpleAuth (never OIDC) — air-gapped department deploy.
- SQLite or Postgres, both with in-binary migrations.
- Tailwind + brand tokens only (no Bootstrap / Material / Chakra / d3-bundled chart libs).
- TLS-everywhere outside dev.
- Dark + light theme parity on every screen.
- RTL via CSS logical properties.
- Brand-token discipline enforced via `scripts/check-no-hex.sh`.
- AI is **never** auto-apply; every AI surface degrades to the deterministic path when offline.

---

## 2. Roadmap at a Glance

| Wave | Theme | Lead persona | New surface | Key backend additions | Status |
|------|-------|--------------|-------------|----------------------|--------|
| **1** | **Pipeline Studio** | Pipeline owner | `/studio` (canvas + structured editors + dry-run + versions + diff + rollback) | `pipeline_revisions` table; 6 revision endpoints; `StageRun.Body/Format/Err` for dry-run | **DONE — v1.4.0** |
| **2** | **Live Topology & Flow Command Center** | On-call operator | `/topology` (force-directed live graph); evolved `/` | `GET /topology` aggregator; circuit-state gauge + event; `Manager` recent-traffic ring buffer | Planned |
| **3** | **DLQ Intelligence Console** | On-call + pipeline owner | Rebuilt `/dlq` (fuzzy clusters, payload diff, replay-sim, retry-confidence, explain-why) | `error_fingerprint` + `error_template` + `failing_stage_*` columns; cluster API; replay-sim endpoint; **AI workstream begins** | Planned |
| **4** | **Observability Drilldown & Operational Intelligence** | Platform engineer | `/observability` (per-stage waterfall, percentiles, anomalies, alert ribbon) | `internal/explain/` package; `GET /explain/{subject}/{id}`; in-process SLO evaluator | Planned |
| **5** | **Governance Center** | Security reviewer + tenant owner | `/governance/{audit,rbac,keys,approvals}` | `proposed_changes` table (approvals); audit search + facets; RBAC matrix endpoint; key rotation dry-run | Planned |
| **6** | **Extend & Adopt** | Platform engineer + tenant owner + new operator | `/plugins`, evolved `/tenants/[id]/usage`, `/onboarding`, `/lineage` | Plugin signing (`signature`, `publisher`, `version`); tenant quotas + daily rollup; correlation-id ring buffer | Planned |

**Sequencing rationale.** Wave 1 ships the `pipeline_revisions` primitive everything else binds to (replay-sim, approvals, lineage). Wave 2 cashes in existing telemetry for instant operator value. Wave 3 introduces the AI workstream over the cluster surface (highest immediate leverage). Wave 4 layers explainability over Waves 2–3. Wave 5 binds approvals to revisions and codifies compliance posture. Wave 6 onboards the next 100 operators onto a mature platform.

---

## 3. What Shipped in Wave 1 (Pipeline Studio)

**Released as v1.4.0 on 2026-05-25.** Promotes `/flow` from a 2024 prototype into the **primary** pipeline workflow.

### 3.1 New surfaces

- **`/pipelines/{id}/studio`** — three-pane shell with bottom dry-run dock:
  - **Palette** (left) — drag source for the 7 stage types.
  - **Canvas** (center) — SVG graph of source → stages → destination + alternate route destinations.
  - **Inspector** (right) — structured editor for the selected node + green/red validity indicator + per-field errors.
  - **DryRunDock** (bottom) — sample picker (paste + fixtures), per-stage outcome strip (timing pill + collapsed body preview), click-to-diff between adjacent stages.
- **VersionRail** (left) — collapsible per-pipeline revision list with Live / Draft badges; pick one or two revisions for compare.
- **DiffViewer** — structured pipeline / stage / transform / routing-rule diff; renders Added / Modified / Removed sections; overlays on the canvas.
- **DeployDialog** — diff vs Live + summary input + optional approver field → confirm → transactional write-through + `MarkDeployed` + `Manager.Reload`.
- **Rollback** — creates a NEW revision holding the target's snapshot and promotes it via the same ceremony; audit-logged via the existing `AuditAdminActions` middleware.
- **CommandPalette entries** — `Studio: Deploy`, `Studio: Compare to live`, `Studio: Discard draft` (gated on `/studio` routes).
- **Legacy form view** preserved at `/pipelines/{id}?legacy=1`; plain `/pipelines/{id}` 307-redirects into the Studio.

### 3.2 Structured per-stage editors

| Stage | Editor | Notes |
|-------|--------|-------|
| `filter` | `FilterEditor` | Path chips + PathPicker |
| `transform` | `TransformEditor` | Wraps existing `TransformListEditor`; per-row type / source / target / mask / set-value / drag-reorder |
| `translate` | `TranslateEditor` | Output-format dropdown + conditional `SchemaSelector` + `proto_message` |
| `route` | `RouteEditor` | Wraps existing `RoutingRuleListEditor`; per-row condition / operator / destination / priority / enabled |
| `script` | `ScriptEditor` | Monospace textarea with line numbers (hand-rolled); `timeout_ms` (5000 default, 30000 max); "Test on sample" CTA |
| `validate` | `ValidateEditor` | `SchemaSelector` filtered by type; `proto_message` for protobuf; "Test schema" CTA |
| `wasm` | `WasmEditor` | Plugin dropdown + metadata (size, checksum, uploaded_at); "Upload new" Dialog |

Every editor exposes `bind:config` (string JSON) and `bind:valid` (bool); unknown keys preserved on commit (forward-compat rule). The raw-JSON `<details>` escape hatch is retained, defaults closed.

### 3.3 New backend primitives

- **`pipeline_revisions` table** (migration 0022) — append-only snapshots:
  ```sql
  CREATE TABLE pipeline_revisions (
      id, tenant_id, pipeline_id, revision_number, snapshot, snapshot_hash,
      author_sub, author_username, change_summary, created_at, deployed_at, deploy_request_id
  );
  UNIQUE (pipeline_id, revision_number)
  ```
  Snapshot is canonical JSON of `{Pipeline, Stages, Transforms, RoutingRules, SchemaVersion}`. Hash excludes child IDs and timestamps so identical PUTs collapse. Per-pipeline mutex makes revision-number assignment race-free.
- **`pipelines.requires_approval BOOLEAN`** — Wave-5 forward-compat column; default `false`.
- **Snapshot helper** — `snapshotPipelineRevision` invoked from `handleUpdatePipeline`, `handleReplaceStages`, `handleReplaceTransforms`, `handleReplaceRoutingRules`. Async via background goroutine with 5s timeout (the synchronous request-context path bled the request lifecycle into the snapshot path); tracked by a `pendingBackgroundOps` WaitGroup so the shutdown drain blocks for in-flight writes (bounded 5s).
- **Tx-aware repo variants** — `ReplaceForPipelineTx` on stages/transforms/routing-rules and `UpdateTx` on pipelines, sharing one `storage.Tx` for atomic deploy/rollback writes.
- **`CreateForce`** — bypasses hash dedup for operator-intent inserts (rollback creates a NEW revision even when its snapshot matches an older one).
- **`StageRun` extension** — preview engine captures per-stage `body`, `format`, `err`; surfaced as `stage_runs[]` on `POST /v1/preview`. Body is `bytes.Clone`d so the dry-run dock can render without read corruption.

### 3.4 New endpoints

| Method | Path | RBAC |
|--------|------|------|
| GET | `/api/v1/pipelines/{id}/revisions` (paginated) | viewer |
| GET | `/api/v1/pipelines/{id}/revisions/current` | viewer |
| GET | `/api/v1/pipelines/{id}/revisions/{rev}` | viewer |
| GET | `/api/v1/pipelines/{id}/revisions/{rev}/diff?against={other}` | viewer |
| POST | `/api/v1/pipelines/{id}/revisions/{rev}/rollback` | operator |
| POST | `/api/v1/pipelines/{id}/deploy` | operator |

The diff endpoint emits structured `{added, modified, removed}` sets with positional child matching. The rollback handler creates a new revision from the target snapshot, write-throughs transactionally, then triggers `Manager.Reload`. The deploy handler promotes an existing revision; the latent `requires_approval` gate returns 409 when set without an approver.

### 3.5 Hygiene

- **`scripts/check-no-hex.sh`** — brand-token discipline CI lint. Comment-aware, perl-stripping, with an `# check-no-hex: ignore` escape hatch for intentional exceptions (e.g. `<meta name="theme-color">`).
- All new components are theme-token-driven (dark + light parity verified) and use CSS logical properties for RTL.

### 3.6 What did not ship (deferred to Wave 2+)

- Proper draft-vs-deploy separation. Today the Studio's Deploy button re-deploys the *latest existing* revision (round-tripping the operator through the approval/summary ceremony); legacy PUTs still auto-deploy via the snapshot helper. There is no "save draft without deploy" path yet.
- Tenant-saved samples library in the DryRunDock (paste + fixtures only).
- Recent-traffic sample picker tab (needs a Manager ring buffer — Wave 2).
- PathPicker integration in the wrapped TransformEditor / RouteEditor row UIs (the PathPicker component ships in Wave 1, but the row UIs still take a hand-typed path).
- Approval-required-to-deploy *UI* (column exists; the toggle to flip `requires_approval` is not yet exposed).
- Deploy-button auto-disable when any per-stage editor reports `valid=false` (today: visual `!` indicator in the Inspector; the server still rejects).

### 3.7 Backwards compatibility

- No breaking changes. Existing PUT handlers continue to work; their semantics (save = deploy) are preserved via the snapshot helper.
- Legacy `/pipelines/{id}` URL still serves the form view when `?legacy=1` is appended (one-release safety net).
- Migration 0022 runs in-binary at first boot.

---

## 4. Wave 2 — Live Topology & Flow Command Center

**Goal.** Convert `/` + `/flow` into a *live* broker-to-broker topology — the page operators open at 3 a.m. Median time-to-first-signal during incident < 5 s.

### Backend additions

- **`GET /api/v1/topology`** — single aggregator over connections + pipelines + per-pipeline `mqconnector_pipeline_up` + circuit-breaker state + DLQ depth + source/dest depth gauges. Caches for 1s to coalesce SSE fan-out.
- **`mqconnector_circuit_state`** gauge (Open / HalfOpen / Closed) + breaker-state event on the existing `internal/events` bus → SSE pushes deltas instead of full snapshots.
- **`Manager` recent-traffic ring buffer** — ~25 recent post-stage outputs per pipeline, ≤64 KB each. Powers Studio's "Recent traffic" sample picker tab that Wave 1 deferred.
- **Tenant-saved samples library** — `tenant_samples(tenant_id, name, body, format, created_by, created_at)` (migration 0023). Powers the second deferred Studio tab.

### Frontend surfaces

- **`/topology`** — force-directed graph: brokers = nodes, pipelines = animated edges, depth/throughput = edge weight, circuit-open = red-pulsed edge, shadow destinations = dashed.
- **Evolved `/`** — flow strip + alert ribbon above the existing KPI tiles. DLQ count badge moves to the COMMAND header so it's globally visible.
- **New chart primitives** under `web/src/lib/components/charts/`:
  - `TopologyGraph.svelte` — ~200-line hand-rolled force layout (vendor a `web/src/lib/charts/force.ts` single-file simulator rather than pull `d3-force`).
  - `LiveSankey.svelte`
  - `EdgePulse.svelte`

### Studio retrofit

- DryRunDock gets `Recent traffic` and `Saved samples` tabs (was paste + fixtures only).
- Canvas optionally overlays a Topology layer (throughput annotations, last-seen latency, error pulses) — toggled from the StudioHeader.

### Why second

Cheapest leverage — backend data already exists; biggest perceived shift in product identity.

---

## 5. Wave 3 — DLQ Intelligence Console (AI workstream begins)

**Goal.** From "bulk delete + exact-match buckets" (current state, shipped in 1.3.0) to **fuzzy clustering + payload diff + replay-sim + retry-confidence + root-cause hints**. Mean DLQ triage time ↓ ≥50%.

### Backend additions

- **`internal/dlq/cluster`** package — SimHash + tokenised-template fingerprinting (`validation: missing field <X>` collapses across X values).
- **Migration 0024**: `dlq.error_fingerprint`, `dlq.error_template`, `dlq.failing_stage_name`, `dlq.failing_stage_index` (populated at insert from the `StageRun` observation log already added in 1.3.0).
- **`GET /api/v1/dlq/clusters`** — returns clusters with representative payload + impact (count, tenants affected, time span, blast-radius pipelines).
- **`POST /api/v1/dlq/{id}/replay-sim`** — runs the message through the current pipeline revision in preview mode (reuses Wave 1 `RunStages`); returns `would_succeed`, `failing_stage`, `delta_from_original`. **No broker contact.**
- **`GET /api/v1/dlq/{id}/diff?against={other_id}`** — payload-shape diff.
- **`internal/ai/` package** — provider abstraction (see §8); cluster-naming + plain-English summary feature behind the `dlq_cluster_naming` allowlist (off by default).

### Frontend surfaces

- **Rebuilt `/dlq`** — cluster panel left, cluster detail with representative payload + impact bar centre, action drawer right with retry-confidence pill (from replay-sim) and "explain why this failed" panel.
- **New chart primitives** — `Heatmap.svelte` (error timeline), `PayloadDiff.svelte` (LCS-based, shares code with Studio's PayloadDiffView).

### Dependency

Replay-sim binds to Wave 1's stable revision concept (`pipeline_revisions` is the input to the sim).

---

## 6. Wave 4 — Observability Drilldown & Operational Intelligence

**Goal.** Per-stage error metrics, latency percentiles, anomaly highlighting, *explain-why* panels. Time from SLO breach to root-cause hypothesis < 2 min.

### Backend additions

- **`internal/explain/`** package — composable explainer modules over existing metrics + audit + DLQ + breaker view:
  - `circuit_explainer.go` — last 5 failures, breaker state transitions, dest broker depth, dedup hit-rate window.
  - `drift_explainer.go` — `validate_attempts` vs `_failures` deltas, top error templates from clustered DLQ, last revision change.
  - `latency_explainer.go` — per-stage histogram deltas, source-broker depth, leader-handoff events.
  - `dlq_root_cause.go` — revision diff since first occurrence, schema changes, recent transform edits.
- **`GET /api/v1/explain/{subject}/{id}`** returns structured `Explanation{Subject, Headline, Facts[], Sections[], AISummary?}`.
- **`internal/slo/`** in-process evaluator parsing the same `deploy/prometheus/mqconnector-slos.yaml` (single source of truth — no rule drift between Prometheus and the in-binary alert ribbon).
- **`GET /api/v1/alerts/active`** — drives the cross-shell alert ribbon.

### Frontend surfaces

- **`/observability`** — per-pipeline detail with stage waterfall (p50/p95/p99 `PercentileBand` per stage), drift chart, depth + breaker overlays, anomaly markers.
- **Alert ribbon** component across the shell (every page).
- **New chart primitives** — `PercentileBand.svelte`, `WaterfallStages.svelte`, `AnomalyMarker.svelte`.

### Dependency

Reuses Wave 2 nav and Wave 3 cross-links (explainer modules pull from the DLQ cluster index).

---

## 7. Wave 5 — Governance Center

**Goal.** Compliance posture as a first-class surface. SOC 2 / ISO 27001 walkthrough = one click per control. Zero unsigned key rotations.

### Backend additions

- **`internal/approval/`** package + **`proposed_changes`** table (migration 0025). Pipeline-revision promotion (Wave 1) optionally requires N-of-M approvals from a configured role set; promotion writes `approval_record` with approver subs, timestamps, hash bound to the revision. Reusable for key rotation + plugin install.
- **`GET /api/v1/audit/search`** — filters (actor, action, resource, tenant, date, status) + facets.
- **`GET /api/v1/rbac/matrix?tenant=`** — (user × resource) effective-role grid.
- **`GET /api/v1/secrets/history`** (from audit) + **`POST /api/v1/secrets/rotate/dry-run`**.

### Frontend surfaces

- **`/governance/audit`** — filterable timeline + diff viewer over existing `audit_log_diffs`.
- **`/governance/rbac`** — user × resource heatmap, click to grant/revoke.
- **`/governance/keys`** — key-version timeline, rotation wizard with dry-run preview, custody source (env/file/Vault).
- **`/governance/approvals`** — pending approvals queue.
- **Approval-required badge** on Studio's Deploy flow (uses `pipelines.requires_approval` column shipped in Wave 1) — finally exposes the toggle Wave 1 deferred.

### Dependency

Binds to Wave 1 revisions (approval rows hash-bind to revision IDs); binds to Wave 4 explainers (for "why this rotation was needed" cards).

---

## 8. Wave 6 — Extend & Adopt (Plugins + Tenant Ops + Onboarding + Lineage)

**Goal.** Platform growth surface. New tenant zero-to-first-message < 30 min. Plugin installs require signature. Tenant owners self-serve quota visibility without admin tickets.

### Backend additions

- **`internal/plugins/signing/`** — cosign-style detached signature verification on upload; `plugins.signature`, `plugins.publisher`, `plugins.version`, `plugins.min_runtime` columns (migration 0026). `plugins.require_signing` config defaults `false` for two minor releases, then flips in a major (per deprecation policy).
- **Per-plugin Prometheus counters** (invocations, errors, p95 duration, memory peak — wazero exposes hooks; cardinality capped at the plugin level — per-(plugin, tenant) explodes).
- **`tenant_quotas(tenant_id, max_pipelines, max_msgs_per_day, max_dlq_rows, sla_target_p99_ms)`** (migration 0027) + **`tenant_usage_daily`** rollup table; nightly worker scrapes metrics + DLQ counts.
- **Lineage primitive (lightweight)** — rely on existing W3C trace-context plumbing in `internal/tracing`; persist `correlation_id` on DLQ rows + a 24h ring buffer `recent_messages(correlation_id, pipeline_id, source_conn, dest_conn, outcome, latency_ms, ts)` capped ~1M rows TTL-evicted. **Deliberately NOT a graph DB.**

### Frontend surfaces

- **`/plugins`** — upload, signature status, sandbox observability, per-plugin metrics, version pinning.
- **`/tenants/[id]/usage`** — daily/weekly throughput chart, SLA dial, quota headroom bars, top pipelines.
- **`/onboarding`** — first-run wizard (system-admin / tenant-owner / operator tours) with integration templates ("Kafka → IBM MQ JSON-passthrough", "RabbitMQ → Kafka with PII redaction").
- **`/lineage`** — paste correlation ID, see the path.
- **Guided playbooks** embedded into `/help` as runnable checklists.

---

## 9. Cross-Cutting AI Workstream

Introduced in Wave 3, expanded across Waves 4 / 5 / 6 (Wave 1 retrofit possible).

### Provider abstraction

Single `internal/ai/provider.go`:

```go
type LLMProvider interface {
    Complete(ctx, CompletionRequest) (CompletionResponse, error)
    StructuredOutput(ctx, schema, prompt) (json.RawMessage, error)
    Capabilities() Capabilities
}

type Config struct {
    Endpoint   string         // OpenAI-compatible: vLLM / llama.cpp / TGI / Ollama
    Model      string
    AuthHeader string
    TimeoutMs  int            // default 8000
    MaxTokens  int
    EnabledFor []Capability   // explicit allowlist per feature
    AuditEvery bool           // default true
}
```

Implementation: `openaiCompatible{}` (net/http + json only — no vendor SDK). Tests use a `fakeProvider`. Reuses department's self-hosted models.

### Capabilities by wave

- **Wave 3 (DLQ).** Cluster naming + plain-English summary. Root-cause hints over fingerprints.
- **Wave 1 retrofit (optional).** Transformation-from-example (paste before/after, AI proposes `transform` rules). Schema-mapping suggestions when wiring new pipelines.
- **Wave 4 (Observability).** Anomaly explanation — explain-why summary layer over `Explanation` struct (structured output works without LLM; AI is sidecar).
- **Wave 5 (Governance).** Migration assistant for revision diffs in human terms. Redaction-pattern auto-detect — scan recent DLQ payloads, propose jsonpath/regex rules for the existing redaction system (shipped 1.3.0).
- **Wave 6 (Adopt).** Daily operational digest mailed to tenant owners (throughput, top errors, SLA posture, suggested actions).

### Non-negotiable constraints

- Every AI call writes an `ai_audit` row (`id, ts, feature, caller_sub, tenant_id, prompt_hash, model, endpoint, tokens_in, tokens_out, latency_ms, outcome, result_id_ref` — migration 0028).
- Every AI-derived surface shows a visible "AI suggestion" chip + "explain" tooltip linking to the audit row.
- **Never auto-apply.** Suggestions land in a draft slot → operator clicks apply or discards. Studio's diff/promote flow (Wave 1) handles the apply path.
- Air-gapped fallback: every feature degrades to the deterministic path when endpoint unreachable.
- `mqconnector_ai_calls_total{outcome=...}` exposes provider health.

### `config.example.yaml` addition

```yaml
ai:
  enabled: false                # opt-in per deploy
  provider: openai_compatible
  endpoint: http://llm.internal/v1
  model: qwen2.5-14b-instruct
  auth_header: ${AI_AUTH_HEADER}
  timeout_ms: 8000
  max_tokens: 1024
  features:                     # explicit allowlist, none enabled by default
    - dlq_cluster_naming
    - transformation_from_example
    - explain_why_summary
    - redaction_pattern_detect
  audit_every_call: true
```

---

## 10. New Backend Primitives — Catalogue

Cumulative, in delivery order:

| Primitive | Wave | Notes |
|-----------|------|-------|
| **Pipeline revisions** | 1 (shipped v1.4.0) | Append-only snapshots; everything else binds |
| **`pipelines.requires_approval`** | 1 (shipped v1.4.0) | Forward-compat column; UI in Wave 5 |
| **`StageRun.Body/Format/Err`** | 1 (shipped v1.4.0) | Per-stage capture for dry-run + replay-sim |
| **`Manager` recent-traffic ring buffer** | 2 | ~25 outputs × pipeline; Studio "Recent traffic" tab |
| **`tenant_samples`** | 2 | Studio "Saved samples" tab |
| **`mqconnector_circuit_state` gauge + event** | 2 | Topology edge state |
| **Per-stage error indexing** | 3 | `error_fingerprint`, `error_template`, `failing_stage_*` on DLQ |
| **DLQ cluster index** | 3 | SimHash + tokenised-template fingerprinting |
| **Replay-sim endpoint** | 3 | Reuses Wave 1 `RunStages` over a stable revision |
| **LLM provider + AI audit** | 3 | `internal/ai/`, `ai_audit` table |
| **Explainer engine** | 4 | `internal/explain/` composable modules + `Explanation` struct |
| **In-process SLO evaluator** | 4 | Parses same `mqconnector-slos.yaml` as Prometheus |
| **Approval workflow** | 5 | `proposed_changes` table; reusable across resource kinds |
| **Audit search + facets** | 5 | `GET /api/v1/audit/search` |
| **RBAC matrix endpoint** | 5 | `GET /api/v1/rbac/matrix?tenant=` |
| **Plugin signing + versioning** | 6 | `signature`, `publisher`, `version`, `min_runtime` |
| **Per-tenant quotas + usage rollup** | 6 | `tenant_quotas`, `tenant_usage_daily` |
| **Correlation-id lineage** | 6 | `recent_messages` ring buffer (NOT a graph DB) |

---

## 11. Information Architecture (Final, after Wave 6)

Group by **operator intent**, not data model:

```
COMMAND        (operate now)
  /                Overview                — KPI strip + alert ribbon
  /topology        Live Topology           — broker/pipeline graph
  /dlq             DLQ Intelligence        — clusters, replay-sim
  /observability   Operations Intelligence — per-stage, percentiles, explain-why

BUILD          (design and ship change)
  /studio          Pipeline Studio         — visual builder, revisions, promote   ← shipped v1.4.0
  /connections     Connections
  /lineage         Message Lineage         — correlation lookup

GOVERN         (compliance and trust)
  /governance/audit     Audit Explorer
  /governance/rbac      Permissions Matrix
  /governance/keys      Encryption & Custody
  /governance/approvals Pending Approvals

EXTEND         (platform capability)
  /plugins         WASM Plugins
  /tenants         Tenants & Quotas        — per-tenant usage, SLA
  /tokens          API Tokens
  /webhooks        Webhooks

LEARN          (operator confidence)
  /onboarding      Guided Setup
  /help            Playbooks & Reference
  /settings        System Settings
```

DLQ count badge moves to COMMAND header. Alert ribbon (Wave 4) sits above the page area. `/flow` 301s to `/studio` (Wave 1 implemented the 307; the 301 lands when the legacy form view is removed in Wave 2).

---

## 12. Visualization Stack Decision

**Keep hand-coded SVG; add primitives wave-by-wave.**

Reasoning: COMPLIANCE rule against component libraries implicitly disfavours runtime chart libraries (d3 alone ~270 KB; chart.js ~200 KB — breaches the "every byte ships in the binary, every byte renders air-gapped" mental model). Existing `Sparkline.svelte` + `SystemPulse.svelte` prove pure-SVG with small hand-rolled scale helpers (`web/src/lib/charts/scale.ts`) is tractable.

Acknowledged tradeoff: `TopologyGraph` (force-directed) is non-trivial → vendor a ~200-line single-file force simulator (`web/src/lib/charts/force.ts`) rather than pull `d3-force`.

Brand-token discipline enforced via `scripts/check-no-hex.sh` (shipped Wave 1) which fails on any hex outside `web/src/lib/brand-tokens.css`. Chart primitives consume only `var(--chart-series-1..8)` (define those tokens up front when Wave 2 starts).

**New primitives in `web/src/lib/components/charts/` (introduced wave-by-wave):**

| Primitive | Wave | Notes |
|-----------|------|-------|
| `Sparkline.svelte` | 1 | Existing; remains in place |
| `TopologyGraph.svelte`, `LiveSankey.svelte`, `EdgePulse.svelte` | 2 | Hand-rolled force layout |
| `Heatmap.svelte`, `PayloadDiff.svelte` | 3 | LCS-based diff (PayloadDiff shares code with Studio's PayloadDiffView shipped in Wave 1) |
| `PercentileBand.svelte`, `WaterfallStages.svelte`, `AnomalyMarker.svelte` | 4 | Latency UX |

---

## 13. Per-Wave Success Metrics

Operator-facing, not LOC.

- **Wave 1.** Time-to-first-pipeline < 15 min for new operator; config errors detected post-deploy ↓50% (dry-run catches earlier); zero rollbacks-by-DB-restore (rollback button replaces them).
- **Wave 2.** Median time-to-first-signal during incident < 5 s; topology page becomes default homepage by operator preference (telemetry on first-page).
- **Wave 3.** Mean DLQ triage time ↓ ≥50%; ratio of "retry without fix" actions ↓; cluster-cardinality reduction vs raw error count ≥10×.
- **Wave 4.** Time from SLO breach to root-cause hypothesis < 2 min; per-stage latency answerable in UI without Grafana; explain-why opened on ≥30% of incident pages.
- **Wave 5.** SOC 2 walkthrough time ↓; "who can do what" answerable in one screen; zero unsigned key rotations.
- **Wave 6.** New tenant zero-to-first-message < 30 min; plugin install requires signature; tenant owners self-serve quota visibility without admin tickets.

---

## 14. Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| Revision migration with existing pipelines | (Resolved in Wave 1) Snapshot helper backfills on first save; legacy PUT endpoints kept for two minor releases per deprecation policy. |
| Hot-reload race during revision diff capture | (Resolved in Wave 1) Reuses manager's existing serialisation (`reload_race_test.go`); snapshot helper is async with WaitGroup-tracked bounded drain on shutdown. |
| AI latency in air-gapped env | Hard 8s timeout; UI shows skeleton; degrades to deterministic path; AI suggestions are sidecar drawers, never modal. |
| Brand-token discipline at scale | (Resolved in Wave 1) `scripts/check-no-hex.sh` CI lint fails on any hex outside `brand-tokens.css`. |
| In-process SLO evaluator drifting from Prometheus rules | Parse the same `mqconnector-slos.yaml` file at boot — single source of truth. |
| Plugin signing breaking sites with unsigned plugins | `plugins.require_signing` defaults false; warn for two minor releases; flip in a major. |
| WASM cardinality explosion | Per-plugin labels OK; per-(plugin, tenant) explodes — cap at plugin level. |
| Approval workflow deadlocking shipping | Tenant-owner emergency-override produces a flagged audit row. |
| Topology aggregator hot-path cost (Wave 2) | Coalesce SSE fan-out with 1s cache; aggregator is read-only over already-collected metrics; no new sampling. |
| Replay-sim creating broker side-effects (Wave 3) | Hardwired preview mode — `RunStages` does not touch destination connectors; assert with a counting test connector. |

---

## 15. Documentation Deliverables

Created at the wave that needs them, not up-front.

- **`MQCONNECTOR_PLATFORM_EVOLUTION.md`** — this document (created end of Wave 1).
- `docs/operator-handbook/README.md` — index of cross-functional journeys (Wave 2 — when there are multiple journeys to index).
- `docs/operator-handbook/tenant-onboarding.md` — zero to first producing pipeline (Wave 6, with `/onboarding`).
- `docs/operator-handbook/pipeline-promotion.md` — Studio draft → dry-run → approval → production swap → rollback (Wave 5, when approvals land).
- `docs/operator-handbook/dlq-triage-decision-tree.md` — when to retry / fix-upstream / discard (Wave 3, with Cluster Console).
- `docs/operator-handbook/key-rotation.md` — generate, stage, dry-run, rotate, verify, audit (Wave 5, with key-rotation wizard).
- `docs/operator-handbook/audit-forensics.md` — unexpected actor investigation, hash-chain verification, syslog/CEF integration (Wave 5).
- `docs/ai-ops.md` — self-hosted model integration, capability matrix, audit guarantees, air-gapped behaviour (Wave 3, when AI lands).
- `docs/explain/README.md` — explainer-module catalogue + how to add one (Wave 4).

---

## 16. Execution Handoff

After Wave 1 ships (v1.4.0), the recommended path forward:

1. **Wave 2 planning** — write a Wave 2 plan to `/Users/ag-work/.claude/plans/` following the same pattern (execution-ready Wave detail; subsequent waves remain strategic outlines).
2. **Per-wave gates** — `go test ./...` + `cd web && npm run check && npm run test` + `scripts/check-no-hex.sh` + `scripts/build.sh` must pass before commit.
3. **Per-wave ship** — version-bump.sh (confirm with user), update CHANGELOG.md, update `docs/FEATURES.md`, commit, PR.
4. **Documentation deliverables** are written **during the wave that needs them**, not up-front. This document is the single canonical strategy doc; per-wave plans expand into execution detail when the wave starts.

After Wave 6, mqConnector is the enterprise integration operating system the department needs: design / govern / observe / explain / remediate, all in one air-gapped pane, with self-hosted AI as connective tissue.
