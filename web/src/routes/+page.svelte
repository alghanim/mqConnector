<!--
  / — Operations overview dashboard.

  Single pane that surfaces every persisted data stream the operator
  needs at a glance:

    /api/health           overall status + per-connection state
    /api/metrics          cumulative counters per pipeline
    /api/v1/dlq           failed-message queue (total + latest 5)
    /api/v1/audit         admin action log (latest 10)

  All four endpoints are polled in parallel every 5 s. Throughput is
  derived client-side: the backend only exposes cumulative message
  counters, so we keep up to 13 successive /metrics snapshots per
  pipeline (= 12 deltas = a 60 s rolling window) and compute the
  per-interval delta locally. This avoids any backend schema change
  for short-window throughput.

  Runtime logs (slog) write to stdout and are not persisted in
  SQLite. They are intentionally NOT a card on this dashboard — the
  Live logs section points the operator at journalctl / docker logs
  instead. Persisting logs is its own feature, out of scope here.
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import {
    api,
    type AuditDiff,
    type AuditEntry,
    type DLQEntry,
    type Health,
    type PipelineMetric
  } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Sparkline from '$lib/components/Sparkline.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatChip from '$lib/components/StatChip.svelte';
  import SystemPulse from '$lib/components/SystemPulse.svelte';
  import {
    metrics as liveMetrics,
    dlqTotal as liveDlqTotal,
    health as liveHealth,
    liveMode as liveModeStore
  } from '$lib/stores/live';
  import { ArrowUpRight } from 'lucide-svelte';

  let health: Health | null = null;
  let pipelines: PipelineMetric[] = [];
  let dlqTotal = 0;
  let dlqLatest: DLQEntry[] = [];
  let audit: AuditEntry[] = [];
  let error = '';
  let lastRefreshed = '';
  let interval: ReturnType<typeof setInterval> | undefined;
  let metricsFallbackTimer: ReturnType<typeof setInterval> | undefined;

  // Audit diff drill-down — opens inline below the row. PUT rows are
  // the only ones with a recorded diff; for other verbs we just show
  // the request-id so the operator can grep logs.
  let openDiff: string | null = null;
  let diffCache = new Map<string, AuditDiff | null>();
  let diffLoading: string | null = null;

  async function toggleDiff(id: string) {
    if (openDiff === id) {
      openDiff = null;
      return;
    }
    openDiff = id;
    if (diffCache.has(id)) return;
    diffLoading = id;
    try {
      const d = await api.get<AuditDiff>(`/v1/audit/${id}/diff`);
      diffCache.set(id, d);
    } catch {
      // 404 → no diff recorded for this row (non-PUT or pre-Phase-19b
      // row); cache the null so we don't refetch.
      diffCache.set(id, null);
    } finally {
      diffLoading = null;
      diffCache = diffCache; // trigger reactivity
    }
  }

  function prettyJSON(s: string): string {
    if (!s) return '';
    try {
      return JSON.stringify(JSON.parse(s), null, 2);
    } catch {
      return s;
    }
  }

  // Bind to the shared live stream. The layout already opened it; this
  // page just subscribes to the metrics + health + dlqTotal stores and
  // pushes each sample into the sparkline buffer.
  $: health = $liveHealth;
  $: pipelines = $liveMetrics?.pipelines ?? pipelines;
  $: dlqTotal = $liveDlqTotal;
  $: liveMode = $liveModeStore;

  // Side effect: every time a new metrics snapshot lands, record it for
  // the sparkline window. Cap at the snapshot's receivedAt to avoid
  // recording the same frame twice on store rebroadcast.
  let lastMetricsAt = 0;
  $: if ($liveMetrics && $liveMetrics.receivedAt !== lastMetricsAt) {
    lastMetricsAt = $liveMetrics.receivedAt;
    recordSamples($liveMetrics.pipelines);
    lastRefreshed = new Date($liveMetrics.receivedAt).toLocaleTimeString();
    error = '';
  }

  // History per pipeline of cumulative counters with timestamps.
  // 13 snapshots = 12 deltas = 60 s at the 5 s polling cadence.
  const MAX_SAMPLES = 13;
  type Snapshot = { ts: number; processed: number; failed: number };
  let history = new Map<string, Snapshot[]>();
  let aggregateDeltas: number[] = [];
  // Triggers Svelte reactivity when history is mutated in place.
  let historyVersion = 0;

  function recordSamples(metrics: PipelineMetric[]): void {
    const ts = Date.now();
    const next = new Map(history);
    const liveIds = new Set(metrics.map((m) => m.pipeline_id));
    for (const id of Array.from(next.keys())) {
      if (!liveIds.has(id)) next.delete(id);
    }
    for (const m of metrics) {
      const snaps = next.get(m.pipeline_id) ?? [];
      snaps.push({ ts, processed: m.messages_processed, failed: m.messages_failed });
      while (snaps.length > MAX_SAMPLES) snaps.shift();
      next.set(m.pipeline_id, snaps);
    }
    history = next;
    historyVersion++;

    // Aggregate total throughput per interval — sum of per-pipeline deltas.
    // Use the longest available run; pipelines with shorter history just
    // contribute zeros at the front of the window.
    let maxLen = 0;
    for (const snaps of history.values()) if (snaps.length > maxLen) maxLen = snaps.length;
    if (maxLen < 2) {
      aggregateDeltas = [];
      return;
    }
    const out: number[] = new Array(maxLen - 1).fill(0);
    for (const snaps of history.values()) {
      const offset = maxLen - snaps.length;
      for (let i = 1; i < snaps.length; i++) {
        const d = snaps[i].processed - snaps[i - 1].processed;
        // Clamp counter resets (pipeline restart) so they don't show up
        // as huge negative spikes.
        out[offset + i - 1] += Math.max(0, d);
      }
    }
    aggregateDeltas = out;
  }

  function pipelineDeltas(id: string, _v: number): number[] {
    const snaps = history.get(id) ?? [];
    if (snaps.length < 2) return [];
    const out: number[] = [];
    for (let i = 1; i < snaps.length; i++) {
      out.push(Math.max(0, snaps[i].processed - snaps[i - 1].processed));
    }
    return out;
  }

  // Slow-cadence fetches for the two surfaces SSE doesn't push: the
  // latest DLQ items panel and the recent admin activity feed. The
  // dlq_total badge is fed by SSE; these are the *list* views.
  async function refreshSlowSurfaces(): Promise<void> {
    const [dlq, aud] = await Promise.allSettled([
      api.get<{ total: number; items: DLQEntry[] }>('/v1/dlq?page=1&per_page=5'),
      api.get<{ items: AuditEntry[] }>('/v1/audit?page=1&per_page=10')
    ]);
    if (dlq.status === 'fulfilled') {
      dlqLatest = dlq.value.items ?? [];
    }
    if (aud.status === 'fulfilled') audit = aud.value.items ?? [];
  }

  // Engaged only when SSE has dropped. Pulls /health + /metrics so the
  // sparkline window doesn't go flat.
  async function pollLiveFallback(): Promise<void> {
    const [h, m] = await Promise.allSettled([
      api.get<Health>('/health'),
      api.get<{ uptime: string; pipelines: Record<string, PipelineMetric> }>('/metrics')
    ]);
    if (h.status === 'fulfilled') liveHealth.set(h.value);
    if (m.status === 'fulfilled') {
      const pipes = Object.values(m.value.pipelines || {});
      liveMetrics.set({
        uptime: m.value.uptime,
        pipelines: pipes,
        active: pipes.length,
        receivedAt: Date.now()
      });
    }
  }

  onMount(() => {
    refreshSlowSurfaces();
    interval = setInterval(refreshSlowSurfaces, 15_000);
  });
  onDestroy(() => {
    if (interval) clearInterval(interval);
    if (metricsFallbackTimer) clearInterval(metricsFallbackTimer);
  });

  // Polling fallback for the live-data surfaces. Engages only while
  // SSE is down so we don't double-fetch in steady state.
  $: if (!$liveModeStore) {
    if (!metricsFallbackTimer) {
      pollLiveFallback();
      metricsFallbackTimer = setInterval(pollLiveFallback, 5_000);
    }
  } else if (metricsFallbackTimer) {
    clearInterval(metricsFallbackTimer);
    metricsFallbackTimer = undefined;
  }

  // ── Derived KPIs ────────────────────────────────────────────────
  $: totalProcessed = pipelines.reduce((s, m) => s + m.messages_processed, 0);
  $: totalFailed = pipelines.reduce((s, m) => s + m.messages_failed, 0);
  $: totalPipelines = pipelines.length;
  $: activePipelines =
    health?.active_pipelines ?? pipelines.filter((p) => p.status === 'connected').length;
  $: errorPipelines = pipelines.filter((p) => p.last_error);

  // ── Formatters ──────────────────────────────────────────────────
  function variantFor(s: string): 'success' | 'warning' | 'danger' | 'neutral' {
    if (s === 'healthy' || s === 'connected') return 'success';
    if (s === 'degraded') return 'warning';
    if (s === 'unhealthy' || s === 'error') return 'danger';
    return 'neutral';
  }
  function httpVariant(code: number): 'success' | 'warning' | 'danger' | 'neutral' {
    if (code === 0) return 'neutral';
    if (code >= 200 && code < 300) return 'success';
    if (code >= 400 && code < 500) return 'warning';
    if (code >= 500) return 'danger';
    return 'neutral';
  }
  function fmtNumber(n: number): string {
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
    if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
    return n.toLocaleString();
  }

  // ── Throughput chart geometry ───────────────────────────────────
  // Inline SVG, sized off the container via viewBox. Keeps the page
  // dependency-free; Chart.js would have meant a new npm dep + the
  // CDN path the embedded binary can't reach.
  const CHART_W = 640;
  const CHART_H = 180;
  const CHART_PAD = { top: 12, end: 12, bottom: 22, start: 36 };
  $: chartMax = aggregateDeltas.length > 0 ? Math.max(1, ...aggregateDeltas) : 1;
  $: chartPoints = (() => {
    if (aggregateDeltas.length < 2) return '';
    const innerW = CHART_W - CHART_PAD.start - CHART_PAD.end;
    const innerH = CHART_H - CHART_PAD.top - CHART_PAD.bottom;
    return aggregateDeltas
      .map((v, i) => {
        const x =
          CHART_PAD.start + (i * innerW) / (aggregateDeltas.length - 1);
        const y = CHART_PAD.top + innerH - (v / chartMax) * innerH;
        return `${x.toFixed(2)},${y.toFixed(2)}`;
      })
      .join(' ');
  })();
  $: chartArea = (() => {
    if (aggregateDeltas.length < 2 || !chartPoints) return '';
    const innerH = CHART_H - CHART_PAD.top - CHART_PAD.bottom;
    const innerW = CHART_W - CHART_PAD.start - CHART_PAD.end;
    const baseY = CHART_PAD.top + innerH;
    return `${CHART_PAD.start},${baseY} ${chartPoints} ${CHART_PAD.start + innerW},${baseY}`;
  })();
  $: chartTicks = [0, 0.25, 0.5, 0.75, 1].map((p) => ({
    y: CHART_PAD.top + (1 - p) * (CHART_H - CHART_PAD.top - CHART_PAD.bottom),
    label: fmtNumber(Math.round(chartMax * p))
  }));
