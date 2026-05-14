<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type DLQEntry } from '$lib/api';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Badge from '$lib/components/Badge.svelte';

  let entries: DLQEntry[] = [];
  let total = 0;
  let page = 1;
  let perPage = 20;
  let error = '';
  let loading = false;

  async function refresh() {
    loading = true;
    try {
      const resp = await api.get<{ items: DLQEntry[]; total: number }>(
        `/v1/dlq?page=${page}&per_page=${perPage}`
      );
      entries = resp.items ?? [];
      total = resp.total ?? 0;
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    } finally {
      loading = false;
    }
  }

  onMount(refresh);

  async function retry(e: DLQEntry) {
    try {
      await api.post(`/v1/dlq/${e.id}/retry`);
      await refresh();
    } catch (err: unknown) {
      error = (err as { message?: string }).message || 'retry failed';
    }
  }
  async function remove(e: DLQEntry) {
    if (!confirm('Delete this entry?')) return;
    try {
      await api.del(`/v1/dlq/${e.id}`);
      await refresh();
    } catch (err: unknown) {
      error = (err as { message?: string }).message || 'delete failed';
    }
  }
</script>

<div class="space-y-6 max-w-6xl">
  <div class="flex items-baseline justify-between">
    <h2 class="text-2xl font-semibold" style="color: var(--text)">Dead-letter queue</h2>
    <span class="text-sm" style="color: var(--text-muted)">{total} total</span>
  </div>

  {#if error}
    <p style="color: var(--danger)">{error}</p>
  {/if}

  <Card>
    {#if loading && entries.length === 0}
      <p style="color: var(--text-muted)">Loading…</p>
    {:else if entries.length === 0}
      <p style="color: var(--text-muted)">No dead letters.</p>
    {:else}
      <table class="table">
        <thead>
          <tr>
            <th>When</th>
            <th>Pipeline</th>
            <th>Queue</th>
            <th>Reason</th>
            <th>Retries</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each entries as e}
            <tr>
              <td style="color: var(--text-muted); white-space: nowrap;">
                {new Date(e.created_at).toLocaleString()}
              </td>
              <td style="color: var(--text)">{e.pipeline_id || '—'}</td>
              <td>{e.source_queue || '—'}</td>
              <td style="color: var(--danger); max-width: 300px;">
                <div class="truncate" title={e.error_reason}>{e.error_reason}</div>
              </td>
              <td><Badge variant="neutral">{e.retry_count}</Badge></td>
              <td>
                <div class="flex gap-2 justify-end">
                  <Button variant="ghost" on:click={() => retry(e)}>Retry</Button>
                  <Button variant="outline" on:click={() => remove(e)}>Delete</Button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </Card>
</div>

<style>
  td:last-child { text-align: end; }
  .truncate { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
