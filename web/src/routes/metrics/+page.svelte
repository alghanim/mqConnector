<!--
  /metrics — pipeline throughput dashboard.

  Anatomy:
    PageHeader        title + subtitle + uptime chip + refresh state
    KPI band          totals across all pipelines (processed, failed,
                      bytes, avg latency)
    Live table        per pipeline: status dot, resolved pipeline NAME,
                      broker-iconed source/destination flow, processed,
                      failed (ratio), avg latency, trend sparkline.
                      Click a row → in-place drilldown panel below it
                      with bigger flow, throughput chart, latency pills,
                      last-error block, and quick actions.

  Naming + broker glyphs come from a best-effort fetch of
  /api/v1/pipelines + /api/v1/connections (same pattern the overview
  uses — see /+page.svelte commit 8bb6d29). The live /metrics stream is
  keyed by pipeline_id only, so we resolve through Map<id, Pipeline> and
  Map<id, Connection> maps refreshed every 15 s. Failure of either fetch
  degrades to the raw UUID / topic strings — the page never goes blank.
-->
<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import {
    api,
    type Connection,
    type Pipeline,
    type PipelineMetric
  } from '$lib/api';
  import { metrics as liveMetrics } from '$lib/stores/live';
  import { locale, t } from '$lib/stores/locale';
  import {
    loadCatalogues,
    pipelineLabel,
    endpointType,
    endpointName
  } from '$lib/stores/catalogue';
  import Card from '$lib/components/Card.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatChip from '$lib/components/StatChip.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Sparkline from '$lib/components/Sparkline.svelte';
  import ConnectionTypeIcon from '$lib/components/ConnectionTypeIcon.svelte';
  import {
    Activity,
    AlertOctagon,
    ArrowRight,
    Clock,
    ArrowUpDown,
    ArrowUp,
    ArrowDown,
    ChevronDown,
    ChevronRight,
    ExternalLink,
    Inbox,
    Edit3
  } from 'lucide-svelte';

  let uptime = '';
  let pipelines: PipelineMetric[] = [];
  let error = '';
  let loading = true;
  let interval: ReturnType<typeof setInterval> | undefined;
  let catalogueInterval: ReturnType<typeof setInterval> | undefined;

  // Sparkline window per pipeline. Same windowing logic as the overview
  // page — 13 snapshots = 12 deltas = 60 s at the 5 s polling cadence.
  const MAX_SAMPLES = 13;
  type Snapshot = { processed: number; failed: number };
  let history = new Map<string, Snapshot[]>();
  let historyVersion = 0;

  // Pipeline + Connection catalogues for friendly-name + broker-glyph
  // resolution. Best-effort: failure leaves the maps empty and the UI
  // falls back to the raw UUID / topic strings on the metric record.
  let pipelineMap = new Map<string, Pipeline>();
  let connectionMap = new Map<string, Connection>();

  // ── Catalogue refresh ────────────────────────────────────────────
  // Pulls /v1/pipelines + /v1/connections in parallel via the shared
  // catalogue helper; both endpoints are best-effort and failures
  // degrade the UI to UUIDs / topic strings rather than blocking the
  // live table. See $lib/stores/catalogue for the helper itself.
  async function refreshCatalogues(): Promise<void> {
    const c = await loadCatalogues('metrics');
    pipelineMap = c.pipelines;
    connectionMap = c.connections;
  }

  type SortKey =
    | 'pipeline_id'
    | 'status'
    | 'messages_processed'
    | 'messages_failed'
    | 'fail_rate'
    | 'bytes_processed'
    | 'avg_latency_ms'
    | 'last_message_time';
  let sortKey: SortKey = 'messages_processed';
  let sortDir: 'asc' | 'desc' = 'desc';

  function setSort(k: SortKey) {
    if (sortKey === k) {
      sortDir = sortDir === 'asc' ? 'desc' : 'asc';
    } else {
      sortKey = k;
      sortDir = k === 'pipeline_id' || k === 'status' || k === 'last_message_time' ? 'asc' : 'desc';
    }
  }

  function sortValue(m: PipelineMetric, k: SortKey): number | string {
    if (k === 'fail_rate') {
      const total = m.messages_processed + m.messages_failed;
      return total > 0 ? m.messages_failed / total : 0;
    }
    if (k === 'pipeline_id') {
      // Sort by resolved name when available so the alphabetisation
      // matches what the operator actually sees in the column.
      return pipelineLabel(m.pipeline_id, pipelineMap).toLowerCase();
    }
    const v = (m as unknown as Record<string, unknown>)[k];
    if (typeof v === 'number') return v;
    return (v as string) || '';
  }

  // ── Status tone resolution ───────────────────────────────────────
  // A pipeline is "healthy" only when connected AND failure rate is
  // ≤0%. "warning" tone covers any failure rate >0 but ≤5%, plus the
  // non-error transition states (reconnecting, paused, idle). >5%
  // failure or an explicit error state turns the dot red.
  function statusTone(m: PipelineMetric): 'success' | 'warning' | 'danger' | 'neutral' {
    if (m.last_error || m.status === 'error') return 'danger';
    const total = m.messages_processed + m.messages_failed;
    const rate = total > 0 ? m.messages_failed / total : 0;
    if (rate > 0.05) return 'danger';
    if (rate > 0) return 'warning';
    if (m.status === 'connected') return 'success';
    if (m.status === 'idle' || !m.status) return 'neutral';
    return 'warning';
  }

  function statusLabel(m: PipelineMetric, lc: 'en' | 'ar'): string {
    const tone = statusTone(m);
    if (tone === 'success') return t(lc, 'metrics.status.healthy');
    if (tone === 'warning') return t(lc, 'metrics.status.warning');
    if (tone === 'danger') return t(lc, 'metrics.status.error');
    return t(lc, 'metrics.status.idle');
  }

  // Sparkline tone per pipeline status. Mirrors the dot:
  //   green when no failures, amber when >0%, red when >5%.
  function sparklineVariant(
    m: PipelineMetric
  ): 'success' | 'warning' | 'danger' | 'secondary' {
    const tone = statusTone(m);
    if (tone === 'danger') return 'danger';
    if (tone === 'warning') return 'warning';
    if (tone === 'success') return 'success';
    return 'secondary';
  }

  async function refresh() {
    try {
      const res = await api.get<{
        uptime: string;
        pipelines: Record<string, PipelineMetric>;
      }>('/metrics');
      uptime = res.uptime;
      pipelines = Object.values(res.pipelines || {});

      const next = new Map(history);
      const liveIds = new Set(pipelines.map((p) => p.pipeline_id));
      for (const id of Array.from(next.keys())) {
        if (!liveIds.has(id)) next.delete(id);
      }
      for (const m of pipelines) {
        const arr = next.get(m.pipeline_id) ?? [];
        arr.push({ processed: m.messages_processed, failed: m.messages_failed });
        while (arr.length > MAX_SAMPLES) arr.shift();
        next.set(m.pipeline_id, arr);
      }
      history = next;
      historyVersion++;
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    } finally {
      loading = false;
    }
  }

  // Bind to the shared live stream as a *secondary* source — the
  // /metrics REST fetch above is still the primary so this page keeps
  // working even if SSE is down. When SSE delivers, we adopt the
  // fresher snapshot and also push to the sparkline window so the
  // chart keeps moving without waiting for the next 5 s poll.
  let lastLiveAt = 0;
  $: if ($liveMetrics && $liveMetrics.receivedAt !== lastLiveAt) {
    lastLiveAt = $liveMetrics.receivedAt;
    pipelines = $liveMetrics.pipelines;
    uptime = $liveMetrics.uptime;
    const next = new Map(history);
    const liveIds = new Set(pipelines.map((p) => p.pipeline_id));
    for (const id of Array.from(next.keys())) {
      if (!liveIds.has(id)) next.delete(id);
    }
    for (const m of pipelines) {
      const arr = next.get(m.pipeline_id) ?? [];
      arr.push({ processed: m.messages_processed, failed: m.messages_failed });
      while (arr.length > MAX_SAMPLES) arr.shift();
      next.set(m.pipeline_id, arr);
    }
    history = next;
    historyVersion++;
    loading = false;
  }

  function deltas(id: string, _v: number, kind: 'processed' | 'failed' = 'processed'): number[] {
    const arr = history.get(id) ?? [];
    if (arr.length < 2) return [];
    const out: number[] = [];
    for (let i = 1; i < arr.length; i++) {
      out.push(Math.max(0, arr[i][kind] - arr[i - 1][kind]));
    }
    return out;
  }

  function fmtBytes(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
  }

  function fmtNum(n: number): string {
    return n.toLocaleString();
  }

  // ── Drilldown state ──────────────────────────────────────────────
  // Single-select expansion. Clicking the same row again collapses.
  // Each row gets aria-expanded so a screen reader announces state.
  let openId: string | null = null;
  function toggleOpen(id: string) {
    openId = openId === id ? null : id;
  }

  $: totalProcessed = pipelines.reduce((s, p) => s + p.messages_processed, 0);
  $: totalFailed = pipelines.reduce((s, p) => s + p.messages_failed, 0);
  $: totalBytes = pipelines.reduce((s, p) => s + p.bytes_processed, 0);
  $: avgLatency = pipelines.length
    ? pipelines.reduce((s, p) => s + p.avg_latency_ms, 0) / pipelines.length
    : 0;
  $: totalFailRate =
    totalProcessed + totalFailed > 0
      ? (totalFailed / (totalProcessed + totalFailed)) * 100
      : 0;

  $: sorted = (() => {
    // Read pipelineMap so the sorted array re-derives when the
    // catalogue settles (name-sort depends on it).
    void pipelineMap;
    return [...pipelines].sort((a, b) => {
      const va = sortValue(a, sortKey);
      const vb = sortValue(b, sortKey);
      let cmp = 0;
      if (typeof va === 'number' && typeof vb === 'number') cmp = va - vb;
      else cmp = String(va).localeCompare(String(vb));
      return sortDir === 'asc' ? cmp : -cmp;
    });
  })();

  onMount(() => {
    void refresh();
    void refreshCatalogues();
    interval = setInterval(refresh, 5_000);
    // Re-pull pipelines + connections at the slow cadence too so
    // the metrics page picks up renames + new pipelines without a
    // full reload — same 15 s cadence the dashboard uses.
    catalogueInterval = setInterval(() => {
      void refreshCatalogues();
    }, 15_000);
  });
  onDestroy(() => {
    if (interval) clearInterval(interval);
    if (catalogueInterval) clearInterval(catalogueInterval);
  });
