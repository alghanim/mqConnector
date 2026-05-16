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
  import { Activity, AlertOctagon, ArrowRight, Clock } from 'lucide-svelte';

  let uptime = '';
  let pipelines: PipelineMetric[] = [];
  let error = '';
  let loading = true;
  let interval: ReturnType<typeof setInterval> | undefined;

  async function refresh() {
    try {
      const res = await api.get<{
        uptime: string;
        pipelines: Record<string, PipelineMetric>;
      }>('/metrics');
      uptime = res.uptime;
      pipelines = Object.values(res.pipelines || {});
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    } finally {
      loading = false;
    }
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

  $: totalProcessed = pipelines.reduce((s, p) => s + p.messages_processed, 0);
  $: totalFailed = pipelines.reduce((s, p) => s + p.messages_failed, 0);
  $: totalBytes = pipelines.reduce((s, p) => s + p.bytes_processed, 0);
  $: avgLatency = pipelines.length
    ? pipelines.reduce((s, p) => s + p.avg_latency_ms, 0) / pipelines.length
    : 0;
  $: errorPipelines = pipelines.filter((p) => p.last_error);

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
    <table class="m-table">
      <thead>
        <tr>
          <th>{t($locale, 'metrics.col.status')}</th>
          <th>{t($locale, 'dlq.pipeline')}</th>
          <th>{t($locale, 'metrics.col.flow')}</th>
          <th class="right">{t($locale, 'metrics.processed')}</th>
          <th class="right">{t($locale, 'metrics.failed')}</th>
          <th class="right">{t($locale, 'metrics.bytes')}</th>
          <th class="right">{t($locale, 'metrics.avgLatency')}</th>
          <th>{t($locale, 'metrics.lastMessage')}</th>
        </tr>
      </thead>
      <tbody>
        {#each pipelines as m (m.pipeline_id)}
          {@const failRate =
            m.messages_processed + m.messages_failed > 0
              ? m.messages_failed / (m.messages_processed + m.messages_failed)
              : 0}
          <tr>
            <td>
              <span class="status-dot" data-tone={statusVariant(m.status)} aria-hidden="true"></span>
              <Badge variant={statusVariant(m.status)}>{m.status}</Badge>
            </td>
            <td>
              <div class="cell-name">
                <Activity size={14} aria-hidden="true" />
                <code class="mono">{m.pipeline_id}</code>
              </div>
            </td>
            <td class="cell-flow">
              <code class="mono small">{m.source_queue || '?'}</code>
              <ArrowRight size={12} aria-hidden="true" class="flow-arr" />
              <code class="mono small">{m.dest_queue || '?'}</code>
            </td>
            <td class="right number">{fmtNum(m.messages_processed)}</td>
            <td class="right">
              {#if m.messages_failed > 0}
                <span class="fail-cell">
                  <span class="fail-count">{fmtNum(m.messages_failed)}</span>
                  <span class="fail-rate">({(failRate * 100).toFixed(1)}%)</span>
                </span>
              {:else}
                <span class="muted-zero">0</span>
              {/if}
            </td>
            <td class="right mono small">{fmtBytes(m.bytes_processed)}</td>
            <td class="right mono small">{m.avg_latency_ms.toFixed(1)} ms</td>
            <td class="mono small muted">{m.last_message_time || '—'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
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
  .m-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.8125rem;
  }
  .m-table thead th {
    text-align: start;
    padding: 0.5rem 0.75rem;
    color: var(--text-muted);
    font-size: 0.6875rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom: 1px solid var(--border);
    background: var(--surface);
    position: sticky;
    top: 0;
  }
  .m-table tbody tr {
    transition: background-color 100ms;
  }
  .m-table tbody tr:hover {
    background: var(--surface-2);
  }
  .m-table td {
    padding: 0.625rem 0.75rem;
    border-bottom: 1px solid var(--border);
    color: var(--text);
    vertical-align: middle;
  }
  .m-table tbody tr:last-child td {
    border-bottom: 0;
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
  .muted {
    color: var(--text-muted);
  }
  .muted-zero {
    color: var(--text-tertiary);
  }

  .cell-name {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    color: var(--text-muted);
  }
  .cell-flow {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text);
  }
  :global([dir='rtl']) :global(.flow-arr) {
    transform: scaleX(-1);
  }

  .status-dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 999px;
    margin-inline-end: 6px;
    background: var(--text-tertiary);
    vertical-align: middle;
  }
  .status-dot[data-tone='success'] {
    background: var(--success);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--success) 22%, transparent);
  }
  .status-dot[data-tone='warning'] {
    background: var(--warning);
  }
  .status-dot[data-tone='danger'] {
    background: var(--danger);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--danger) 22%, transparent);
    animation: pulse 1.6s ease-in-out infinite;
  }
  @keyframes pulse {
    0%, 100% { transform: scale(1); opacity: 1; }
    50% { transform: scale(1.18); opacity: .8; }
  }
  @media (prefers-reduced-motion: reduce) {
    .status-dot { animation: none !important; }
  }

  .fail-cell {
    display: inline-flex;
    align-items: baseline;
    gap: 0.375rem;
  }
  .fail-count {
    color: var(--danger);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  .fail-rate {
    color: var(--text-tertiary);
    font-size: 0.6875rem;
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
    border-radius: 8px;
    background: var(--accent);
    color: #fff;
    text-decoration: none;
    font-size: 0.875rem;
    font-weight: 500;
    border: 1px solid var(--accent);
    transition: filter 150ms;
  }
  .link-cta:hover {
    filter: brightness(1.1);
  }
</style>
