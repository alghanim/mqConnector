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
  import { onDestroy, onMount, tick } from 'svelte';
  import { api, type DLQEntry } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatChip from '$lib/components/StatChip.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { RotateCw, Plus } from 'lucide-svelte';

  // ─── List state ─────────────────────────────────────────────────
  let entries: DLQEntry[] = [];
  let total = 0;
  let pageNum = 1;
  // Page size is generous on purpose: during incident triage the
  // operator scrolls a wide window of failures rather than clicking
  // through pages. The CSS rule on .dlq-table tbody tr applies row
  // virtualization (content-visibility) so 500 rows render fast.
  const perPage = 500;
  let error = '';
  let busy = false;

  async function refresh() {
    busy = true;
    try {
      const params = new URLSearchParams({
        page: String(pageNum),
        per_page: String(perPage)
      });
      // Push the filter through to the server so pagination counts and
      // matches reflect the WHOLE table, not just the current page. The
      // client-side `filtered` is still derived from the loaded rows for
      // visual immediacy when the user is mid-keystroke.
      if (filterPipeline) params.set('pipeline_id', filterPipeline);
      if (filterError) params.set('error', filterError);
      const since = windowToSince(filterWindow);
      if (since) params.set('since', since);

      const res = await api.get<{
        page: number;
        per_page: number;
        total: number;
        items: DLQEntry[];
      }>(`/v1/dlq?${params.toString()}`);
      entries = res.items ?? [];
      total = res.total ?? 0;
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    } finally {
      busy = false;
    }
  }

  // Translates the UI's window selection into an RFC3339 lower-bound that
  // the server understands. "all" → undefined (no bound applied).
  function windowToSince(win: 'all' | '1h' | '24h' | '7d'): string | undefined {
    if (win === 'all') return undefined;
    const ms = win === '1h' ? 3600_000 : win === '24h' ? 86_400_000 : 7 * 86_400_000;
    return new Date(Date.now() - ms).toISOString();
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

  // Re-fetch when the user pauses typing. The reactive statement watches
  // the four filter inputs together so any change schedules a single
  // debounce window. Skipped during the initial refresh (busy=true) and
  // during page navigation (we want pageNum changes to fire immediately).
  let refetchTimer: ReturnType<typeof setTimeout> | undefined;
  $: scheduleRefetch(filterPipeline, filterError, filterWindow);
  function scheduleRefetch(_p: string, _e: string, _w: typeof filterWindow) {
    if (refetchTimer) clearTimeout(refetchTimer);
    refetchTimer = setTimeout(() => {
      pageNum = 1;
      refresh();
    }, 250);
  }
  onDestroy(() => {
    if (refetchTimer) clearTimeout(refetchTimer);
  });

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
  // bulkScope decides whether the confirm path hits the per-id
  // endpoints (selection-driven) or the server-side bulk endpoint
  // keyed on the active filter. The latter is the only way to reach
  // rows that don't fit in the current page.
  let bulkScope: 'selection' | 'filter' = 'selection';
  let bulkBusy = false;

  function askBulkRetry() {
    if (selected.size === 0) return;
    bulkScope = 'selection';
    bulkAction = 'retry';
  }
  function askBulkDelete() {
    if (selected.size === 0) return;
    bulkScope = 'selection';
    bulkAction = 'delete';
  }
  // ask*MatchingFilter targets every row that matches the current
  // filter (server-side), not just the visible-page selection. Only
  // active when at least one filter is set — bulk-deleting the entire
  // table by accident is exactly the misclick this guards against.
  function askBulkRetryMatching() {
    if (!filterActive) return;
    bulkScope = 'filter';
    bulkAction = 'retry';
  }
  function askBulkDeleteMatching() {
    if (!filterActive) return;
    bulkScope = 'filter';
    bulkAction = 'delete';
  }
  async function confirmBulk() {
    if (!bulkAction) return;
    bulkBusy = true;
    try {
      if (bulkScope === 'filter') {
        // Server-side bulk: one call, one transaction. Maps to the
        // new POST /v1/dlq/bulk/{retry,delete} endpoint that accepts
        // the same filter shape as the list endpoint.
        const since = windowToSince(filterWindow);
        const body: Record<string, unknown> = { max_rows: 1000 };
        if (filterPipeline) body.pipeline_id = filterPipeline;
        if (filterError) body.error = filterError;
        if (since) body.since = since;
        const res = await api.post<{
          succeeded: number;
          failed: number;
          failures?: Record<string, string>;
        }>(`/v1/dlq/bulk/${bulkAction}`, body);
        if (res.failed > 0) {
          error = `${res.failed}/${res.succeeded + res.failed} ${bulkAction} failed`;
        }
      } else {
        const ids = Array.from(selected);
        const results = await Promise.allSettled(
          ids.map((id) =>
            bulkAction === 'retry' ? api.post(`/v1/dlq/${id}/retry`) : api.del(`/v1/dlq/${id}`)
          )
        );
        const failures = results.filter((r) => r.status === 'rejected').length;
        if (failures > 0) {
          error = `${failures}/${ids.length} ${bulkAction} failed`;
        }
      }
      selected = new Set();
      bulkAction = null;
      await refresh();
      await refreshGroups();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || `${bulkAction} failed`;
    } finally {
      bulkBusy = false;
    }
  }

  $: filterActive = !!filterPipeline || !!filterError || filterWindow !== 'all';

  // ─── Top error patterns ─────────────────────────────────────────
  // Surfaces what's burning so an oncall can triage on pattern first,
  // rows second. Backed by GET /v1/dlq/groups — server-side aggregate,
  // so the answer is meaningful even when total >> visible window.
  type ErrorGroup = { pattern: string; count: number; oldest_at: string };
  let groups: ErrorGroup[] = [];
  async function refreshGroups() {
    try {
      const res = await api.get<{ items: ErrorGroup[] }>(`/v1/dlq/groups?limit=5`);
      groups = res.items ?? [];
    } catch {
      groups = [];
    }
  }
  onMount(refreshGroups);
  function adoptGroupAsFilter(pattern: string) {
    filterError = pattern;
    refresh();
  }

  // ─── Detail drawer ──────────────────────────────────────────────
  // The drawer is a side-anchored modal. We can't reuse the centred
  // Dialog primitive, so we replicate its a11y machinery here:
  //   - role="dialog" + aria-modal + aria-labelledby
  //   - Tab / Shift+Tab trap inside the drawer
  //   - Escape closes (unless a confirm Dialog is open on top)
  //   - Initial focus on the close button
  //   - Focus restored to the row trigger on close
  //   - <html> overflow locked while open so the page behind doesn't
  //     scroll under the drawer
  let viewing: DLQEntry | null = null;
  let copied = false;
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;
  let drawerEl: HTMLDivElement | null = null;
  let lastFocused: Element | null = null;

  function focusables(): HTMLElement[] {
    if (!drawerEl) return [];
    return Array.from(
      drawerEl.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
      )
    );
  }

  function openDetail(e: DLQEntry) {
    lastFocused = typeof document !== 'undefined' ? document.activeElement : null;
    viewing = e;
    copied = false;
    // First focusable in the drawer is the Close button.
    tick().then(() => focusables()[0]?.focus());
  }
  function closeDetail() {
    viewing = null;
    if (copiedTimer) clearTimeout(copiedTimer);
    if (lastFocused instanceof HTMLElement) {
      lastFocused.focus({ preventScroll: true });
    }
    lastFocused = null;
  }

  function onDrawerKey(e: KeyboardEvent) {
    if (!viewing) return;
    // Defer to a confirm Dialog when one is layered over the drawer —
    // its Escape handler should fire instead of ours.
    if (pendingDeleteId !== null || bulkAction !== null) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      closeDetail();
      return;
    }
    if (e.key !== 'Tab') return;
    const items = focusables();
    if (items.length === 0) {
      e.preventDefault();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }

  // Lock background scroll while the drawer is open.
  $: if (typeof document !== 'undefined') {
    if (viewing) {
      document.documentElement.style.overflow = 'hidden';
    } else {
      document.documentElement.style.overflow = '';
    }
  }

  onDestroy(() => {
    if (typeof document !== 'undefined') document.documentElement.style.overflow = '';
    if (copiedTimer) clearTimeout(copiedTimer);
  });

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
  <PageHeader
    title={t($locale, 'dlq.title')}
    subtitle={t($locale, 'dlq.pageSubtitle')}
    count={total}
  >
    <svelte:fragment slot="primary">
      <Button variant="ghost" on:click={refresh} loading={busy}>
        <RotateCw size={14} aria-hidden="true" />
        <span class="ms-1">{t($locale, 'common.refresh')}</span>
      </Button>
    </svelte:fragment>

    <svelte:fragment slot="stats">
      <StatChip
        label={t($locale, 'common.total')}
        value={total}
        tone={total > 0 ? 'danger' : 'default'}
      />
      {#if entries.length > 0}
        <StatChip
          label={t($locale, 'dlq.retries')}
          value={entries.filter((e) => e.retry_count > 0).length}
          tone="warning"
        />
      {/if}
    </svelte:fragment>
  </PageHeader>

  {#if error}
    <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
  {/if}

  <!-- ─── Filters ───────────────────────────────────────────────── -->
  <section aria-label={t($locale, 'dlq.filters')}>
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
  </section>

  <!-- ─── Top error patterns ──────────────────────────────────────
       Server-side aggregate (GET /v1/dlq/groups). Lets the operator
       triage by pattern before drilling into rows. Click a pattern
       to set it as the error filter. -->
  {#if groups.length > 0}
    <section class="dlq-groups" aria-label="DLQ top error patterns">
      <header class="dlq-groups-header">
        <h2>Top error patterns</h2>
        <span class="dlq-groups-hint">click to filter</span>
      </header>
      <ul class="dlq-groups-list">
        {#each groups as g}
          <li>
            <button
              type="button"
              class="dlq-group-pill"
              on:click={() => adoptGroupAsFilter(g.pattern.slice(0, 40))}
            >
              <span class="dlq-group-pattern" title={g.pattern}>{g.pattern}</span>
              <Badge variant="neutral">{g.count}</Badge>
            </button>
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  <!-- ─── Bulk action bar ───────────────────────────────────────── -->
  {#if selected.size > 0}
    <section class="dlq-bulk-bar" aria-label={t($locale, 'dlq.bulk.region')}>
      <span class="dlq-bulk-count" aria-live="polite">
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
    </section>
  {:else if filterActive && total > entries.length}
    <!-- When a filter is active and matches more rows than fit on the
         current page, surface the server-side bulk affordance — the
         only way to act on the entire match-set in one go. -->
    <section class="dlq-bulk-bar" aria-label="Bulk on filter">
      <span class="dlq-bulk-count" aria-live="polite">
        <Badge variant="neutral">{total}</Badge>
        rows match this filter — apply to all?
      </span>
      <div class="flex gap-2">
        <Button variant="outline" on:click={askBulkDeleteMatching}>
          Delete all matching
        </Button>
        <Button on:click={askBulkRetryMatching}>Retry all matching</Button>
      </div>
    </section>
  {/if}

  <Card>
    {#if entries.length === 0}
      <EmptyState
        illustration="dlq"
        title={t($locale, 'empty.dlq.title')}
        body={t($locale, 'empty.dlq.body')}
      />
    {:else if filtered.length === 0}
      <p style="color: var(--text-muted)">{t($locale, 'dlq.emptyFiltered')}</p>
    {:else}
      <table class="table dlq-table" aria-label={t($locale, 'dlq.title')}>
        <thead>
          <tr>
            <th class="dlq-checkbox-cell">
              <input
                type="checkbox"
                checked={allVisibleSelected}
                indeterminate={someVisibleSelected}
                on:change={toggleAllVisible}
                aria-checked={someVisibleSelected
                  ? 'mixed'
                  : allVisibleSelected
                    ? 'true'
                    : 'false'}
                aria-label={t($locale, 'dlq.selectAll')}
              />
            </th>
            <th>{t($locale, 'common.when')}</th>
            <th>{t($locale, 'dlq.pipeline')}</th>
            <th>{t($locale, 'dlq.sourceQueue')}</th>
            <th>{t($locale, 'common.reason')}</th>
            <th>{t($locale, 'dlq.retries')}</th>
            <th><span class="sr-only">{t($locale, 'common.actions')}</span></th>
          </tr>
        </thead>
        <tbody>
          {#each filtered as e (e.id)}
            <tr class:dlq-selected={selected.has(e.id)} aria-selected={selected.has(e.id)}>
              <td class="dlq-checkbox-cell">
                <input
                  type="checkbox"
                  checked={selected.has(e.id)}
                  on:change={() => toggleOne(e.id)}
                  on:click|stopPropagation
                  aria-label="{t($locale, 'dlq.selectRow')} {e.id}"
                />
              </td>
              <!--
                Single row-trigger button: it owns the row's "open detail"
                affordance for both mouse and keyboard. Its accessible
                name folds in pipeline + reason so a screen-reader user
                hears the row context in one announcement instead of
                tabbing through three redundant buttons.
              -->
              <td style="color: var(--text-muted)">
                <button
                  class="dlq-row-button"
                  type="button"
                  on:click={() => openDetail(e)}
                  aria-label="{t($locale, 'dlq.detail.viewRow')} — {e.pipeline_id ||
                    '—'}, {e.created_at}, {e.error_reason}"
                >
                  <time datetime={e.created_at}>{e.created_at}</time>
                </button>
              </td>
              <td style="color: var(--text)">{e.pipeline_id || '—'}</td>
              <td style="color: var(--text-muted)">{e.source_queue || '—'}</td>
              <td style="color: var(--text)">{e.error_reason}</td>
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
<!--
  Escape + Tab trap live on a single window listener so the handler
  fires regardless of which element inside the drawer has focus.
  Guarded by `viewing` inside onDrawerKey.
-->
<svelte:window on:keydown={onDrawerKey} />

{#if viewing}
  {@const decoded = decodePayload(viewing.original_msg)}
  {@const viewingId = viewing.id}
  <!-- Scrim — visual-only, click closes. aria-hidden because the
       dialog itself carries all semantics. -->
  <!-- svelte-ignore a11y-click-events-have-key-events -->
  <!-- svelte-ignore a11y-no-static-element-interactions -->
  <div class="dlq-scrim" aria-hidden="true" on:click={closeDetail}></div>
  <div
    bind:this={drawerEl}
    class="dlq-drawer"
    role="dialog"
    aria-modal="true"
    aria-labelledby="dlq-detail-title"
  >
    <div class="dlq-drawer-head">
      <div>
        <p id="dlq-detail-title" class="section-heading">{t($locale, 'dlq.detail.title')}</p>
        <p class="text-xs mt-1" style="color: var(--text-muted)">
          {viewing.pipeline_id || '—'} · {viewing.source_queue || '—'}
        </p>
      </div>
      <Button variant="ghost" on:click={closeDetail}>
        {t($locale, 'dlq.detail.close')}
      </Button>
    </div>

    <div class="dlq-drawer-body">
      <div class="dlq-meta">
        <div class="dlq-meta-row">
          <span class="dlq-meta-label">{t($locale, 'dlq.detail.id')}</span>
          <code class="dlq-meta-value">{viewing.id}</code>
        </div>
        <div class="dlq-meta-row">
          <span class="dlq-meta-label">{t($locale, 'dlq.detail.created')}</span>
          <time class="dlq-meta-value" datetime={viewing.created_at}>{viewing.created_at}</time>
        </div>
        {#if viewing.last_retry_at}
          <div class="dlq-meta-row">
            <span class="dlq-meta-label">{t($locale, 'dlq.detail.lastRetry')}</span>
            <time class="dlq-meta-value" datetime={viewing.last_retry_at}
              >{viewing.last_retry_at}</time
            >
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
        <!-- aria-live region for the copy confirmation. Visual badge
             above is for sighted users; this is the SR equivalent. -->
        <span class="sr-only" aria-live="polite">
          {copied ? t($locale, 'dlq.detail.copied') : ''}
        </span>
        {#if decoded.binary}
          <p class="text-xs mt-1" style="color: var(--text-muted)">
            {t($locale, 'dlq.detail.binary')}
          </p>
        {/if}
        <!-- role="region" + tabindex="0" gives keyboard users a focus
             stop so they can scroll the payload with arrow keys / Page
             Up/Down; SRs announce it as a labelled landmark. The lint
             rule fires on the <pre> element type regardless of the
             explicit role — a scrollable labelled region is a valid
             tab stop per WAI-ARIA APG. -->
        <!-- svelte-ignore a11y-no-noninteractive-tabindex -->
        <pre
          class="dlq-payload"
          class:dlq-payload-binary={decoded.binary}
          role="region"
          aria-label={t($locale, 'dlq.payload')}
          tabindex="0">{decoded.text}</pre>
      </div>
    </div>

    <div class="dlq-drawer-foot">
      <Button variant="outline" on:click={() => askRemove(viewingId)}>
        {t($locale, 'common.delete')}
      </Button>
      <Button on:click={() => retry(viewingId)}>{t($locale, 'common.retry')}</Button>
    </div>
  </div>
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

  /* ─── Top error patterns panel ──────────────────────────────── */
  .dlq-groups {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface-2);
  }
  .dlq-groups-header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 8px;
  }
  .dlq-groups-header h2 {
    font-size: 13px;
    font-weight: 600;
    margin: 0;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .dlq-groups-hint {
    font-size: 12px;
    color: var(--text-muted);
  }
  .dlq-groups-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .dlq-group-pill {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 999px;
    cursor: pointer;
    font: inherit;
    color: var(--text);
    max-width: 100%;
  }
  .dlq-group-pill:hover {
    border-color: var(--primary);
  }
  .dlq-group-pattern {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 36ch;
    font-size: 13px;
  }

  /* ─── Table ──────────────────────────────────────────────────── */
  /*
   * Row-level virtualization via the browser. content-visibility: auto
   * lets the engine skip layout + paint for rows outside the viewport;
   * contain-intrinsic-size reserves the right height so the scrollbar
   * doesn't jump as rows scroll into view. Together they keep DLQ snappy
   * up to a few thousand rows without a windowing library.
   *
   * For datasets beyond what content-visibility comfortably handles
   * (~5–10k rows), the proper fix is the VirtualTable component in
   * lib/components/VirtualTable.svelte. The pagination cap (per_page
   * <= 500) keeps us well below that here.
   */
  .dlq-table tbody tr {
    content-visibility: auto;
    contain-intrinsic-size: auto 48px;
  }
  .dlq-checkbox-cell {
    inline-size: 36px;
    text-align: center;
  }
  .dlq-checkbox-cell input {
    accent-color: var(--primary);
    cursor: pointer;
  }
  /*
   * Selected-row indicator: 14% gold tint gets us above the 3:1 non-text
   * contrast threshold vs. the unselected row; the inset stripe is a
   * second, redundant cue for low-vision users. Logical inset so the
   * stripe flips to the trailing edge under RTL.
   */
  .dlq-selected {
    background: color-mix(in srgb, var(--primary) 14%, transparent);
    box-shadow: inset 3px 0 0 var(--primary);
  }
  /* :global is required: [dir] is on <html>, outside this component's
     scope, so Svelte's scoped CSS otherwise drops the rule. */
  :global([dir='rtl']) .dlq-selected {
    box-shadow: inset -3px 0 0 var(--primary);
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
