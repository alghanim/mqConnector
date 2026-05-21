<!--
  /metrics — pipeline throughput dashboard.

  Anatomy:
    PageHeader        title + subtitle + uptime chip + refresh state
    KPI band          totals across all pipelines (processed, failed,
                      bytes, avg latency)
    Live table        per pipeline: status pill, flow, processed,
                      failed (ratio), bytes, avg latency, last seen
                      timestamp; row hover surfaces the error trail
                      when failed > 0.
    Error trail       below the table, one card per pipeline currently
                      in error state with its last_error message.
-->
<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { api, type PipelineMetric } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatChip from '$lib/components/StatChip.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Sparkline from '$lib/components/Sparkline.svelte';
  import { Activity, AlertOctagon, ArrowRight, Clock, ArrowUpDown, ArrowUp, ArrowDown } from 'lucide-svelte';

  let uptime = '';
  let pipelines: PipelineMetric[] = [];
  let error = '';
  let loading = true;
  let interval: ReturnType<typeof setInterval> | undefined;

  // Sparkline window per pipeline. Same windowing logic as the overview
  // page — 13 snapshots = 12 deltas = 60 s at the 5 s polling cadence.
  const MAX_SAMPLES = 13;
  type Snapshot = { processed: number; failed: number };
  let history = new Map<string, Snapshot[]>();
  let historyVersion = 0;

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
      // Sensible defaults: text columns ascending, numeric descending.
      sortDir = k === 'pipeline_id' || k === 'status' || k === 'last_message_time' ? 'asc' : 'desc';
    }
  }

  function sortValue(m: PipelineMetric, k: SortKey): number | string {
    if (k === 'fail_rate') {
      const t = m.messages_processed + m.messages_failed;
      return t > 0 ? m.messages_failed / t : 0;
    }
    const v = (m as unknown as Record<string, unknown>)[k];
    if (typeof v === 'number') return v;
    return (v as string) || '';
  }

  async function refresh() {
    try {
      const res = await api.get<{
        uptime: string;
        pipelines: Record<string, PipelineMetric>;
      }>('/metrics');
      uptime = res.uptime;
      pipelines = Object.values(res.pipelines || {});

      // Window the per-pipeline processed/failed counters for sparklines.
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

  function deltas(id: string, _v: number, kind: 'processed' | 'failed' = 'processed'): number[] {
    const arr = history.get(id) ?? [];
    if (arr.length < 2) return [];
    const out: number[] = [];
    for (let i = 1; i < arr.length; i++) {
      out.push(Math.max(0, arr[i][kind] - arr[i - 1][kind]));
    }
    return out;
  }

  function statusVariant(s: string): 'success' | 'warning' | 'danger' | 'neutral' {
    if (s === 'connected') return 'success';
    if (s === 'error') return 'danger';
    return 'neutral';
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

  function fmtTime(s: string): string {
    if (!s) return '—';
    // Show only HH:MM:SS portion when a timestamp is present; falls back
    // to the raw string otherwise.
    const m = /T(\d{2}:\d{2}:\d{2})/.exec(s);
    return m ? m[1] : s;
  }


  $: totalProcessed = pipelines.reduce((s, p) => s + p.messages_processed, 0);
  $: totalFailed = pipelines.reduce((s, p) => s + p.messages_failed, 0);
  $: totalBytes = pipelines.reduce((s, p) => s + p.bytes_processed, 0);
  $: avgLatency = pipelines.length
    ? pipelines.reduce((s, p) => s + p.avg_latency_ms, 0) / pipelines.length
    : 0;
  $: errorPipelines = pipelines.filter((p) => p.last_error);
  $: totalFailRate =
    totalProcessed + totalFailed > 0
      ? (totalFailed / (totalProcessed + totalFailed)) * 100
      : 0;

  $: sorted = [...pipelines].sort((a, b) => {
    const va = sortValue(a, sortKey);
    const vb = sortValue(b, sortKey);
    let cmp = 0;
    if (typeof va === 'number' && typeof vb === 'number') cmp = va - vb;
    else cmp = String(va).localeCompare(String(vb));
    return sortDir === 'asc' ? cmp : -cmp;
  });

  onMount(() => {
    void refresh();
    interval = setInterval(refresh, 5_000);
  });
  onDestroy(() => {
    if (interval) clearInterval(interval);
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
            <th>
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
            <tr>
              <td>
                <Badge variant={statusVariant(m.status)}>{m.status}</Badge>
              </td>
              <td class="cell-pipe">
                <div class="cell-name" title={m.pipeline_id}>
                  <Activity size={14} aria-hidden="true" class="flow-arr" />
                  <code class="mono pipe-id">{m.pipeline_id}</code>
                </div>
              </td>
              <td>
                <div class="cell-flow" title="{m.source_queue || '?'} → {m.dest_queue || '?'}">
                  <code class="mono small flow-end">{m.source_queue || '?'}</code>
                  <ArrowRight size={12} aria-hidden="true" class="flow-arr" />
                  <code class="mono small flow-end">{m.dest_queue || '?'}</code>
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
                    <span class="cell-sub fail-rate" class:rate-bad={failRate > 0.01}>{(failRate * 100).toFixed(failRate < 0.001 ? 3 : 2)}%</span>
                  {:else}
                    <span class="muted-zero">0</span>
                    <span class="cell-sub">—</span>
                  {/if}
                </div>
              </td>
              <td class="right number" class:lat-warn={m.avg_latency_ms > 250} class:lat-bad={m.avg_latency_ms > 1000}>
                {m.avg_latency_ms.toFixed(1)} ms
              </td>
              <td class="col-spark">
                <Sparkline
                  data={deltas(m.pipeline_id, historyVersion, 'processed')}
                  variant={m.messages_failed > 0 ? 'warning' : 'secondary'}
                  width={88}
                  height={22}
                />
              </td>
            </tr>
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
                <span class="cell-sub fail-rate" class:rate-bad={totalFailRate > 1}>{totalFailRate > 0 ? `${totalFailRate.toFixed(totalFailRate < 0.1 ? 3 : 2)}%` : '—'}</span>
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

{#if errorPipelines.length > 0}
  <section class="errors">
    <h2 class="errors-title">
      <AlertOctagon size={16} aria-hidden="true" />
      {t($locale, 'common.reason')}
    </h2>
    {#each errorPipelines as m (m.pipeline_id)}
      <Card>
        <p class="error-pipeline mono">{m.pipeline_id}</p>
        <pre class="error-body">{m.last_error}</pre>
      </Card>
    {/each}
  </section>
{/if}

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
  /* Every column except Flow (col 3) is rigid: each one shrinks to
     fit just its content via the `inline-size: 1% + nowrap` trick.
     Pipeline (col 2) holds the full UUID; Flow (col 3) is the sole
     elastic column and ellipses when the queue pair overflows. */
  .m-table thead th:nth-child(1),    /* Status */
  .m-table thead th:nth-child(2),    /* Pipeline (full UUID) */
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
     (bytes / fail %) underneath. Keeps signal density without
     widening the row. */
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

  .m-table tbody tr {
    transition: background-color 100ms;
  }
  .m-table tbody tr:hover {
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
  .mono {
    font-family: 'SFMono-Regular', Menlo, monospace;
  }
  .mono.small {
    font-size: 0.75rem;
  }
  .muted-zero {
    color: var(--text-tertiary);
  }

  .cell-pipe {
    /* Column rigidly sized to its content (the full UUID + icon).
       No ellipsis here — the operator needs the whole id. */
    white-space: nowrap;
  }
  .cell-name {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    color: var(--text-muted);
  }
  .pipe-id {
    /* Hyphens in UUIDs are soft-break opportunities; force one line. */
    word-break: keep-all;
    overflow-wrap: normal;
    white-space: nowrap;
  }
  .cell-flow {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text);
    white-space: nowrap;
    min-inline-size: 0;
    overflow: hidden;
  }
  .flow-end {
    overflow: hidden;
    text-overflow: ellipsis;
    min-inline-size: 0;
    flex: 1 1 auto;
  }
  :global(.flow-arr) {
    flex: 0 0 auto;
  }
  :global([dir='rtl']) :global(.flow-arr) {
    transform: scaleX(-1);
  }

  .fail-count {
    color: var(--danger);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .errors {
    margin-top: 1.25rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .errors-title {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0;
    color: var(--danger);
    font-size: 0.9375rem;
    font-weight: 600;
  }
  .error-pipeline {
    margin: 0;
    font-size: 0.75rem;
    color: var(--text-muted);
  }
  .error-body {
    margin: 0.375rem 0 0;
    font-family: 'SFMono-Regular', Menlo, monospace;
    font-size: 0.75rem;
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
