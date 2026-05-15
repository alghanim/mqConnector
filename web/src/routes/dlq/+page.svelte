<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type DLQEntry } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Dialog from '$lib/components/Dialog.svelte';

  let entries: DLQEntry[] = [];
  let total = 0;
  let page = 1;
  const perPage = 20;
  let error = '';
  let busy = false;
  let pendingDeleteId: string | null = null;
  let deleting = false;

  async function refresh() {
    busy = true;
    try {
      const res = await api.get<{ page: number; per_page: number; total: number; items: DLQEntry[] }>(
        `/v1/dlq?page=${page}&per_page=${perPage}`
      );
      entries = res.items ?? [];
      total = res.total ?? 0;
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    } finally {
      busy = false;
    }
  }

  async function retry(id: string) {
    try {
      await api.post(`/v1/dlq/${id}/retry`);
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'retry failed';
    }
  }

  function askRemove(id: string) {
    pendingDeleteId = id;
  }
  async function confirmRemove() {
    if (!pendingDeleteId) return;
    deleting = true;
    try {
      await api.del(`/v1/dlq/${pendingDeleteId}`);
      pendingDeleteId = null;
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'delete failed';
    } finally {
      deleting = false;
    }
  }

  function previewPayload(b64: string): string {
    try {
      const txt = atob(b64);
      return txt.length > 200 ? txt.slice(0, 200) + '…' : txt;
    } catch {
      return '(undecodable)';
    }
  }

  $: pages = Math.max(1, Math.ceil(total / perPage));
  onMount(refresh);
</script>

<div class="space-y-6 max-w-5xl">
  <div class="flex items-baseline justify-between">
    <h2 class="text-2xl font-semibold" style="color: var(--text)">{t($locale, 'dlq.title')}</h2>
    <Button variant="ghost" on:click={refresh} loading={busy}>{t($locale, 'common.refresh')}</Button>
  </div>

  {#if error}
    <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
  {/if}

  <Card>
    {#if entries.length === 0}
      <p style="color: var(--text-muted)">{t($locale, 'dlq.empty')}</p>
    {:else}
      <table class="table">
        <thead>
          <tr>
            <th>{t($locale, 'common.when')}</th>
            <th>{t($locale, 'dlq.pipeline')}</th>
            <th>{t($locale, 'dlq.sourceQueue')}</th>
            <th>{t($locale, 'common.reason')}</th>
            <th>{t($locale, 'dlq.retries')}</th>
            <th>{t($locale, 'dlq.payload')}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each entries as e (e.id)}
            <tr>
              <td style="color: var(--text-muted)">{e.created_at}</td>
              <td style="color: var(--text)">{e.pipeline_id || '—'}</td>
              <td style="color: var(--text-muted)">{e.source_queue || '—'}</td>
              <td style="color: var(--text)">{e.error_reason}</td>
              <td><Badge variant={e.retry_count > 0 ? 'warning' : 'neutral'}>{e.retry_count}</Badge></td>
              <td>
                <code class="text-xs" style="color: var(--text-muted)">{previewPayload(e.original_msg)}</code>
              </td>
              <td>
                <div class="flex gap-2 justify-end">
                  <Button variant="ghost" on:click={() => retry(e.id)}>{t($locale, 'common.retry')}</Button>
                  <Button variant="outline" on:click={() => askRemove(e.id)}>{t($locale, 'common.delete')}</Button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>

      {#if pages > 1}
        <div class="flex items-center justify-between mt-4">
          <span class="text-xs" style="color: var(--text-muted)">
            {page} / {pages} · {total}
          </span>
          <div class="flex gap-2">
            <Button variant="ghost" disabled={page <= 1} on:click={() => { page--; refresh(); }}>
              {t($locale, 'common.previous')}
            </Button>
            <Button variant="ghost" disabled={page >= pages} on:click={() => { page++; refresh(); }}>
              {t($locale, 'common.next')}
            </Button>
          </div>
        </div>
      {/if}
    {/if}
  </Card>
</div>

<Dialog
  open={pendingDeleteId !== null}
  title={t($locale, 'common.confirmDelete')}
  confirmLabel={t($locale, 'common.delete')}
  cancelLabel={t($locale, 'common.cancel')}
  busy={deleting}
  on:cancel={() => (pendingDeleteId = null)}
  on:confirm={confirmRemove}
>
  <p>{t($locale, 'dlq.delete.confirm')}</p>
</Dialog>

<style>
  td:last-child { text-align: end; }
</style>
