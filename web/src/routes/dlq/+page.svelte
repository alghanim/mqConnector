<!--
  /dlq — Dead-letter queue console.

  Phase 6 added the bits an oncall actually needs during an incident:
    - Row-click drawer with full decoded payload (instead of a 200-char
      preview), error reason, timestamps, retry history.
    - Filter bar (pipeline + error-text contains + time window).
    - Bulk select with parallel retry / delete.

  Filtering is client-side over the currently loaded page; the API
  endpoint only accepts page+per_page today. We default to per_page=100
  so the visible window is large enough that filtering is useful for
  the common case. Server-side filter is a follow-up — kept out of
  Phase 6 to avoid backend changes.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type DLQEntry } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';

  // ─── List state ─────────────────────────────────────────────────
  let entries: DLQEntry[] = [];
  let total = 0;
  let pageNum = 1;
  const perPage = 100;
  let error = '';
  let busy = false;

  async function refresh() {
    busy = true;
    try {
      const res = await api.get<{
        page: number;
        per_page: number;
        total: number;
        items: DLQEntry[];
      }>(`/v1/dlq?page=${pageNum}&per_page=${perPage}`);
      entries = res.items ?? [];
      total = res.total ?? 0;
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    } finally {
      busy = false;
    }
  }

  onMount(refresh);

  // ─── Filters (client-side) ──────────────────────────────────────
  let filterPipeline = '';
  let filterError = '';
  let filterWindow: 'all' | '1h' | '24h' | '7d' = 'all';

  $: pipelineOptions = (() => {
    const set = new Set<string>();
    for (const e of entries) if (e.pipeline_id) set.add(e.pipeline_id);
    const opts = Array.from(set)
      .sort()
      .map((id) => ({ value: id, label: id }));
    return [{ value: '', label: t($locale, 'dlq.filter.allPipelines') }, ...opts];
  })();
  $: windowOptions = [
    { value: 'all', label: t($locale, 'dlq.filter.window.all') },
    { value: '1h', label: t($locale, 'dlq.filter.window.1h') },
    { value: '24h', label: t($locale, 'dlq.filter.window.24h') },
    { value: '7d', label: t($locale, 'dlq.filter.window.7d') }
  ];

  function withinWindow(createdAt: string, win: typeof filterWindow): boolean {
    if (win === 'all') return true;
    const ts = Date.parse(createdAt);
    if (Number.isNaN(ts)) return true;
    const now = Date.now();
    const ms = win === '1h' ? 3600_000 : win === '24h' ? 86_400_000 : 7 * 86_400_000;
    return now - ts <= ms;
  }

  $: filtered = entries.filter((e) => {
    if (filterPipeline && e.pipeline_id !== filterPipeline) return false;
    if (filterError && !e.error_reason.toLowerCase().includes(filterError.toLowerCase())) return false;
    if (!withinWindow(e.created_at, filterWindow)) return false;
    return true;
  });

  function clearFilters() {
    filterPipeline = '';
    filterError = '';
    filterWindow = 'all';
  }

  // ─── Selection (bulk) ───────────────────────────────────────────
  let selected = new Set<string>();
  function toggleOne(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    selected = next;
  }
  function toggleAllVisible() {
    const visibleIds = filtered.map((e) => e.id);
    const allOn = visibleIds.every((id) => selected.has(id));
    const next = new Set(selected);
    if (allOn) {
      visibleIds.forEach((id) => next.delete(id));
    } else {
      visibleIds.forEach((id) => next.add(id));
    }
    selected = next;
  }
  $: allVisibleSelected =
    filtered.length > 0 && filtered.every((e) => selected.has(e.id));
  $: someVisibleSelected =
    !allVisibleSelected && filtered.some((e) => selected.has(e.id));

  // ─── Per-row actions ────────────────────────────────────────────
  let pendingDeleteId: string | null = null;
  let deleting = false;

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

  // ─── Bulk actions ───────────────────────────────────────────────
  let bulkAction: 'retry' | 'delete' | null = null;
  let bulkBusy = false;

  function askBulkRetry() {
    if (selected.size === 0) return;
    bulkAction = 'retry';
  }
  function askBulkDelete() {
    if (selected.size === 0) return;
    bulkAction = 'delete';
  }
  async function confirmBulk() {
    if (!bulkAction) return;
    bulkBusy = true;
    const ids = Array.from(selected);
    try {
      // Parallel — the per-id endpoints handle individually and there's
      // no server-side bulk endpoint. Use Promise.allSettled so one
      // failure doesn't abort the rest.
      const results = await Promise.allSettled(
        ids.map((id) =>
          bulkAction === 'retry' ? api.post(`/v1/dlq/${id}/retry`) : api.del(`/v1/dlq/${id}`)
        )
      );
      const failures = results.filter((r) => r.status === 'rejected').length;
      if (failures > 0) {
        error = `${failures}/${ids.length} ${bulkAction} failed`;
      }
      selected = new Set();
      bulkAction = null;
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || `${bulkAction} failed`;
    } finally {
      bulkBusy = false;
    }
  }

  // ─── Detail drawer ──────────────────────────────────────────────
  let viewing: DLQEntry | null = null;
  let copied = false;
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;

  function openDetail(e: DLQEntry) {
    viewing = e;
    copied = false;
  }
  function closeDetail() {
    viewing = null;
    if (copiedTimer) clearTimeout(copiedTimer);
  }

  /**
   * Decode the base64 payload to UTF-8. Falls back to a hex/byte
   * preview when the bytes aren't valid UTF-8 (binary payloads — IBM
   * MQ RFH2 headers and the like).
   */
  function decodePayload(b64: string): { text: string; binary: boolean; bytes: number } {
    try {
      const raw = atob(b64);
      // Try to detect binary by counting control chars (excluding \n \r \t)
      let ctrl = 0;
      for (let i = 0; i < raw.length; i++) {
        const c = raw.charCodeAt(i);
        if (c < 32 && c !== 9 && c !== 10 && c !== 13) ctrl++;
      }
      const binary = ctrl > raw.length * 0.05;
      if (binary) {
        const hex = Array.from(raw.slice(0, 512))
          .map((c) => c.charCodeAt(0).toString(16).padStart(2, '0'))
          .join(' ');
        return { text: hex, binary: true, bytes: raw.length };
      }
      // Try to pretty-print JSON; leave as-is if it's not JSON.
      try {
        const parsed = JSON.parse(raw);
        return { text: JSON.stringify(parsed, null, 2), binary: false, bytes: raw.length };
      } catch {
        return { text: raw, binary: false, bytes: raw.length };
      }
    } catch {
      return { text: '(undecodable)', binary: false, bytes: 0 };
    }
  }

  async function copyPayload() {
    if (!viewing) return;
    const { text } = decodePayload(viewing.original_msg);
    try {
      await navigator.clipboard?.writeText(text);
      copied = true;
      if (copiedTimer) clearTimeout(copiedTimer);
      copiedTimer = setTimeout(() => (copied = false), 1600);
    } catch {
      // clipboard API can be blocked in some browsers — surface inline
      copied = false;
    }
  }

  $: pages = Math.max(1, Math.ceil(total / perPage));
  $: anyFilter =
    filterPipeline !== '' || filterError !== '' || filterWindow !== 'all';
