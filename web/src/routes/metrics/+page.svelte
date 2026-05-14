<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type PipelineMetric } from '$lib/api';
  import Card from '$lib/components/Card.svelte';
  import Badge from '$lib/components/Badge.svelte';

  let pipelines: Record<string, PipelineMetric> = {};
  let uptime = '';
  let error = '';

  async function refresh() {
    try {
      const resp = await api.get<{ uptime: string; pipelines: Record<string, PipelineMetric> }>(
        '/metrics'
      );
      pipelines = resp.pipelines || {};
      uptime = resp.uptime || '';
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    }
  }

  onMount(() => {
    refresh();
    const i = setInterval(refresh, 5_000);
    return () => clearInterval(i);
  });

  function variantFor(s: string): 'success' | 'warning' | 'danger' | 'neutral' {
    if (s === 'connected') return 'success';
    if (s === 'error') return 'danger';
    if (s === 'disconnected') return 'warning';
    return 'neutral';
  }
</script>

<div class="space-y-6 max-w-6xl">
  <div class="flex items-baseline justify-between">
    <h2 class="text-2xl font-semibold" style="color: var(--text)">Metrics</h2>
    <span class="text-sm" style="color: var(--text-muted)">uptime {uptime}</span>
  </div>

  {#if error}
    <p style="color: var(--danger)">{error}</p>
  {/if}

  <Card>
    {#if Object.keys(pipelines).length === 0}
      <p style="color: var(--text-muted)">No active pipelines.</p>
    {:else}
      <table class="table">
        <thead>
          <tr>
            <th>Pipeline</th>
            <th>Source → Dest</th>
            <th>Status</th>
            <th>Processed</th>
            <th>Failed</th>
            <th>Bytes</th>
            <th>Avg latency</th>
          </tr>
        </thead>
        <tbody>
          {#each Object.values(pipelines) as m}
            <tr>
              <td style="color: var(--text)">{m.pipeline_id}</td>
              <td style="color: var(--text-muted)">{m.source_queue} → {m.dest_queue}</td>
              <td><Badge variant={variantFor(m.status)}>{m.status}</Badge></td>
              <td>{m.messages_processed}</td>
              <td>{m.messages_failed}</td>
              <td>{m.bytes_processed}</td>
              <td>{m.avg_latency_ms.toFixed(2)} ms</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </Card>
</div>
