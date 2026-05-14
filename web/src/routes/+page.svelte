<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Health } from '$lib/api';
  import Card from '$lib/components/Card.svelte';
  import Badge from '$lib/components/Badge.svelte';

  let health: Health | null = null;
  let error = '';

  async function refresh() {
    try {
      health = await api.get<Health>('/health');
      error = '';
    } catch (e: unknown) {
      const err = e as { message?: string };
      error = err.message || 'failed to load';
    }
  }

  onMount(() => {
    refresh();
    const interval = setInterval(refresh, 10_000);
    return () => clearInterval(interval);
  });

  function variantFor(s: string): 'success' | 'warning' | 'danger' | 'neutral' {
    if (s === 'healthy' || s === 'connected') return 'success';
    if (s === 'degraded') return 'warning';
    if (s === 'unhealthy' || s === 'error') return 'danger';
    return 'neutral';
  }
</script>

<div class="space-y-6 max-w-5xl">
  <div class="flex items-baseline justify-between">
    <h2 class="text-2xl font-semibold" style="color: var(--text)">Overview</h2>
    {#if health}
      <span class="text-xs" style="color: var(--text-muted)">v{health.version} · uptime {health.uptime}</span>
    {/if}
  </div>

  {#if error}
    <Card>
      <p style="color: var(--danger)">{error}</p>
    </Card>
  {:else if !health}
    <Card>
      <p style="color: var(--text-muted)">Loading…</p>
    </Card>
  {:else}
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
      <Card strip>
        <p class="section-heading">Overall</p>
        <p class="mt-2 text-2xl font-semibold" style="color: var(--text)">
          <Badge variant={variantFor(health.status)}>{health.status}</Badge>
        </p>
      </Card>
      <Card>
        <p class="section-heading">Database</p>
        <p class="mt-2 text-base" style="color: var(--text)">{health.db_status}</p>
      </Card>
      <Card>
        <p class="section-heading">Active pipelines</p>
        <p class="mt-2 text-2xl font-semibold" style="color: var(--text)">{health.active_pipelines}</p>
      </Card>
    </div>

    {#if health.connections && health.connections.length > 0}
      <Card>
        <p class="section-heading mb-3">Connections</p>
        <table class="table">
          <thead>
            <tr>
              <th>Pipeline</th>
              <th>Source</th>
              <th>Destination</th>
              <th>Status</th>
              <th>Last error</th>
            </tr>
          </thead>
          <tbody>
            {#each health.connections as c}
              <tr>
                <td style="color: var(--text)">{c.pipeline_id}</td>
                <td>{c.source_queue}</td>
                <td>{c.dest_queue}</td>
                <td><Badge variant={variantFor(c.status)}>{c.status}</Badge></td>
                <td style="color: var(--text-muted)">{c.last_error || '—'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </Card>
    {/if}
  {/if}
</div>
