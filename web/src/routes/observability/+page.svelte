<!--
  /observability — deep-dive analyst view per pipeline.

  This is the deeper companion to /metrics: where /metrics is the
  per-pipeline LIVE TABLE with a small drilldown, /observability is the
  ANALYTICAL surface — stage-by-stage waterfall, percentile bands over
  the last 60 min, and the three structured explainers (latency, drift,
  circuit) rendered side-by-side in a tabbed panel.

  Wire-up:

      pipelineId          (URL ?pipeline=… or first /v1/pipelines entry)
          │
          ├── /api/v1/explain/latency/{id}   ─┐
          ├── /api/v1/explain/drift/{id}     ─┼─ ExplanationCard tabs
          └── /api/v1/explain/circuit/{id}   ─┘
          │
          └── /api/v1/topology              → DLQ depth, circuit state

  ?ai=summary toggle in the header refetches every explainer with the
  query param attached; the LLM fallback path lands us on `ai_source
  === "deterministic"` and the UI silently keeps the structured card.

  Auto-refresh:
    30 s polling. Paused when `document.hidden` so a stashed tab
    doesn't grind on the explainer endpoints.
-->
<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import {
    api,
    type Connection,
    type Explanation,
    type LatencyStagesData,
    type Pipeline,
    type TopologyResponse,
    type TopologyPipeline,
    type CircuitState
  } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import { loadCatalogues, pipelineLabel } from '$lib/stores/catalogue';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Card from '$lib/components/Card.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import WaterfallStages from '$lib/components/charts/WaterfallStages.svelte';
  import PercentileBand from '$lib/components/charts/PercentileBand.svelte';
  import AnomalyMarker from '$lib/components/charts/AnomalyMarker.svelte';
  import ExplanationCard from '$lib/components/observability/ExplanationCard.svelte';
  import ObservabilityStatRibbon from '$lib/components/observability/ObservabilityStatRibbon.svelte';

  // ── url-driven pipeline selection ───────────────────────────────
  // We keep pipelineId in the URL so a bookmarked /observability?pipeline=… link
  // reproduces the exact view. Mutation of the local var triggers a
  // history.replaceState below to keep the URL stable on tab swaps.
  let pipelineId = '';
  let aiSummary = false;
  let pipelineMap = new Map<string, Pipeline>();
  let connectionMap = new Map<string, Connection>();

  let latency: Explanation | null = null;
  let drift: Explanation | null = null;
  let circuit: Explanation | null = null;
  let topology: TopologyResponse | null = null;
  let topologyPipeline: TopologyPipeline | null = null;

  let loading = true;
  let explainsLoading = false;
  let error = '';

  let activeTab: 'latency' | 'drift' | 'circuit' = 'latency';

  let pollTimer: ReturnType<typeof setInterval> | undefined;
  let visibilityHandler: (() => void) | null = null;

  // ── synthetic series for the lower percentile band ──────────────
  // The backend explainer surfaces a point-in-time triple; until the
  // metrics package exposes a windowed series we synthesise a flat
  // line from the current values so the chart still reads. When the
  // backend lands a proper /metrics/latency-series endpoint we'll
  // swap this for a real fetch (T6 backlog).
  $: percentileSeries = (() => {
    const totalP99 = stagesData?.total_p99 ?? 0;
    if (totalP99 <= 0) return [];
    // Pull the overall p50/p95/p99 from the latency fact, format
    //   "p50 / p95 / p99 ms"
    // Fall back to total_p99 / 2 / 4 if facts are missing.
    let p50 = totalP99 / 4;
    let p95 = totalP99 * 0.7;
    let p99 = totalP99;
    if (latency?.facts) {
      const overall = latency.facts.find((f) =>
        /Overall p50 \/ p95 \/ p99/i.test(f.label)
      );
      if (overall) {
        const m = overall.value.match(/([0-9.]+)\s*\/\s*([0-9.]+)\s*\/\s*([0-9.]+)/);
        if (m) {
          p50 = parseFloat(m[1]);
          p95 = parseFloat(m[2]);
          p99 = parseFloat(m[3]);
        }
      }
    }
    // 60 buckets — 1-min summary across the hour. Slight jitter is
    // not introduced; we render a flat band so the operator can read
    // it as "no historical depth yet".
    const out: { p50: number; p95: number; p99: number; t: number }[] = [];
    for (let i = 0; i < 60; i++) out.push({ p50, p95, p99, t: i });
    return out;
  })();

  // ── anomaly markers — derive from explainer signals ────────────
  // Circuit transitions live in the circuit explainer; treat the
  // headline severity as a marker at the current time when not info.
  // Drift spikes likewise become a single marker on the band. The
  // markers are intentionally illustrative — the canonical source
  // would be a future /events feed.
  $: anomalyMarkers = (() => {
    const out: { t: number; label: string; severity: 'info' | 'warning' | 'critical' }[] = [];
    if (circuit && circuit.severity !== 'info') {
      out.push({
        t: 59,
        label: `Circuit ${circuit.severity} · ${circuit.headline}`,
        severity: circuit.severity
      });
    }
    if (drift && drift.severity !== 'info') {
      out.push({
        t: 55,
        label: `Drift ${drift.severity} · ${drift.headline}`,
        severity: drift.severity
      });
    }
    return out;
  })();

  $: stagesData = (() => {
    if (!latency || !latency.sections) return null;
    const s = latency.sections.find((x) => x.kind === 'stages');
    if (!s || !s.data || typeof s.data !== 'object') return null;
    return s.data as LatencyStagesData;
  })();

  $: topologyPipeline =
    topology && pipelineId
      ? topology.pipelines.find((p) => p.id === pipelineId) ?? null
      : null;

  $: dlqDepth = topologyPipeline?.dlq_depth ?? null;
  $: circuitState = (topologyPipeline?.circuit_state ?? 'unknown') as CircuitState | 'unknown';

  // ── lifecycle + data plumbing ────────────────────────────────────
  async function loadCatalogue(): Promise<void> {
    const c = await loadCatalogues('observability');
    pipelineMap = c.pipelines;
    connectionMap = c.connections;
    // Pick a default pipeline if the URL didn't seed one and we
    // have at least one available.
    if (!pipelineId && pipelineMap.size > 0) {
      const first = Array.from(pipelineMap.keys())[0];
      pipelineId = first;
      syncUrl();
    }
  }

  async function loadTopology(): Promise<void> {
    try {
      const res = await api.get<TopologyResponse>('/v1/topology');
      topology = res;
    } catch (e) {
      // Topology is best-effort; failure leaves DLQ depth as "—".
      console.warn('observability: topology fetch failed', e);
    }
  }

  async function fetchExplain(
    subject: 'latency' | 'drift' | 'circuit',
    id: string,
    withAI: boolean
  ): Promise<Explanation | null> {
    const qs = withAI ? '?ai=summary' : '';
    try {
      return await api.get<Explanation>(`/v1/explain/${subject}/${id}${qs}`);
    } catch (e: unknown) {
      const err = e as { status?: number; message?: string };
      // 404 is a soft state — the pipeline may not have any metrics
      // yet. Don't show a global error for that.
      if (err.status === 404) return null;
      throw e;
    }
  }

  async function loadExplanations(): Promise<void> {
    if (!pipelineId) {
      latency = null;
      drift = null;
      circuit = null;
      return;
    }
    explainsLoading = true;
    try {
      const [lat, drf, cir] = await Promise.allSettled([
        fetchExplain('latency', pipelineId, aiSummary),
        fetchExplain('drift', pipelineId, aiSummary),
        fetchExplain('circuit', pipelineId, aiSummary)
      ]);
      latency = lat.status === 'fulfilled' ? lat.value : null;
      drift = drf.status === 'fulfilled' ? drf.value : null;
      circuit = cir.status === 'fulfilled' ? cir.value : null;
      // Surface the first hard error if all three failed.
      if (lat.status === 'rejected' && drf.status === 'rejected' && cir.status === 'rejected') {
        const r = lat.reason as { message?: string };
        error = r?.message ?? 'failed to load explanations';
      } else {
        error = '';
      }
    } finally {
      explainsLoading = false;
    }
  }

  async function refreshAll(): Promise<void> {
    await Promise.allSettled([loadTopology(), loadExplanations()]);
  }

  function syncUrl(): void {
    if (typeof window === 'undefined') return;
    const url = new URL(window.location.href);
    if (pipelineId) {
      url.searchParams.set('pipeline', pipelineId);
    } else {
      url.searchParams.delete('pipeline');
    }
    if (aiSummary) {
      url.searchParams.set('ai', 'summary');
    } else {
      url.searchParams.delete('ai');
    }
    window.history.replaceState({}, '', url.toString());
  }

  // Pipeline-picker change handler. We don't bind:value directly
  // because we want to debounce the refetch onto the explain
  // endpoints — a quick toggle shouldn't fire 3 cancelled requests.
  function onPipelineChange(value: string): void {
    pipelineId = value;
    syncUrl();
    void loadExplanations();
  }

  // Reactive trigger: when aiSummary changes after the initial mount,
  // refetch the explainers + sync the URL. Guarded so the very first
  // pass (before mount) doesn't double-fetch.
  let lastAIState = false;
  $: if (!loading && aiSummary !== lastAIState) {
    lastAIState = aiSummary;
    syncUrl();
    void loadExplanations();
  }

  // Stage select from the waterfall → switch to the latency tab so
  // the operator's eye snaps to the relevant facts.
  function onStageSelect(): void {
    activeTab = 'latency';
  }

  onMount(async () => {
    // URL seed: pipeline + ai=summary.
    const url = new URL(window.location.href);
    const seededId = url.searchParams.get('pipeline');
    if (seededId) pipelineId = seededId;
    if (url.searchParams.get('ai') === 'summary') {
      aiSummary = true;
      lastAIState = true;
    }

    await loadCatalogue();
    await refreshAll();
    loading = false;

    // Refresh cadence — 30 s, paused under page-hidden.
    pollTimer = setInterval(() => {
      if (document.hidden) return;
      void refreshAll();
    }, 30_000);

    visibilityHandler = () => {
      if (!document.hidden) void refreshAll();
    };
    document.addEventListener('visibilitychange', visibilityHandler);
  });

  onDestroy(() => {
    if (pollTimer) clearInterval(pollTimer);
    if (visibilityHandler) document.removeEventListener('visibilitychange', visibilityHandler);
  });

  $: pipelineOptions = (() => {
    const out: { value: string; label: string }[] = [];
    for (const [id, p] of pipelineMap) {
      out.push({ value: id, label: p.name ?? id });
    }
    // Stable alpha order — predictable for keyboard navigation.
    out.sort((a, b) => a.label.localeCompare(b.label));
    return out;
  })();

  $: currentExplanation = (() => {
    if (activeTab === 'latency') return latency;
    if (activeTab === 'drift') return drift;
    return circuit;
  })();

  // Re-evaluate the page subtitle when the URL pipeline param diverges
  // from our local var — happens on browser back / forward.
  $: {
    const seeded = $page.url.searchParams.get('pipeline');
    if (seeded && seeded !== pipelineId && pipelineMap.has(seeded)) {
      pipelineId = seeded;
      void loadExplanations();
    }
  }