</script>

<PageHeader
  title={t($locale, 'metrics.title')}
  subtitle={t($locale, 'metrics.pageSubtitle')}
  count={pipelines.length}
>
  <svelte:fragment slot="stats">
    <StatChip
      label={t($locale, 'metrics.processed')}
      value={fmtNum(totalProcessed)}
      tone="success"
    />
    <StatChip
      label={t($locale, 'metrics.failed')}
      value={fmtNum(totalFailed)}
      tone={totalFailed > 0 ? 'danger' : 'default'}
    />
    <StatChip label={t($locale, 'metrics.bytes')} value={fmtBytes(totalBytes)} />
    <StatChip
      label={t($locale, 'metrics.avgLatency')}
      value={`${avgLatency.toFixed(1)} ms`}
    />
    <StatChip label={t($locale, 'metrics.uptime')} value={uptime || '—'} />
  </svelte:fragment>
</PageHeader>

{#if error}
  <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
{/if}

<Card>
  {#if loading}
    <div class="skel-rows">
      {#each Array(4) as _, i (i)}
        <div class="skel-row">
          <Skeleton width="14%" height="0.85em" />
          <Skeleton width="32%" height="0.85em" />
          <Skeleton width="12%" height="0.85em" />
          <Skeleton width="12%" height="0.85em" />
          <Skeleton width="12%" height="0.85em" />
          <Skeleton width="12%" height="0.85em" />
        </div>
      {/each}
    </div>
  {:else if pipelines.length === 0}
    <EmptyState
      illustration="metrics"
      title={t($locale, 'empty.metrics.title')}
      body={t($locale, 'empty.metrics.body')}
    >
      <svelte:fragment slot="action">
        <a class="link-cta" href="/pipelines">{t($locale, 'pipelines.title')}</a>
      </svelte:fragment>
    </EmptyState>
  {:else}
    <div class="m-table-wrap">
      <table class="m-table">
        <thead>
          <tr>
            <th class="col-status">
              <button type="button" class="sort-btn" on:click={() => setSort('status')}>
                <span>{t($locale, 'metrics.col.status')}</span>
                {#if sortKey === 'status'}{#if sortDir === 'asc'}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}{:else}<ArrowUpDown size={11} class="dim" />{/if}
              </button>
            </th>
            <th>
              <button type="button" class="sort-btn" on:click={() => setSort('pipeline_id')}>
                <span>{t($locale, 'dlq.pipeline')}</span>
                {#if sortKey === 'pipeline_id'}{#if sortDir === 'asc'}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}{:else}<ArrowUpDown size={11} class="dim" />{/if}
              </button>
            </th>
            <th>{t($locale, 'metrics.col.flow')}</th>
            <th class="right">
              <button type="button" class="sort-btn" on:click={() => setSort('messages_processed')}>
                <span>{t($locale, 'metrics.processed')}</span>
                {#if sortKey === 'messages_processed'}{#if sortDir === 'asc'}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}{:else}<ArrowUpDown size={11} class="dim" />{/if}
              </button>
            </th>
            <th class="right">
              <button type="button" class="sort-btn" on:click={() => setSort('messages_failed')}>
                <span>{t($locale, 'metrics.failed')}</span>
                {#if sortKey === 'messages_failed'}{#if sortDir === 'asc'}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}{:else}<ArrowUpDown size={11} class="dim" />{/if}
              </button>
            </th>
            <th class="right">
              <button type="button" class="sort-btn" on:click={() => setSort('avg_latency_ms')}>
                <span>{t($locale, 'metrics.avgLatency')}</span>
                {#if sortKey === 'avg_latency_ms'}{#if sortDir === 'asc'}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}{:else}<ArrowUpDown size={11} class="dim" />{/if}
              </button>
            </th>
            <th>{t($locale, 'metrics.col.trend')}</th>
          </tr>
        </thead>
        <tbody>
          {#each sorted as m (m.pipeline_id)}
            {@const failRate =
              m.messages_processed + m.messages_failed > 0
                ? m.messages_failed / (m.messages_processed + m.messages_failed)
                : 0}
            {@const tone = statusTone(m)}
            {@const label = pipelineLabel(m.pipeline_id, pipelineMap)}
            {@const srcType = endpointType(m.pipeline_id, 'source', pipelineMap, connectionMap)}
            {@const dstType = endpointType(m.pipeline_id, 'destination', pipelineMap, connectionMap)}
            {@const srcName = endpointName(m.pipeline_id, 'source', pipelineMap, connectionMap)}
            {@const dstName = endpointName(m.pipeline_id, 'destination', pipelineMap, connectionMap)}
            {@const isOpen = openId === m.pipeline_id}
            {@const spark = deltas(m.pipeline_id, historyVersion, 'processed')}
            <tr
              class="row-main"
              class:row-open={isOpen}
              aria-expanded={isOpen}
              on:click={() => toggleOpen(m.pipeline_id)}
              on:keydown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault();
                  toggleOpen(m.pipeline_id);
                }
              }}
              tabindex="0"
              role="button"
              aria-label={isOpen
                ? t($locale, 'metrics.row.collapse')
                : t($locale, 'metrics.row.expand')}
            >
              <td class="col-status">
                <span class="status-cell">
                  <span class="status-dot" data-tone={tone} aria-hidden="true"></span>
                  <span class="status-label">{statusLabel(m, $locale)}</span>
                </span>
              </td>
              <td class="cell-pipe">
                <div class="cell-name" title={m.pipeline_id}>
                  <span class="chev-wrap" aria-hidden="true">
                    {#if isOpen}
                      <ChevronDown size={13} />
                    {:else}
                      <ChevronRight size={13} />
                    {/if}
                  </span>
                  <Activity size={13} aria-hidden="true" class="row-glyph" />
                  <span class="pipe-name">{label}</span>
                </div>
              </td>
              <td>
                <div
                  class="cell-flow"
                  title="{srcName ?? m.source_queue ?? '?'} → {dstName ?? m.dest_queue ?? '?'}"
                >
                  <span class="flow-chip" data-end="source">
                    <ConnectionTypeIcon type={srcType} size={12} />
                    <span class="flow-chip-name">{srcName ?? m.source_queue ?? '?'}</span>
                  </span>
                  <ArrowRight size={12} aria-hidden="true" class="flow-arr" />
                  <span class="flow-chip" data-end="destination">
                    <ConnectionTypeIcon type={dstType} size={12} />
                    <span class="flow-chip-name">{dstName ?? m.dest_queue ?? '?'}</span>
                  </span>
                </div>
              </td>
              <td class="right">
                <div class="cell-stack">
                  <span class="number">{fmtNum(m.messages_processed)}</span>
                  <span class="cell-sub">{fmtBytes(m.bytes_processed)}</span>
                </div>
              </td>
              <td class="right">
                <div class="cell-stack">
                  {#if m.messages_failed > 0}
                    <span class="fail-count">{fmtNum(m.messages_failed)}</span>
                    <span class="cell-sub fail-rate" class:rate-bad={failRate > 0.01}
                      >{(failRate * 100).toFixed(failRate < 0.001 ? 3 : 2)}%</span
                    >
                  {:else}
                    <span class="muted-zero">0</span>
                    <span class="cell-sub">—</span>
                  {/if}
                </div>
              </td>
              <td
                class="right number"
                class:lat-warn={m.avg_latency_ms > 250}
                class:lat-bad={m.avg_latency_ms > 1000}
              >
                {m.avg_latency_ms.toFixed(1)} ms
              </td>
              <td class="col-spark">
                <Sparkline
                  data={spark}
                  variant={sparklineVariant(m)}
                  width={80}
                  height={26}
                />
              </td>
            </tr>
            {#if isOpen}
              <tr class="drilldown-row" aria-hidden="false">
                <td colspan="7" class="drilldown-cell">
                  <div class="drill" role="region" aria-label="{label} {t($locale, 'metrics.row.expand')}">
                    <!-- Flow + actions row -->
                    <div class="drill-head">
                      <div class="drill-flow">
                        <span class="drill-eyebrow">{t($locale, 'metrics.drilldown.flow')}</span>
                        <div class="drill-flow-row">
                          <span class="drill-chip" data-end="source">
                            <ConnectionTypeIcon type={srcType} size={14} />
                            <span class="drill-chip-name"
                              >{srcName ?? m.source_queue ?? '?'}</span
                            >
                          </span>
                          <ArrowRight size={16} aria-hidden="true" class="flow-arr" />
                          <span class="drill-chip" data-end="destination">
                            <ConnectionTypeIcon type={dstType} size={14} />
                            <span class="drill-chip-name"
                              >{dstName ?? m.dest_queue ?? '?'}</span
                            >
                          </span>
                        </div>
                      </div>
                      <div class="drill-actions">
                        <a
                          class="drill-btn drill-btn-primary"
                          href="/pipelines/{m.pipeline_id}/studio"
                          on:click|stopPropagation
                        >
                          <ExternalLink size={13} aria-hidden="true" />
                          {t($locale, 'metrics.drilldown.openStudio')}
                        </a>
                        <a
                          class="drill-btn"
                          href="/dlq?pipeline={m.pipeline_id}"
                          on:click|stopPropagation
                        >
                          <Inbox size={13} aria-hidden="true" />
                          {t($locale, 'metrics.drilldown.viewDLQ')}
                        </a>
                        <a
                          class="drill-btn"
                          href="/pipelines/{m.pipeline_id}/studio"
                          on:click|stopPropagation
                        >
                          <Edit3 size={13} aria-hidden="true" />
                          {t($locale, 'metrics.drilldown.edit')}
                        </a>
                      </div>
                    </div>

                    <!-- Metrics grid: throughput chart + latency pills -->
                    <div class="drill-grid">
                      <div class="drill-panel drill-panel-chart">
                        <p class="drill-eyebrow">{t($locale, 'metrics.drilldown.throughput')}</p>
                        {#if spark.length >= 2}
                          <Sparkline
                            data={spark}
                            variant={sparklineVariant(m)}
                            width={420}
                            height={84}
                            label="{label} {t($locale, 'metrics.drilldown.throughput')}"
                          />
                        {:else}
                          <p class="drill-empty">
                            {t($locale, 'empty.metrics.body')}
                          </p>
                        {/if}
                      </div>
                      <div class="drill-panel">
                        <p class="drill-eyebrow">{t($locale, 'metrics.drilldown.latency')}</p>
                        <div class="lat-pills">
                          <div class="lat-pill">
                            <span class="lat-pill-key">avg</span>
                            <span class="lat-pill-val"
                              class:lat-warn={m.avg_latency_ms > 250}
                              class:lat-bad={m.avg_latency_ms > 1000}
                              >{m.avg_latency_ms.toFixed(1)} ms</span
                            >
                          </div>
                          <div class="lat-pill">
                            <span class="lat-pill-key">{t($locale, 'metrics.processed')}</span>
                            <span class="lat-pill-val">{fmtNum(m.messages_processed)}</span>
                          </div>
                          <div class="lat-pill">
                            <span class="lat-pill-key">{t($locale, 'metrics.failed')}</span>
                            <span class="lat-pill-val" class:fail-count={m.messages_failed > 0}
                              >{fmtNum(m.messages_failed)}</span
                            >
                          </div>
                          <div class="lat-pill">
                            <span class="lat-pill-key">{t($locale, 'metrics.bytes')}</span>
                            <span class="lat-pill-val">{fmtBytes(m.bytes_processed)}</span>
                          </div>
                        </div>
                      </div>
                    </div>

                    {#if m.last_error}
                      <div class="drill-error">
                        <Alert variant="error">
                          <p class="drill-error-label">
                            {t($locale, 'metrics.drilldown.lastError')}
                          </p>
                          <pre class="drill-error-body">{m.last_error}</pre>
                        </Alert>
                      </div>
                    {/if}
                  </div>
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
        <tfoot>
          <tr>
            <td colspan="3" class="tfoot-label">{t($locale, 'metrics.totals')}</td>
            <td class="right tfoot-total">
              <div class="cell-stack">
                <span class="number">{fmtNum(totalProcessed)}</span>
                <span class="cell-sub">{fmtBytes(totalBytes)}</span>
              </div>
            </td>
            <td class="right tfoot-total" class:tfoot-bad={totalFailed > 0}>
              <div class="cell-stack">
                <span class="number">{fmtNum(totalFailed)}</span>
                <span class="cell-sub fail-rate" class:rate-bad={totalFailRate > 1}
                  >{totalFailRate > 0
                    ? `${totalFailRate.toFixed(totalFailRate < 0.1 ? 3 : 2)}%`
                    : '—'}</span
                >
              </div>
            </td>
            <td class="right number tfoot-total">{avgLatency.toFixed(1)} ms</td>
            <td></td>
          </tr>
        </tfoot>
      </table>
    </div>
  {/if}
</Card>

<p class="refresh-note">
  <Clock size={12} aria-hidden="true" /> {t($locale, 'metrics.refreshNote')}
</p>

<style>
  .m-table-wrap {
    /* No horizontal scroll. Auto-layout below shrinks numeric cells
       to their content and gives Pipeline + Flow the remaining width
       (with ellipsis if the queue pair is genuinely too long). */
    overflow-x: hidden;
    margin-inline: -16px;
  }
  .m-table {
    inline-size: 100%;
    table-layout: auto;
    border-collapse: collapse;
    font-size: 0.8125rem;
  }
  /* All columns except Flow (col 3) are rigid: each one shrinks to
     fit just its content via the `inline-size: 1% + nowrap` trick.
     Status + Pipeline are now content-sized too (no UUID column). */
  .m-table thead th:nth-child(1),    /* Status */
  .m-table thead th:nth-child(2),    /* Pipeline (name + chevron) */
  .m-table thead th:nth-child(4),    /* Processed */
  .m-table thead th:nth-child(5),    /* Failed */
  .m-table thead th:nth-child(6),    /* Avg latency */
  .m-table thead th:nth-child(7) {   /* Trend */
    inline-size: 1%;
  }
  .m-table thead th {
    text-align: start;
    padding: 0.5rem 0.625rem;
    color: var(--text-muted);
    font-size: 0.6875rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom: 1px solid var(--border);
    background: var(--surface);
    position: sticky;
    top: 0;
    z-index: 1;
    white-space: nowrap;
  }
  .sort-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    background: transparent;
    border: 0;
    padding: 0;
    margin: 0;
    color: inherit;
    font: inherit;
    font-size: inherit;
    text-transform: inherit;
    letter-spacing: inherit;
    cursor: pointer;
    white-space: nowrap;
  }
  .sort-btn:hover {
    color: var(--text);
  }
  .sort-btn :global(svg.dim) {
    opacity: 0.4;
  }

  .col-spark {
    text-align: center;
    padding-block: 4px;
  }
  /* Numeric body cells stay on one line so the right-edge stack
     reads cleanly. */
  .m-table td.right,
  .m-table td.number {
    white-space: nowrap;
  }

  /* Stacked metric cell: big tabular number on top, muted sub-row
     (bytes / fail %) underneath. */
  .cell-stack {
    display: inline-flex;
    flex-direction: column;
    align-items: flex-end;
    line-height: 1.15;
  }
  .cell-sub {
    color: var(--text-tertiary);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
    margin-block-start: 2px;
  }
  .fail-rate.rate-bad {
    color: var(--warning);
  }

  /* Main row — clickable. Cursor + hover tint signal that something
     opens. Tabindex + role="button" + keydown handler give parity
     with mouse for keyboard-only users. */
  .m-table tbody tr.row-main {
    transition: background-color 100ms;
    cursor: pointer;
  }
  .m-table tbody tr.row-main:hover {
    background: var(--surface-2);
  }
  .m-table tbody tr.row-main:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  /* When a row is open, give it a slightly stronger tint so the
     drilldown-row clearly belongs to it. */
  .m-table tbody tr.row-main.row-open {
    background: var(--surface-2);
  }
  .m-table td {
    padding: 0.5rem 0.625rem;
    border-bottom: 1px solid var(--divider-subtle);
    color: var(--text);
    vertical-align: middle;
  }
  .m-table tbody tr:last-child td {
    border-bottom: 0;
  }
  /* Drilldown rows: drop the border so the panel reads as a single
     surface attached to its parent row. */
  .m-table tbody tr.drilldown-row td {
    border-bottom: 1px solid var(--divider);
    padding: 0;
    background: var(--surface);
  }

  .m-table tfoot td {
    padding: 0.625rem;
    background: var(--surface);
    border-top: 1.5px solid var(--border-strong);
    border-bottom: 0;
    font-weight: 600;
    color: var(--text);
  }
  .tfoot-label {
    color: var(--text-muted);
    font-size: 0.6875rem;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .tfoot-total {
    font-variant-numeric: tabular-nums;
  }
  .tfoot-bad {
    color: var(--danger);
  }
  .rate-bad {
    color: var(--warning);
  }
  .lat-warn {
    color: var(--warning);
  }
  .lat-bad {
    color: var(--danger);
  }
  .right {
    text-align: end;
  }
  .number {
    font-variant-numeric: tabular-nums;
    font-weight: 600;
  }
  .muted-zero {
    color: var(--text-tertiary);
  }

  /* ── Status cell ─────────────────────────────────────────────── */
  .col-status {
    inline-size: 1%;
  }
  .status-cell {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    white-space: nowrap;
  }
  .status-dot {
    display: inline-block;
    inline-size: 8px;
    block-size: 8px;
    border-radius: 999px;
    background: var(--text-tertiary);
    flex-shrink: 0;
  }
  .status-dot[data-tone='success'] {
    background: var(--success-solid);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--success) 22%, transparent);
  }
  .status-dot[data-tone='warning'] {
    background: var(--warning);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--warning) 22%, transparent);
  }
  .status-dot[data-tone='danger'] {
    background: var(--danger);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--danger) 22%, transparent);
    animation: status-pulse 1.6s ease-in-out infinite;
  }
  @keyframes status-pulse {
    0%,
    100% {
      transform: scale(1);
      opacity: 1;
    }
    50% {
      transform: scale(1.2);
      opacity: 0.8;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .status-dot {
      animation: none !important;
    }
  }
  .status-label {
    color: var(--text);
    font-size: 12px;
    font-weight: 500;
  }

  /* ── Pipeline-name cell ──────────────────────────────────────── */
  .cell-pipe {
    /* Content-sized; the name is the dominant element here. Use the
       dash-link styling (bold body text + hover underline) — never
       maroon — per the brand discipline. */
    white-space: nowrap;
  }
  .cell-name {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    color: var(--text);
  }
  .chev-wrap {
    color: var(--text-tertiary);
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
  }
  .row-main:hover .chev-wrap,
  .row-main.row-open .chev-wrap {
    color: var(--text-muted);
  }
  /* RTL: the chevron points the natural reading direction; flip
     ChevronRight so it points inline-end. */
  :global([dir='rtl']) .chev-wrap :global(svg) {
    transform: scaleX(-1);
  }
  .cell-name :global(svg.row-glyph) {
    color: var(--text-tertiary);
    flex-shrink: 0;
  }
  .pipe-name {
    color: var(--text);
    font-weight: 600;
    font-size: 13px;
    border-block-end: 1px solid transparent;
    padding-block-end: 1px;
    transition: border-color 120ms ease;
  }
  .row-main:hover .pipe-name,
  .row-main:focus-visible .pipe-name {
    border-block-end-color: var(--border-strong, var(--border));
  }

  /* ── Flow chips ──────────────────────────────────────────────── */
  .cell-flow {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--text);
    white-space: nowrap;
    min-inline-size: 0;
    overflow: hidden;
  }
  .flow-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding-block: 1px;
    padding-inline: 6px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text);
    max-inline-size: 12rem;
    min-inline-size: 0;
  }
  .flow-chip[data-end='source'] :global(svg),
  .flow-chip[data-end='destination'] :global(svg) {
    color: var(--primary);
    flex-shrink: 0;
  }
  .flow-chip-name {
    color: var(--text);
    font-weight: 500;
    font-size: 11px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-inline-size: 0;
  }
  :global(.flow-arr) {
    flex: 0 0 auto;
    color: var(--text-tertiary);
  }
  :global([dir='rtl']) :global(.flow-arr) {
    transform: scaleX(-1);
  }

  .fail-count {
    color: var(--danger);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  /* ── Drilldown panel ─────────────────────────────────────────── */
  .drilldown-cell {
    padding: 0;
  }
  .drill {
    padding: 16px 20px;
    background: var(--surface-2);
    display: flex;
    flex-direction: column;
    gap: 14px;
    border-inline-start: 3px solid var(--accent, var(--primary));
  }
  .drill-head {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 16px;
    flex-wrap: wrap;
  }
  .drill-flow {
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-inline-size: 0;
  }
  .drill-eyebrow {
    color: var(--text-tertiary);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 600;
    margin: 0;
  }
  .drill-flow-row {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .drill-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding-block: 4px;
    padding-inline: 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    font-size: 13px;
    font-weight: 500;
  }
  .drill-chip[data-end='source'] :global(svg),
  .drill-chip[data-end='destination'] :global(svg) {
    color: var(--primary);
    flex-shrink: 0;
  }
  .drill-chip-name {
    color: var(--text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-inline-size: 16rem;
  }

  .drill-actions {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .drill-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding-block: 6px;
    padding-inline: 12px;
    border-radius: 8px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--text);
    text-decoration: none;
    font-size: 12px;
    font-weight: 500;
    transition:
      background-color 120ms ease,
      border-color 120ms ease;
  }
  .drill-btn:hover,
  .drill-btn:focus-visible {
    background: var(--surface-2);
    border-color: var(--border-strong);
  }
  .drill-btn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }
  .drill-btn-primary {
    background: var(--surface);
    border-color: var(--primary);
    color: var(--text);
  }
  .drill-btn-primary :global(svg) {
    color: var(--primary);
  }

  .drill-grid {
    display: grid;
    grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
    gap: 14px;
  }
  @media (max-width: 900px) {
    .drill-grid {
      grid-template-columns: 1fr;
    }
  }
  .drill-panel {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 10px;
  }
  .drill-panel-chart :global(svg.sparkline) {
    inline-size: 100%;
    block-size: auto;
  }
  .drill-empty {
    color: var(--text-tertiary);
    font-size: 12px;
    margin: 0;
  }

  .lat-pills {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }
  .lat-pill {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding-block: 6px;
    padding-inline: 10px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    min-inline-size: 0;
  }
  .lat-pill-key {
    color: var(--text-tertiary);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .lat-pill-val {
    color: var(--text);
    font-size: 14px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .drill-error {
    /* The Alert component owns its own border + padding; we just give
       it inline-block layout context. */
    margin-block-start: 2px;
  }
  .drill-error-label {
    color: var(--text-muted);
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    margin: 0 0 4px;
    font-weight: 600;
  }
  .drill-error-body {
    margin: 0;
    font-family: 'SFMono-Regular', Menlo, monospace;
    font-size: 12px;
    line-height: 1.45;
    color: var(--danger);
    white-space: pre-wrap;
    word-break: break-word;
  }

  .refresh-note {
    margin-top: 0.75rem;
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    color: var(--text-tertiary);
    font-size: 0.75rem;
  }

  .skel-rows {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.5rem;
  }
  .skel-row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .link-cta {
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
  .link-cta:hover {
    background: var(--accent-hover);
  }
</style>