</script>

<div class="space-y-6 max-w-6xl">
  <PageHeader title={t($locale, 'dash.title')} subtitle={t($locale, 'dash.pageSubtitle')}>
    <svelte:fragment slot="stats">
      {#if health}
        <StatChip
          label={t($locale, 'health.label')}
          value={health.status}
          tone={health.status === 'healthy' ? 'success' : health.status === 'degraded' ? 'warning' : 'danger'}
        />
        <StatChip label={t($locale, 'metrics.uptime')} value={health.uptime} />
        <StatChip label="v" value={health.version} />
      {/if}
      {#if lastRefreshed}
        <StatChip
          label={liveMode ? t($locale, 'dash.live') : t($locale, 'dash.refreshed')}
          value={lastRefreshed}
          tone={liveMode ? 'success' : 'default'}
        />
      {/if}
    </svelte:fragment>
  </PageHeader>

  {#if error}
    <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
  {/if}

  <!-- ─── Hero pulse ─────────────────────────────────────────────── -->
  <SystemPulse
    status={health?.status ?? 'unknown'}
    activePipelines={activePipelines}
    totalPipelines={totalPipelines}
    deltas={aggregateDeltas}
    failedTotal={totalFailed}
    processedTotal={totalProcessed}
  />

  <!-- ─── KPI row ───────────────────────────────────────────────── -->
  <section aria-label={t($locale, 'dash.title')} class="grid grid-cols-2 lg:grid-cols-4 gap-4">
    <Card strip>
      <p class="section-heading">{t($locale, 'dash.kpi.processed')}</p>
      <p class="dash-kpi">{fmtNumber(totalProcessed)}</p>
    </Card>
    <Card>
      <p class="section-heading">{t($locale, 'dash.kpi.failed')}</p>
      <p class="dash-kpi" style:color={totalFailed > 0 ? 'var(--danger)' : 'var(--text)'}>
        {fmtNumber(totalFailed)}
      </p>
    </Card>
    <Card>
      <p class="section-heading">{t($locale, 'dash.kpi.activePipelines')}</p>
      <p class="dash-kpi">
        {activePipelines}<span class="dash-kpi-sub"> / {totalPipelines}</span>
      </p>
    </Card>
    <Card>
      <p class="section-heading">{t($locale, 'dash.kpi.dlq')}</p>
      <p class="dash-kpi">
        <a href="/dlq" class="dash-kpi-link">{fmtNumber(dlqTotal)}</a>
      </p>
    </Card>
  </section>

  <!-- ─── Throughput chart + Connection health ──────────────────── -->
  <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
    <div class="lg:col-span-2">
      <Card>
        <div class="flex items-baseline justify-between flex-wrap gap-2 mb-2">
          <p class="section-heading">{t($locale, 'dash.throughput.title')}</p>
          <span class="text-caption">{t($locale, 'dash.throughput.subtitle')}</span>
        </div>
        {#if aggregateDeltas.length < 2}
          <p class="dash-empty">{t($locale, 'dash.throughput.empty')}</p>
        {:else}
          <svg
            class="dash-chart"
            viewBox="0 0 {CHART_W} {CHART_H}"
            role="img"
            aria-label="{t($locale, 'dash.throughput.title')} — {aggregateDeltas[aggregateDeltas.length - 1]} {t(
              $locale,
              'dash.kpi.processed'
            )}"
            preserveAspectRatio="none"
          >
            <!-- Horizontal grid lines + y-axis labels -->
            {#each chartTicks as tick}
              <line
                x1={CHART_PAD.start}
                x2={CHART_W - CHART_PAD.end}
                y1={tick.y}
                y2={tick.y}
                class="dash-chart-grid"
              />
              <text x={CHART_PAD.start - 6} y={tick.y + 4} class="dash-chart-tick" text-anchor="end">
                {tick.label}
              </text>
            {/each}
            <!-- Area fill -->
            <polygon points={chartArea} class="dash-chart-area" />
            <!-- Line -->
            <polyline points={chartPoints} class="dash-chart-line" fill="none" />
          </svg>
        {/if}
      </Card>
    </div>
    <div>
      <Card>
        <div class="flex items-baseline justify-between flex-wrap gap-2 mb-3">
          <p class="section-heading">{t($locale, 'dash.connections.title')}</p>
          <a href="/metrics" class="link text-caption">{t($locale, 'dash.pipelines.viewAll')}</a>
        </div>
        {#if !health?.connections || health.connections.length === 0}
          <p class="dash-empty">{t($locale, 'dash.connections.empty')}</p>
        {:else}
          <ul class="dash-conns">
            {#each health.connections as c (c.pipeline_id)}
              <li class="dash-conn-row">
                <Badge variant={variantFor(c.status)}>{c.status}</Badge>
                <div class="dash-conn-body">
                  <p class="dash-conn-name">{c.pipeline_id}</p>
                  <p class="dash-conn-flow">{c.source_queue} → {c.dest_queue}</p>
                </div>
              </li>
            {/each}
          </ul>
        {/if}
      </Card>
    </div>
  </div>

  <!-- ─── Pipeline status grid with sparklines ──────────────────── -->
  <Card>
    <div class="flex items-baseline justify-between mb-3">
      <p class="section-heading">{t($locale, 'dash.pipelines.title')}</p>
      <a href="/metrics" class="link text-caption">{t($locale, 'dash.pipelines.viewAll')}</a>
    </div>
    {#if pipelines.length === 0}
      <p class="dash-empty">{t($locale, 'dash.pipelines.empty')}</p>
    {:else}
      <div class="dash-pipelines">
        {#each pipelines as p (p.pipeline_id)}
          <a
            class="dash-pipeline-card"
            href="/metrics"
            aria-label="{p.pipeline_id} — {t($locale, 'dash.pipelines.open')}"
          >
            <div class="dash-pipeline-head">
              <div class="dash-pipeline-id">
                <p class="dash-pipeline-name">{p.pipeline_id}</p>
                <p class="dash-pipeline-flow">{p.source_queue} → {p.dest_queue}</p>
              </div>
              <Badge variant={variantFor(p.status)}>{p.status}</Badge>
            </div>
            <div class="dash-pipeline-stats">
              <div>
                <p class="dash-stat-label">{t($locale, 'metrics.processed')}</p>
                <p class="dash-stat-value">{fmtNumber(p.messages_processed)}</p>
              </div>
              <div>
                <p class="dash-stat-label">{t($locale, 'metrics.failed')}</p>
                <p
                  class="dash-stat-value"
                  style:color={p.messages_failed > 0 ? 'var(--danger)' : 'var(--text)'}
                >
                  {p.messages_failed}
                </p>
              </div>
              <div>
                <p class="dash-stat-label">{t($locale, 'metrics.avgLatency')}</p>
                <p class="dash-stat-value">{p.avg_latency_ms.toFixed(1)} ms</p>
              </div>
            </div>
            <div class="dash-pipeline-trend">
              <span class="dash-stat-label">{t($locale, 'dash.pipelines.trendLabel')}</span>
              <Sparkline
                data={pipelineDeltas(p.pipeline_id, historyVersion)}
                variant={p.messages_failed > 0 ? 'warning' : 'secondary'}
                label="{t($locale, 'dash.pipelines.trendLabel')} {p.pipeline_id}"
              />
            </div>
            <span class="dash-pipeline-go" aria-hidden="true">
              <ArrowUpRight size={14} strokeWidth={1.75} />
            </span>
          </a>
        {/each}
      </div>
    {/if}
  </Card>

  <!-- ─── Recent errors strip ───────────────────────────────────── -->
  {#if errorPipelines.length > 0}
    <Card>
      <p class="section-heading mb-3">{t($locale, 'dash.errors.title')}</p>
      <div class="space-y-3">
        {#each errorPipelines as p (p.pipeline_id)}
          <div class="dash-error-row">
            <Badge variant="danger">{p.pipeline_id}</Badge>
            <pre class="dash-error-msg">{p.last_error}</pre>
          </div>
        {/each}
      </div>
    </Card>
  {/if}

  <!-- ─── Recent activity (audit) + Latest DLQ ──────────────────── -->
  <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
    <Card>
      <p class="section-heading mb-3">{t($locale, 'dash.events.title')}</p>
      {#if audit.length === 0}
        <p class="dash-empty">{t($locale, 'dash.events.empty')}</p>
      {:else}
        <ul class="dash-events">
          {#each audit as a (a.id)}
            <li class="dash-event-item">
              <Badge variant={httpVariant(a.status)}>{a.status || '—'}</Badge>
              <div class="dash-event-body">
                <p class="dash-event-line">
                  <span class="dash-event-actor">{a.actor || '—'}</span>
                  <span class="dash-event-action">{a.action}</span>
                  <code class="dash-event-resource">{a.resource}</code>
                  {#if a.action === 'PUT'}
                    <button
                      type="button"
                      class="dash-event-diff-btn"
                      on:click={() => toggleDiff(a.id)}
                      aria-expanded={openDiff === a.id}
                    >
                      {openDiff === a.id ? 'hide diff' : 'view diff'}
                    </button>
                  {/if}
                </p>
                <time class="dash-event-time" datetime={a.at}>{a.at}</time>
                {#if openDiff === a.id}
                  <div class="dash-event-diff">
                    {#if diffLoading === a.id}
                      <p class="dash-empty">{t($locale, 'common.loading')}</p>
                    {:else if diffCache.get(a.id)}
                      <p class="dash-event-diff-label">after</p>
                      <pre class="dash-event-diff-pre">{prettyJSON(diffCache.get(a.id)?.after ?? '')}</pre>
                    {:else}
                      <p class="dash-empty">No diff recorded for this row.</p>
                    {/if}
                  </div>
                {/if}
              </div>
            </li>
          {/each}
        </ul>
      {/if}
    </Card>

    <Card>
      <div class="flex items-baseline justify-between mb-3">
        <p class="section-heading">{t($locale, 'dash.dlq.title')}</p>
        <a href="/dlq" class="link text-caption">{t($locale, 'dash.dlq.viewAll')}</a>
      </div>
      {#if dlqLatest.length === 0}
        <p class="dash-empty">{t($locale, 'dash.dlq.empty')}</p>
      {:else}
        <ul class="dash-dlq">
          {#each dlqLatest as d (d.id)}
            <li class="dash-dlq-item">
              <div class="dash-dlq-body">
                <p class="dash-dlq-pipeline">{d.pipeline_id || '—'}</p>
                <p class="dash-dlq-reason">{d.error_reason}</p>
              </div>
              <time class="dash-event-time" datetime={d.created_at}>{d.created_at}</time>
            </li>
          {/each}
        </ul>
      {/if}
    </Card>
  </div>

  <!-- ─── Logs note ─────────────────────────────────────────────── -->
  <Card>
    <p class="section-heading">{t($locale, 'dash.logs.title')}</p>
    <p class="text-body-2 mt-2">{t($locale, 'dash.logs.note')}</p>
  </Card>
</div>

<style>
  /* (header meta is now rendered by PageHeader+StatChip) */

  /* ─── KPI cards ───────────────────────────────────────────────── */
  .dash-kpi {
    margin-top: 6px;
    color: var(--text);
    font-size: 28px;
    font-weight: 700;
    line-height: 1.1;
    letter-spacing: -0.01em;
  }
  .dash-kpi-sub {
    color: var(--text-muted);
    font-size: 16px;
    font-weight: 500;
  }
  .dash-kpi-link {
    color: inherit;
    text-decoration: none;
    border-block-end: 1px dashed var(--border-strong);
  }
  .dash-kpi-link:hover {
    color: var(--primary);
    border-block-end-color: var(--primary);
  }

  /* ─── Throughput chart ────────────────────────────────────────── */
  .dash-chart {
    inline-size: 100%;
    block-size: auto;
    aspect-ratio: 640 / 180;
    display: block;
  }
  .dash-chart-grid {
    stroke: var(--divider);
    stroke-width: 1;
    stroke-dasharray: 2 3;
    opacity: 0.6;
  }
  .dash-chart-tick {
    fill: var(--text-tertiary);
    font-size: 10px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }
  .dash-chart-line {
    stroke: var(--secondary);
    stroke-width: 2;
    stroke-linejoin: round;
    stroke-linecap: round;
  }
  :global([data-theme='light']) .dash-chart-line {
    stroke: var(--primary);
  }
  .dash-chart-area {
    fill: color-mix(in srgb, var(--secondary) 18%, transparent);
    stroke: none;
  }
  :global([data-theme='light']) .dash-chart-area {
    fill: color-mix(in srgb, var(--primary) 14%, transparent);
  }

  /* ─── Connection list ─────────────────────────────────────────── */
  .dash-conns {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .dash-conn-row {
    display: flex;
    align-items: flex-start;
    gap: 10px;
  }
  .dash-conn-body {
    min-inline-size: 0;
    flex: 1;
  }
  .dash-conn-name {
    color: var(--text);
    font-size: 13px;
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dash-conn-flow {
    color: var(--text-muted);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* ─── Pipeline grid ───────────────────────────────────────────── */
  .dash-pipelines {
    display: grid;
    grid-template-columns: 1fr;
    gap: 12px;
  }
  @media (min-width: 720px) {
    .dash-pipelines {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  @media (min-width: 1100px) {
    .dash-pipelines {
      grid-template-columns: repeat(3, minmax(0, 1fr));
    }
  }
  .dash-pipeline-card {
    position: relative;
    display: block;
    border: 1px solid var(--card-border);
    border-radius: 12px;
    padding: 14px;
    background: var(--surface);
    color: inherit;
    text-decoration: none;
    transition:
      border-color 150ms,
      transform 150ms,
      box-shadow 150ms;
    cursor: pointer;
  }
  .dash-pipeline-card:hover,
  .dash-pipeline-card:focus-visible {
    border-color: var(--border-strong);
    box-shadow: 0 6px 16px rgba(0, 0, 0, 0.18);
  }
  :global([data-theme='light']) .dash-pipeline-card:hover,
  :global([data-theme='light']) .dash-pipeline-card:focus-visible {
    box-shadow: 0 6px 16px rgba(0, 0, 0, 0.06);
  }
  .dash-pipeline-card:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }
  .dash-pipeline-go {
    position: absolute;
    inset-block-start: 12px;
    inset-inline-end: 12px;
    color: var(--text-tertiary);
    opacity: 0;
    transition: opacity 150ms;
  }
  .dash-pipeline-card:hover .dash-pipeline-go,
  .dash-pipeline-card:focus-visible .dash-pipeline-go {
    opacity: 1;
  }
  .dash-pipeline-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 8px;
    margin-block-end: 12px;
  }
  .dash-pipeline-id {
    min-inline-size: 0;
    flex: 1;
  }
  .dash-pipeline-name {
    color: var(--text);
    font-size: 14px;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dash-pipeline-flow {
    color: var(--text-muted);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dash-pipeline-stats {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 10px;
    margin-block-end: 10px;
  }
  .dash-stat-label {
    color: var(--text-tertiary);
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    margin-block-end: 2px;
  }
  .dash-stat-value {
    color: var(--text);
    font-size: 14px;
    font-weight: 600;
  }
  .dash-pipeline-trend {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding-block-start: 10px;
    border-block-start: 1px solid var(--divider);
  }

  /* ─── Events (audit) ──────────────────────────────────────────── */
  .dash-events {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .dash-event-item {
    display: flex;
    align-items: flex-start;
    gap: 10px;
  }
  .dash-event-body {
    min-inline-size: 0;
    flex: 1;
  }
  .dash-event-line {
    color: var(--text);
    font-size: 13px;
    line-height: 1.4;
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 6px;
    margin-block-end: 2px;
  }
  .dash-event-actor {
    font-weight: 600;
  }
  .dash-event-action {
    color: var(--text-muted);
    font-size: 11px;
    font-weight: 500;
    letter-spacing: 0.04em;
    padding: 1px 6px;
    border: 1px solid var(--divider);
    border-radius: 6px;
  }
  .dash-event-resource {
    color: var(--text-muted);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-inline-size: 0;
    flex: 1;
  }
  .dash-event-time {
    color: var(--text-tertiary);
    font-size: 11px;
  }
  /* "view diff" toggle. Tone-neutral — the diff itself does the work
     of showing impact. The button itself is a quiet affordance. */
  .dash-event-diff-btn {
    appearance: none;
    background: transparent;
    border: 1px dashed var(--divider);
    color: var(--text-muted);
    font-size: 10px;
    padding: 1px 6px;
    border-radius: 6px;
    cursor: pointer;
    margin-inline-start: 6px;
  }
  .dash-event-diff-btn:hover {
    color: var(--text);
    border-color: var(--text-tertiary);
  }
  .dash-event-diff {
    margin-block-start: 6px;
    background: var(--surface-2);
    border: 1px solid var(--divider);
    border-radius: 8px;
    padding: 8px 10px;
  }
  .dash-event-diff-label {
    color: var(--text-tertiary);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    margin-block-end: 4px;
  }
  .dash-event-diff-pre {
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 11px;
    line-height: 1.45;
    margin: 0;
    white-space: pre-wrap;
    word-break: break-word;
    max-block-size: 240px;
    overflow-y: auto;
  }

  /* ─── DLQ snippets ────────────────────────────────────────────── */
  .dash-dlq {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .dash-dlq-item {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    padding-block-end: 10px;
    border-block-end: 1px solid var(--divider-subtle);
  }
  .dash-dlq-item:last-child {
    padding-block-end: 0;
    border-block-end: none;
  }
  .dash-dlq-body {
    min-inline-size: 0;
    flex: 1;
  }
  .dash-dlq-pipeline {
    color: var(--text);
    font-size: 13px;
    font-weight: 500;
  }
  .dash-dlq-reason {
    color: var(--danger);
    font-size: 12px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    margin-block-start: 2px;
  }

  /* ─── Error strip ─────────────────────────────────────────────── */
  .dash-error-row {
    display: flex;
    align-items: flex-start;
    gap: 10px;
  }
  .dash-error-msg {
    color: var(--danger);
    background: var(--danger-bg);
    border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent);
    border-radius: 10px;
    padding: 8px 10px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 1.45;
    white-space: pre-wrap;
    word-break: break-word;
    flex: 1;
    min-inline-size: 0;
  }

  /* ─── Empty states ────────────────────────────────────────────── */
  .dash-empty {
    color: var(--text-muted);
    font-size: 13px;
    padding: 8px 0;
  }
</style>