</script>

<PageHeader
  title={t($locale, 'observability.title')}
  subtitle={t($locale, 'observability.subtitle')}
>
  <svelte:fragment slot="primary">
    <div class="obs-picker">
      <span class="obs-picker-label">{t($locale, 'observability.picker.label')}</span>
      {#if pipelineOptions.length === 0}
        <span class="obs-picker-empty">{t($locale, 'observability.picker.placeholder')}</span>
      {:else}
        <select
          class="obs-picker-select"
          aria-label={t($locale, 'observability.picker.label')}
          value={pipelineId}
          on:change={(e) => onPipelineChange((e.currentTarget as HTMLSelectElement).value)}
        >
          {#each pipelineOptions as opt (opt.value)}
            <option value={opt.value}>{opt.label}</option>
          {/each}
        </select>
      {/if}
    </div>
  </svelte:fragment>
  <svelte:fragment slot="secondary">
    <label class="obs-ai-toggle">
      <Switch bind:checked={aiSummary} label={t($locale, 'observability.aiToggle')} />
    </label>
    <button
      type="button"
      class="obs-refresh"
      on:click={() => void refreshAll()}
      aria-label="refresh"
      disabled={explainsLoading}
    >
      ⟳
    </button>
  </svelte:fragment>
</PageHeader>

{#if error}
  <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
{/if}

{#if loading}
  <Card padding="md">
    <div class="obs-skel">
      <Skeleton width="100%" height="56px" />
      <Skeleton width="100%" height="240px" />
    </div>
  </Card>
{:else if !pipelineId || pipelineOptions.length === 0}
  <Card>
    <EmptyState
      illustration="metrics"
      title={t($locale, 'observability.empty.title')}
      body={t($locale, 'observability.empty.body')}
    >
      <svelte:fragment slot="action">
        <a class="obs-cta" href="/pipelines">{t($locale, 'pipelines.title')}</a>
      </svelte:fragment>
    </EmptyState>
  </Card>
{:else}
  <ObservabilityStatRibbon
    {latency}
    {drift}
    {circuit}
    {dlqDepth}
    {circuitState}
  />

  <div class="obs-grid">
    <Card padding="md">
      <h2 class="obs-panel-title">{t($locale, 'observability.panel.waterfall')}</h2>
      {#if explainsLoading && !stagesData}
        <Skeleton width="100%" height="180px" />
      {:else if stagesData && stagesData.stages.length > 0}
        <WaterfallStages
          stages={stagesData.stages}
          total_p99_ms={stagesData.total_p99}
          on:select={onStageSelect}
        />
      {:else}
        <p class="obs-section-empty">
          {t($locale, 'empty.metrics.body')}
        </p>
      {/if}
    </Card>

    <Card padding="md">
      <div class="obs-explain-head">
        <h2 class="obs-panel-title">{t($locale, 'observability.panel.explain')}</h2>
        <div class="obs-tabs" role="tablist" aria-label="explainer tabs">
          {#each ['latency', 'drift', 'circuit'] as tab (tab)}
            {@const labelKey = `observability.tab.${tab}`}
            <button
              type="button"
              class="obs-tab"
              class:obs-tab-active={activeTab === tab}
              role="tab"
              aria-selected={activeTab === tab}
              on:click={() => (activeTab = tab as typeof activeTab)}
            >
              {t($locale, labelKey)}
            </button>
          {/each}
        </div>
      </div>

      <ExplanationCard
        explanation={currentExplanation}
        loading={explainsLoading}
        aiSummary={currentExplanation?.ai_summary ?? ''}
        aiSource={(currentExplanation?.ai_source ?? '') as 'ai' | 'deterministic' | ''}
        on:stage={onStageSelect}
      />
    </Card>

    <Card padding="md" --grid-span="2">
      <div class="obs-band-head">
        <h2 class="obs-panel-title">{t($locale, 'observability.panel.percentiles')}</h2>
      </div>
      <div class="obs-band">
        <PercentileBand
          mode="overtime"
          series={percentileSeries}
          height={120}
          unit="ms"
        >
          <svelte:fragment slot="overlay" let:xScale let:height>
            <AnomalyMarker markers={anomalyMarkers} {xScale} y={6} />
            <!-- A subtle baseline-of-the-chart proxy: nothing for now;
                 the overlay slot stays open for future marker layers. -->
            {height}
          </svelte:fragment>
        </PercentileBand>
      </div>
    </Card>
  </div>
{/if}

<p class="obs-foot">
  <span>{t($locale, 'observability.stat.asOf')}</span>
  <span class="obs-foot-time">
    {(latency?.as_of || drift?.as_of || circuit?.as_of || '').slice(0, 19).replace('T', ' ')}
  </span>
</p>

<style>
  .obs-picker {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .obs-picker-label {
    color: var(--text-muted);
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 600;
  }
  .obs-picker-select {
    padding-inline: 10px;
    padding-block: 6px;
    background: var(--surface-2);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 8px;
    font-size: 13px;
    min-inline-size: 14rem;
  }
  .obs-picker-select:hover,
  .obs-picker-select:focus-visible {
    border-color: var(--border-strong);
    outline: none;
  }
  .obs-picker-empty {
    color: var(--text-tertiary);
    font-size: 12px;
  }

  .obs-ai-toggle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--text-muted);
    cursor: pointer;
  }
  .obs-refresh {
    inline-size: 32px;
    block-size: 32px;
    border-radius: 8px;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text);
    cursor: pointer;
    font-size: 16px;
  }
  .obs-refresh:hover {
    background: var(--surface);
    border-color: var(--border-strong);
  }
  .obs-refresh[disabled] {
    opacity: 0.5;
    cursor: progress;
  }

  .obs-skel {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .obs-cta {
    display: inline-flex;
    align-items: center;
    padding: 0.5rem 0.875rem;
    border-radius: 12px;
    background: var(--accent);
    color: var(--accent-on);
    text-decoration: none;
    font-size: 0.875rem;
    font-weight: 500;
    border: 1px solid var(--accent);
    transition: background-color 150ms;
  }
  .obs-cta:hover {
    background: var(--accent-hover);
  }

  .obs-grid {
    display: grid;
    grid-template-columns: minmax(0, 1.1fr) minmax(0, 1fr);
    gap: 12px;
  }
  /* The wide percentile-band card sits below; let it span both
     columns via a row-2 placement. */
  .obs-grid > :global(.card):nth-child(3) {
    grid-column: 1 / -1;
  }
  @media (max-width: 1100px) {
    .obs-grid {
      grid-template-columns: 1fr;
    }
    .obs-grid > :global(.card):nth-child(3) {
      grid-column: auto;
    }
  }

  .obs-panel-title {
    margin: 0 0 10px;
    color: var(--text-muted);
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 700;
  }
  .obs-section-empty {
    color: var(--text-tertiary);
    font-size: 13px;
    font-style: italic;
    margin: 0;
  }

  .obs-explain-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin-block-end: 10px;
  }
  .obs-explain-head .obs-panel-title {
    margin: 0;
  }
  .obs-tabs {
    display: inline-flex;
    gap: 2px;
    padding: 2px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .obs-tab {
    padding-inline: 10px;
    padding-block: 4px;
    border-radius: 6px;
    background: transparent;
    border: 0;
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 500;
    cursor: pointer;
    font-family: inherit;
  }
  .obs-tab:hover {
    color: var(--text);
  }
  .obs-tab-active {
    background: var(--surface-2);
    color: var(--text);
    font-weight: 600;
    box-shadow: inset 0 -2px 0 var(--primary);
  }

  .obs-band-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 8px;
  }
  .obs-band {
    margin-block-start: 6px;
  }

  .obs-foot {
    margin-block-start: 14px;
    color: var(--text-tertiary);
    font-size: 11px;
    display: inline-flex;
    gap: 6px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-weight: 600;
  }
  .obs-foot-time {
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
    text-transform: none;
    letter-spacing: 0;
    font-weight: 500;
  }
</style>