</script>

<div class="space-y-6 max-w-6xl">
  <div class="flex items-baseline justify-between">
    <h2 class="text-2xl font-semibold" style="color: var(--text)">{t($locale, 'dlq.title')}</h2>
    <Button variant="ghost" on:click={refresh} loading={busy}>
      {t($locale, 'common.refresh')}
    </Button>
  </div>

  {#if error}
    <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
  {/if}

  <!-- ─── Filters ───────────────────────────────────────────────── -->
  <Card>
    <div class="dlq-filters">
      <div class="dlq-filter-field">
        <Select
          bind:value={filterPipeline}
          options={pipelineOptions}
          label={t($locale, 'dlq.filter.pipeline')}
        />
      </div>
      <div class="dlq-filter-field">
        <Input
          bind:value={filterError}
          label={t($locale, 'dlq.filter.error')}
          placeholder={t($locale, 'dlq.filter.errorPlaceholder')}
        />
      </div>
      <div class="dlq-filter-field">
        <Select
          bind:value={filterWindow}
          options={windowOptions}
          label={t($locale, 'dlq.filter.window')}
        />
      </div>
      {#if anyFilter}
        <div class="dlq-filter-field dlq-filter-clear">
          <Button variant="ghost" on:click={clearFilters}>
            {t($locale, 'dlq.filter.clear')}
          </Button>
        </div>
      {/if}
    </div>
  </Card>

  <!-- ─── Bulk action bar ───────────────────────────────────────── -->
  {#if selected.size > 0}
    <div class="dlq-bulk-bar">
      <span class="dlq-bulk-count">
        <Badge variant="neutral">{selected.size}</Badge>
        {t($locale, 'dlq.bulk.selected')}
      </span>
      <div class="flex gap-2">
        <Button variant="ghost" on:click={() => (selected = new Set())}>
          {t($locale, 'dlq.bulk.clear')}
        </Button>
        <Button variant="outline" on:click={askBulkDelete}>
          {t($locale, 'dlq.bulk.deleteAll')}
        </Button>
        <Button on:click={askBulkRetry}>{t($locale, 'dlq.bulk.retryAll')}</Button>
      </div>
    </div>
  {/if}

  <Card>
    {#if entries.length === 0}
      <p style="color: var(--text-muted)">{t($locale, 'dlq.empty')}</p>
    {:else if filtered.length === 0}
      <p style="color: var(--text-muted)">{t($locale, 'dlq.emptyFiltered')}</p>
    {:else}
      <table class="table dlq-table">
        <thead>
          <tr>
            <th class="dlq-checkbox-cell">
              <input
                type="checkbox"
                checked={allVisibleSelected}
                indeterminate={someVisibleSelected}
                on:change={toggleAllVisible}
                aria-label="Select all visible"
              />
            </th>
            <th>{t($locale, 'common.when')}</th>
            <th>{t($locale, 'dlq.pipeline')}</th>
            <th>{t($locale, 'dlq.sourceQueue')}</th>
            <th>{t($locale, 'common.reason')}</th>
            <th>{t($locale, 'dlq.retries')}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each filtered as e (e.id)}
            <tr class:dlq-selected={selected.has(e.id)}>
              <td class="dlq-checkbox-cell">
                <input
                  type="checkbox"
                  checked={selected.has(e.id)}
                  on:change={() => toggleOne(e.id)}
                  on:click|stopPropagation
                  aria-label="Select message {e.id}"
                />
              </td>
              <td style="color: var(--text-muted)">
                <button class="dlq-row-button" type="button" on:click={() => openDetail(e)}>
                  {e.created_at}
                </button>
              </td>
              <td style="color: var(--text)">
                <button class="dlq-row-button" type="button" on:click={() => openDetail(e)}>
                  {e.pipeline_id || '—'}
                </button>
              </td>
              <td style="color: var(--text-muted)">{e.source_queue || '—'}</td>
              <td style="color: var(--text)">
                <button class="dlq-row-button" type="button" on:click={() => openDetail(e)}>
                  {e.error_reason}
                </button>
              </td>
              <td>
                <Badge variant={e.retry_count > 0 ? 'warning' : 'neutral'}>
                  {e.retry_count}
                </Badge>
              </td>
              <td>
                <div class="flex gap-2 justify-end">
                  <Button variant="ghost" on:click={() => retry(e.id)}>
                    {t($locale, 'common.retry')}
                  </Button>
                  <Button variant="outline" on:click={() => askRemove(e.id)}>
                    {t($locale, 'common.delete')}
                  </Button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>

      {#if pages > 1}
        <div class="flex items-center justify-between mt-4">
          <span class="text-xs" style="color: var(--text-muted)">
            {pageNum} / {pages} · {total}
          </span>
          <div class="flex gap-2">
            <Button
              variant="ghost"
              disabled={pageNum <= 1}
              on:click={() => {
                pageNum--;
                refresh();
              }}
            >
              {t($locale, 'common.previous')}
            </Button>
            <Button
              variant="ghost"
              disabled={pageNum >= pages}
              on:click={() => {
                pageNum++;
                refresh();
              }}
            >
              {t($locale, 'common.next')}
            </Button>
          </div>
        </div>
      {/if}
    {/if}
  </Card>
</div>

<!-- ─── Per-row delete confirm ──────────────────────────────────── -->
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

<!-- ─── Bulk confirm ────────────────────────────────────────────── -->
<Dialog
  open={bulkAction !== null}
  title={t($locale, 'common.confirmDelete')}
  confirmLabel={bulkAction === 'retry'
    ? t($locale, 'common.retry')
    : t($locale, 'common.delete')}
  cancelLabel={t($locale, 'common.cancel')}
  busy={bulkBusy}
  on:cancel={() => (bulkAction = null)}
  on:confirm={confirmBulk}
>
  <p>
    {bulkAction === 'retry'
      ? t($locale, 'dlq.bulk.confirmRetry')
      : t($locale, 'dlq.bulk.confirmDelete')}
  </p>
  <p class="mt-2" style="color: var(--text-muted); font-size: 13px;">
    {selected.size} {t($locale, 'dlq.bulk.selected')}.
  </p>
</Dialog>

<!-- ─── Detail drawer ───────────────────────────────────────────── -->
{#if viewing}
  {@const decoded = decodePayload(viewing.original_msg)}
  {@const viewingId = viewing.id}
  <div
    class="dlq-scrim"
    role="presentation"
    on:click={closeDetail}
    on:keydown={(e) => e.key === 'Escape' && closeDetail()}
  ></div>
  <aside class="dlq-drawer" aria-label={t($locale, 'dlq.detail.title')}>
    <div class="dlq-drawer-head">
      <div>
        <p class="section-heading">{t($locale, 'dlq.detail.title')}</p>
        <p class="text-xs mt-1" style="color: var(--text-muted)">
          {viewing.pipeline_id || '—'} · {viewing.source_queue || '—'}
        </p>
      </div>
      <Button variant="ghost" on:click={closeDetail}>{t($locale, 'dlq.detail.close')}</Button>
    </div>

    <div class="dlq-drawer-body">
      <div class="dlq-meta">
        <div class="dlq-meta-row">
          <span class="dlq-meta-label">{t($locale, 'dlq.detail.id')}</span>
          <code class="dlq-meta-value">{viewing.id}</code>
        </div>
        <div class="dlq-meta-row">
          <span class="dlq-meta-label">{t($locale, 'dlq.detail.created')}</span>
          <span class="dlq-meta-value">{viewing.created_at}</span>
        </div>
        {#if viewing.last_retry_at}
          <div class="dlq-meta-row">
            <span class="dlq-meta-label">{t($locale, 'dlq.detail.lastRetry')}</span>
            <span class="dlq-meta-value">{viewing.last_retry_at}</span>
          </div>
        {/if}
        <div class="dlq-meta-row">
          <span class="dlq-meta-label">{t($locale, 'dlq.retries')}</span>
          <Badge variant={viewing.retry_count > 0 ? 'warning' : 'neutral'}>
            {viewing.retry_count}
          </Badge>
        </div>
      </div>

      <div class="mt-4">
        <p class="section-heading">{t($locale, 'common.reason')}</p>
        <pre class="dlq-error">{viewing.error_reason}</pre>
      </div>

      <div class="mt-4">
        <div class="flex items-center justify-between">
          <p class="section-heading">
            {t($locale, 'dlq.payload')}
            <span style="color: var(--text-muted); font-weight: 400; font-size: 12px;">
              · {decoded.bytes} {t($locale, 'dlq.detail.bytes')}
            </span>
          </p>
          <div class="flex items-center gap-2">
            {#if copied}
              <Badge variant="success">{t($locale, 'dlq.detail.copied')}</Badge>
            {/if}
            <Button variant="ghost" on:click={copyPayload}>
              {t($locale, 'dlq.detail.copy')}
            </Button>
          </div>
        </div>
        {#if decoded.binary}
          <p class="text-xs mt-1" style="color: var(--text-muted)">
            {t($locale, 'dlq.detail.binary')}
          </p>
        {/if}
        <pre class="dlq-payload" class:dlq-payload-binary={decoded.binary}>{decoded.text}</pre>
      </div>
    </div>

    <div class="dlq-drawer-foot">
      <Button variant="outline" on:click={() => askRemove(viewingId)}>
        {t($locale, 'common.delete')}
      </Button>
      <Button on:click={() => retry(viewingId)}>{t($locale, 'common.retry')}</Button>
    </div>
  </aside>
{/if}

<style>
  td:last-child {
    text-align: end;
  }

  /* ─── Filter bar ─────────────────────────────────────────────── */
  .dlq-filters {
    display: grid;
    grid-template-columns: 1fr;
    gap: 12px;
  }
  @media (min-width: 720px) {
    .dlq-filters {
      grid-template-columns: 1fr 2fr 1fr auto;
      align-items: end;
    }
  }
  .dlq-filter-field {
    min-width: 0;
  }
  .dlq-filter-clear {
    align-self: end;
  }

  /* ─── Bulk action bar ───────────────────────────────────────── */
  .dlq-bulk-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 14px;
    border: 1px solid var(--primary);
    border-radius: 12px;
    background: color-mix(in srgb, var(--primary) 8%, transparent);
  }
  .dlq-bulk-count {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    color: var(--text);
    font-size: 14px;
  }

  /* ─── Table ──────────────────────────────────────────────────── */
  .dlq-checkbox-cell {
    inline-size: 36px;
    text-align: center;
  }
  .dlq-checkbox-cell input {
    accent-color: var(--primary);
    cursor: pointer;
  }
  .dlq-selected {
    background: color-mix(in srgb, var(--primary) 5%, transparent);
  }
  /*
   * Make the timestamp / pipeline / reason cells act as the row-detail
   * trigger. Kept as <button> children rather than a row-level click
   * so checkbox + action buttons remain independently clickable.
   */
  .dlq-row-button {
    background: transparent;
    border: none;
    padding: 0;
    margin: 0;
    color: inherit;
    font: inherit;
    text-align: start;
    cursor: pointer;
  }
  .dlq-row-button:hover {
    color: var(--primary);
    text-decoration: underline;
    text-underline-offset: 3px;
  }

  /* ─── Detail drawer ─────────────────────────────────────────── */
  .dlq-scrim {
    position: fixed;
    inset: 0;
    background: var(--dialog-scrim);
    z-index: 49;
  }
  .dlq-drawer {
    position: fixed;
    inset-block-start: 0;
    inset-inline-end: 0;
    inline-size: min(620px, 92vw);
    block-size: 100vh;
    background: var(--surface);
    border-inline-start: 1px solid var(--border);
    box-shadow: -12px 0 32px rgba(0, 0, 0, 0.25);
    z-index: 50;
    display: flex;
    flex-direction: column;
  }
  .dlq-drawer-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    padding: 16px 20px;
    border-bottom: 1px solid var(--border);
  }
  .dlq-drawer-body {
    flex: 1;
    overflow-y: auto;
    padding: 16px 20px;
  }
  .dlq-drawer-foot {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    padding: 12px 20px;
    border-top: 1px solid var(--border);
  }
  .dlq-meta {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 12px;
    align-items: center;
  }
  .dlq-meta-row {
    display: contents;
  }
  .dlq-meta-label {
    color: var(--text-muted);
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .dlq-meta-value {
    color: var(--text);
    font-size: 13px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    word-break: break-all;
  }
  .dlq-error {
    margin-top: 6px;
    color: var(--danger);
    background: var(--danger-bg);
    border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent);
    border-radius: 12px;
    padding: 10px 12px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .dlq-payload {
    margin-top: 6px;
    color: var(--text);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 10px 12px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 1.45;
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 50vh;
    overflow: auto;
  }
  .dlq-payload-binary {
    word-break: break-all;
    color: var(--text-muted);
  }
</style>
