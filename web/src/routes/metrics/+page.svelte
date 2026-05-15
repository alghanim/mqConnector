<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type PipelineMetric } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Badge from '$lib/components/Badge.svelte';

  let uptime = '';
  let pipelines: PipelineMetric[] = [];
  let error = '';
  let interval: ReturnType<typeof setInterval> | undefined;

  async function refresh() {
    try {
      const res = await api.get<{ uptime: string; pipelines: Record<string, PipelineMetric> }>('/metrics');
      uptime = res.uptime;
      pipelines = Object.values(res.pipelines || {});
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
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

  onMount(() => {
    refresh();
    interval = setInterval(refresh, 5_000);
  });
  onDestroy(() => {
    if (interval) clearInterval(interval);
  });
</script>

<div class="space-y-6 max-w-5xl">
  <div class="flex items-baseline justify-between">
    <h2 class="text-2xl font-semibold" style="color: var(--text)">
      {t($locale, 'metrics.title')}
    </h2>
    <span class="text-xs" style="color: var(--text-muted)">
      {t($locale, 'metrics.uptime')} {uptime} · {t($locale, 'metrics.refreshNote')}
    </span>
  </div>

  {#if error}
    <p style="color: var(--danger)">{error}</p>
  {/if}

  {#if pipelines.length === 0}
    <Card>
      <p style="color: var(--text-muted)">{t($locale, 'common.none')}</p>
    </Card>
  {:else}
    <Card>
      <table class="table">
        <thead>
          <tr>
            <th>{t($locale, 'dlq.pipeline')}</th>
            <th>{t($locale, 'pipelines.flow')}</th>
            <th>{t($locale, 'common.status')}</th>
            <th>{t($locale, 'metrics.processed')}</th>
            <th>{t($locale, 'metrics.failed')}</th>
            <th>{t($locale, 'metrics.bytes')}</th>
            <th>{t($locale, 'metrics.avgLatency')}</th>
            <th>{t($locale, 'metrics.lastMessage')}</th>
          </tr>
        </thead>
        <tbody>
          {#each pipelines as m (m.pipeline_id)}
            <tr>
              <td style="color: var(--text)">{m.pipeline_id}</td>
              <td style="color: var(--text-muted)">{m.source_queue} → {m.dest_queue}</td>
              <td><Badge variant={statusVariant(m.status)}>{m.status}</Badge></td>
              <td style="color: var(--text)">{m.messages_processed.toLocaleString()}</td>
              <td>
                {#if m.messages_failed > 0}
                  <Badge variant="danger">{m.messages_failed}</Badge>
                {:else}
                  <span style="color: var(--text-muted)">0</span>
                {/if}
              </td>
              <td style="color: var(--text-muted)">{fmtBytes(m.bytes_processed)}</td>
              <td style="color: var(--text-muted)">{m.avg_latency_ms.toFixed(1)} ms</td>
              <td style="color: var(--text-muted)">{m.last_message_time || '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </Card>

    {#each pipelines.filter((m) => m.last_error) as m (m.pipeline_id)}
      <Card>
        <p class="section-heading">{t($locale, 'common.reason')} · {m.pipeline_id}</p>
        <pre class="mt-2 text-xs whitespace-pre-wrap" style="color: var(--danger)">{m.last_error}</pre>
      </Card>
    {/each}
  {/if}
</div>
