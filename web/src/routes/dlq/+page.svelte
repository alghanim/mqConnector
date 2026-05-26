<!--
  /dlq — Dead-letter queue console.

  Two-tabbed UI (Wave 3 T5):

    [Clusters]   — failures grouped by error-text fingerprint.
                   Three-pane console: cluster list | cluster detail | action drawer.
                   Default tab; powered by GET /api/v1/dlq/clusters (+ ?ai=names).
                   The right-pane drawer wires up replay-sim + diff-against
                   + retry/delete.

    [All entries] — the legacy flat-list view kept around for the operator
                   that wants to triage by row rather than pattern. Same
                   filter row, same bulk affordances, same detail drawer
                   as Phase 6 — moved verbatim into an {#if activeTab === 'all'}
                   block so it only mounts when actually selected.

  URL params:
    ?pipeline=<id>  — pre-applies the pipeline filter on both tabs (preserved
                      from the legacy page so the /metrics drilldown link
                      keeps working).
    ?tab=all        — opens directly on the All entries tab (lets external
                      surfaces deep-link straight into the flat list).
-->
<script lang="ts">
  import { onDestroy, onMount, tick } from 'svelte';
  import { page } from '$app/stores';
  import {
    api,
    type Connection,
    type DLQCluster,
    type DLQClustersResponse,
    type DLQDiffResponse,
    type DLQEntry,
    type DLQReplaySimResponse,
    type Pipeline
  } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import {
    loadCatalogues,
    pipelineLabel,
    pipelineLabelOrDeleted
  } from '$lib/stores/catalogue';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatChip from '$lib/components/StatChip.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { toasts } from '$lib/stores/toasts';
  import { RotateCw, RefreshCw } from 'lucide-svelte';

  import ClusterCard from '$lib/components/dlq/ClusterCard.svelte';
  import ClusterDetail from '$lib/components/dlq/ClusterDetail.svelte';
  import ActionDrawer from '$lib/components/dlq/ActionDrawer.svelte';

  // ─── Tabs ────────────────────────────────────────────────────────
  // Default to clusters — that's the surface this task ships. The
  // All-entries tab is reachable via the strip or ?tab=all deep link.
  let activeTab: 'clusters' | 'all' = 'clusters';

  // ─── Shared: catalogues ──────────────────────────────────────────
  // Both tabs render pipeline labels; one catalogue load serves both.
  let pipelineMap = new Map<string, Pipeline>();
  let connectionMap = new Map<string, Connection>();
  $: deletedLabel = t($locale, 'dash.pipelines.deleted');
  async function refreshCatalogues(): Promise<void> {
    const c = await loadCatalogues('dlq');
    pipelineMap = c.pipelines;
    connectionMap = c.connections;
  }

  // ─── Shared: relative-time formatter ─────────────────────────────
  // (kept here for the All-entries flat-list rows — the cluster
  // components ship their own copy.)
  function formatRelative(ts: string | undefined, lc: 'en' | 'ar'): string {
    if (!ts) return '';
    const at = Date.parse(ts);
    if (Number.isNaN(at)) return '';
    const diff = (Date.now() - at) / 1000;
    const abs = Math.abs(diff);
    const past = diff >= 0;
    type Unit = 'second' | 'minute' | 'hour' | 'day';
    let value: number;
    let unit: Unit;
    if (abs < 60) {
      value = Math.max(1, Math.round(abs));
      unit = 'second';
    } else if (abs < 3600) {
      value = Math.round(abs / 60);
      unit = 'minute';
    } else if (abs < 86_400) {
      value = Math.round(abs / 3600);
      unit = 'hour';
    } else {
      value = Math.round(abs / 86_400);
      unit = 'day';
    }
    const key = `time.${unit}${value === 1 ? '' : 's'}`;
    const unitLabel = t(lc, key);
    return past
      ? t(lc, 'time.ago').replace('{n}', String(value)).replace('{unit}', unitLabel)
      : t(lc, 'time.in').replace('{n}', String(value)).replace('{unit}', unitLabel);
  }

  // ─── CLUSTERS TAB ────────────────────────────────────────────────

  let clusters: DLQCluster[] = [];
  let clustersTotal = 0;
  let clustersReturned = 0;
  let clustersLoading = false;
  let clustersError = '';
  let clustersGeneratedAt: string | null = null;
  let aiNames = false;

  // Filter row drives both the request URL and the empty-state copy.
  let clusterPipelineId = '';
  // Stored as a string so it binds cleanly into <Select> (which only
  // round-trips string values). We coerce on the way into URLSearchParams.
  let clusterSinceHours = '168'; // last week
  let clusterMinCount = 1;

  // Selection.
  let selectedCluster: DLQCluster | null = null;
  let selectedEntry: DLQEntry | null = null;
  let recentEntries = new Map<string, DLQEntry>();
  let representativeEntry: DLQEntry | null = null;

  // Replay sim + diff state per selected entry.
  let replaySim: DLQReplaySimResponse | null = null;
  let busySim = false;
  let diffResult: DLQDiffResponse | null = null;
  let busyDiff = false;
  let compareId = '';

  // Build the option list for the cluster pipeline filter. We harvest
  // from `pipelinesAffected` across the loaded clusters so the dropdown
  // stays bounded to pipelines that actually have failures.
  $: clusterPipelineOptions = (() => {
    const seen = new Set<string>();
    for (const c of clusters) for (const p of c.pipelines_affected) seen.add(p);
    if (clusterPipelineId) seen.add(clusterPipelineId);
    const opts = Array.from(seen)
      .map((id) => ({ value: id, label: pipelineLabel(id, pipelineMap) }))
      .sort((a, b) => a.label.localeCompare(b.label));
    return [
      { value: '', label: t($locale, 'dlq.filter.allPipelines') },
      ...opts
    ];
  })();

  $: clusterSinceOptions = [
    { value: '24', label: t($locale, 'dlq.clusters.filter.since.day') },
    { value: '168', label: t($locale, 'dlq.clusters.filter.since.week') },
    { value: '720', label: t($locale, 'dlq.clusters.filter.since.month') }
  ];

  async function fetchClusters(): Promise<void> {
    clustersLoading = true;
    try {
      const params = new URLSearchParams();
      if (clusterPipelineId) params.set('pipeline_id', clusterPipelineId);
      const sinceHoursNum = Number(clusterSinceHours);
      if (Number.isFinite(sinceHoursNum) && sinceHoursNum > 0) {
        const since = new Date(Date.now() - sinceHoursNum * 3_600_000).toISOString();
        params.set('since', since);
      }
      if (clusterMinCount > 1) params.set('min_count', String(clusterMinCount));
      params.set('limit', '100');
      if (aiNames) params.set('ai', 'names');
      const res = await api.get<DLQClustersResponse>(
        `/v1/dlq/clusters?${params.toString()}`
      );
      clusters = res.clusters ?? [];
      clustersTotal = res.total ?? 0;
      clustersReturned = res.returned ?? clusters.length;
      clustersGeneratedAt = res.generated_at ?? null;
      clustersError = '';

      // Selection bookkeeping: if the previously-selected cluster is no
      // longer in the new list, drop it. Otherwise re-bind to the fresh
      // object so its fields (count, last_seen, ai_name) update live.
      if (selectedCluster) {
        const next = clusters.find(
          (c) => c.fingerprint === selectedCluster!.fingerprint
        );
        if (next) {
          selectedCluster = next;
        } else {
          selectedCluster = null;
          selectedEntry = null;
          representativeEntry = null;
          recentEntries = new Map();
        }
      }
    } catch (e: unknown) {
      clustersError = (e as { message?: string }).message || 'failed to load clusters';
      clusters = [];
      clustersTotal = 0;
      clustersReturned = 0;
    } finally {
      clustersLoading = false;
    }
  }

  // Debounced refetch when filters or AI toggle change.
  let clusterRefetchTimer: ReturnType<typeof setTimeout> | undefined;
  function scheduleClusterRefetch() {
    if (clusterRefetchTimer) clearTimeout(clusterRefetchTimer);
    clusterRefetchTimer = setTimeout(() => {
      void fetchClusters();
    }, 300);
  }
  // We trip the debounce on any of: pipeline filter, since window,
  // min-count threshold, AI-names toggle. Wrapped so reactivity sees
  // a single dependency-list invocation.
  $: trackClusterFilters(clusterPipelineId, clusterSinceHours, clusterMinCount, aiNames);
  function trackClusterFilters(
    _p: string,
    _h: string,
    _m: number,
    _ai: boolean
  ) {
    scheduleClusterRefetch();
  }

  // Cluster selection — fetch the representative entry + the recent_ids
  // pile so the timeline can render names. We deliberately fetch all
  // recent_ids in parallel (server lookup is cheap and the list is
  // bounded to ~10 ids).
  async function selectCluster(event: CustomEvent<{ cluster: DLQCluster }>) {
    const cluster = event.detail.cluster;
    selectedCluster = cluster;
    // Reset per-entry state so the drawer doesn't show stale sim/diff
    // bound to the previous cluster's entry.
    selectedEntry = null;
    representativeEntry = null;
    recentEntries = new Map();
    replaySim = null;
    diffResult = null;
    compareId = '';

    try {
      const rep = await api.get<DLQEntry>(`/v1/dlq/${cluster.representative_id}`);
      representativeEntry = rep;
      // The representative is also the default action-drawer target.
      selectedEntry = rep;
      // Seed the recent-entries map so the timeline + drawer have it.
      const seed = new Map<string, DLQEntry>();
      seed.set(rep.id, rep);
      recentEntries = seed;
    } catch (e: unknown) {
      // The representative may have been purged between the clusters
      // call and the row fetch; surface but don't blow away the
      // cluster header (it's still valid metadata).
      toasts.error(
        t($locale, 'dlq.clusters.detail.representative'),
        (e as { message?: string }).message || ''
      );
    }

    // Fan out the rest of the recent_ids. Each one is independent —
    // a failure on one shouldn't strand the others. allSettled keeps
    // partial timelines rendering.
    const others = cluster.recent_ids.filter(
      (id) => id !== cluster.representative_id
    );
    if (others.length === 0) return;
    const results = await Promise.allSettled(
      others.map((id) => api.get<DLQEntry>(`/v1/dlq/${id}`))
    );
    const next = new Map(recentEntries);
    results.forEach((r, idx) => {
      if (r.status === 'fulfilled') {
        next.set(others[idx], r.value);
      }
    });
    recentEntries = next;
  }

  function selectTimelineEntry(event: CustomEvent<{ entry: DLQEntry }>) {
    selectedEntry = event.detail.entry;
    // Reset sim/diff — the operator clicked a different row.
    replaySim = null;
    diffResult = null;
    compareId = '';
  }

  // ─── Drawer handlers ────────────────────────────────────────────
  async function onRunReplaySim(event: CustomEvent<{ id: string }>) {
    busySim = true;
    try {
      const res = await api.post<DLQReplaySimResponse>(
        `/v1/dlq/${event.detail.id}/replay-sim`
      );
      replaySim = res;
    } catch (e: unknown) {
      toasts.error(
        t($locale, 'dlq.clusters.drawer.replaySim'),
        (e as { message?: string }).message || ''
      );
    } finally {
      busySim = false;
    }
  }

  async function onPickCompare(event: CustomEvent<{ againstId: string }>) {
    if (!selectedEntry) return;
    busyDiff = true;
    try {
      const res = await api.get<DLQDiffResponse>(
        `/v1/dlq/${selectedEntry.id}/diff?against=${encodeURIComponent(event.detail.againstId)}`
      );
      diffResult = res;
    } catch (e: unknown) {
      toasts.error(
        t($locale, 'dlq.clusters.drawer.compare'),
        (e as { message?: string }).message || ''
      );
    } finally {
      busyDiff = false;
    }
  }

  async function onDrawerRetry(event: CustomEvent<{ id: string }>) {
    try {
      await api.post(`/v1/dlq/${event.detail.id}/retry`);
      toasts.success(t($locale, 'common.retry'), event.detail.id.slice(0, 8));
      // Refetch clusters — the entry may have left the DLQ, which can
      // cascade to the cluster count / last_seen.
      await fetchClusters();
    } catch (e: unknown) {
      toasts.error(
        t($locale, 'common.retry'),
        (e as { message?: string }).message || ''
      );
    }
  }

  // The drawer's delete request opens a confirm dialog (re-use the
  // existing per-row dialog state below).
  function onDrawerAskDelete(event: CustomEvent<{ id: string }>) {
    pendingDeleteId = event.detail.id;
  }

  // ─── Compare-list excluding the focused entry ───────────────────
  // The drawer wants "other recent ids" — its own id should never
  // appear in the compare dropdown.
  $: otherRecentIds = (() => {
    if (!selectedCluster || !selectedEntry) return [] as string[];
    return selectedCluster.recent_ids.filter((id) => id !== selectedEntry!.id);
  })();

  function resetClusterFilters() {
    clusterPipelineId = '';
    clusterSinceHours = '168';
    clusterMinCount = 1;
  }

  $: hasClusterFilters =
    clusterPipelineId !== '' || clusterSinceHours !== '168' || clusterMinCount !== 1;

  // ─── ALL ENTRIES TAB (legacy flat list — preserved verbatim) ─────

  let entries: DLQEntry[] = [];
  let total = 0;
  let pageNum = 1;
  const perPage = 500;
  let listError = '';
  let busy = false;

  async function refresh() {
    busy = true;
    try {
      const params = new URLSearchParams({
        page: String(pageNum),
        per_page: String(perPage)
      });
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
      listError = '';
    } catch (e: unknown) {
      listError = (e as { message?: string }).message || 'failed to load';
    } finally {
      busy = false;
    }
  }

  function windowToSince(win: 'all' | '1h' | '24h' | '7d'): string | undefined {
    if (win === 'all') return undefined;
    const ms = win === '1h' ? 3600_000 : win === '24h' ? 86_400_000 : 7 * 86_400_000;
    return new Date(Date.now() - ms).toISOString();
  }

  let filterPipeline = '';
  let filterError = '';
  let filterWindow: 'all' | '1h' | '24h' | '7d' = 'all';

  $: pipelineOptions = (() => {
    const seen = new Set<string>();
    for (const e of entries) if (e.pipeline_id) seen.add(e.pipeline_id);
    if (filterPipeline) seen.add(filterPipeline);
    const opts = Array.from(seen)
      .map((id) => ({ value: id, label: pipelineLabel(id, pipelineMap) }))
      .sort((a, b) => a.label.localeCompare(b.label));
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
    if (filterError && !e.error_reason.toLowerCase().includes(filterError.toLowerCase()))
      return false;
    if (!withinWindow(e.created_at, filterWindow)) return false;
    return true;
  });

  function clearFilters() {
    filterPipeline = '';
    filterError = '';
    filterWindow = 'all';
  }

  // Debounced refetch — only schedule while the All entries tab is
  // mounted, so a Clusters-tab session doesn't trigger background
  // /v1/dlq calls the operator can't see.
  let refetchTimer: ReturnType<typeof setTimeout> | undefined;
  $: scheduleRefetch(filterPipeline, filterError, filterWindow, activeTab);
  function scheduleRefetch(
    _p: string,
    _e: string,
    _w: typeof filterWindow,
    tab: typeof activeTab
  ) {
    if (refetchTimer) clearTimeout(refetchTimer);
    if (tab !== 'all') return;
    refetchTimer = setTimeout(() => {
      pageNum = 1;
      refresh();
    }, 250);
  }

  // ─── Selection (bulk) — All entries tab ─────────────────────────
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
      listError = (e as { message?: string }).message || 'retry failed';
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
      // If the entry was the one focused in the cluster drawer, drop
      // the selection so the right panel returns to the empty state.
      if (selectedEntry?.id === pendingDeleteId) {
        selectedEntry = null;
        replaySim = null;
        diffResult = null;
        compareId = '';
      }
      pendingDeleteId = null;
      // Refresh both surfaces in case the delete spans tabs.
      if (activeTab === 'all') {
        await refresh();
      } else {
        await fetchClusters();
      }
    } catch (e: unknown) {
      listError = (e as { message?: string }).message || 'delete failed';
    } finally {
      deleting = false;
    }
  }

  // ─── Bulk actions ───────────────────────────────────────────────
  let bulkAction: 'retry' | 'delete' | null = null;
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
          listError = `${res.failed}/${res.succeeded + res.failed} ${bulkAction} failed`;
        }
      } else {
        const ids = Array.from(selected);
        const results = await Promise.allSettled(
          ids.map((id) =>
            bulkAction === 'retry'
              ? api.post(`/v1/dlq/${id}/retry`)
              : api.del(`/v1/dlq/${id}`)
          )
        );
        const failures = results.filter((r) => r.status === 'rejected').length;
        if (failures > 0) {
          listError = `${failures}/${ids.length} ${bulkAction} failed`;
        }
      }
      selected = new Set();
      bulkAction = null;
      await refresh();
      await refreshGroups();
    } catch (e: unknown) {
      listError = (e as { message?: string }).message || `${bulkAction} failed`;
    } finally {
      bulkBusy = false;
    }
  }

  $: filterActive = !!filterPipeline || !!filterError || filterWindow !== 'all';

  // ─── Top error patterns ─────────────────────────────────────────
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
  function adoptGroupAsFilter(pattern: string) {
    filterError = pattern;
    refresh();
  }

  // ─── Detail drawer (All entries tab) ────────────────────────────
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

  $: if (typeof document !== 'undefined') {
    if (viewing) {
      document.documentElement.style.overflow = 'hidden';
    } else {
      document.documentElement.style.overflow = '';
    }
  }

  function decodePayload(b64: string): { text: string; binary: boolean; bytes: number } {
    try {
      const raw = atob(b64);
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
      copied = false;
    }
  }

  $: pages = Math.max(1, Math.ceil(total / perPage));
  $: anyFilter = filterPipeline !== '' || filterError !== '' || filterWindow !== 'all';

  // ─── Tab change side effects ────────────────────────────────────
  // Switching INTO All entries: kick off the legacy /v1/dlq fetch
  // (the debounced reactive statement above will also fire — but we
  // explicitly load here so the page paints rows immediately rather
  // than after the 250 ms debounce window).
  $: if (activeTab === 'all' && entries.length === 0 && !busy) {
    void refresh();
    void refreshGroups();
  }

  // ─── Mount / unmount ─────────────────────────────────────────────
  onMount(async () => {
    // URL deep-link: ?pipeline=<id> applies to both tabs.
    const fromPipeline = $page.url.searchParams.get('pipeline');
    if (fromPipeline) {
      filterPipeline = fromPipeline;
      clusterPipelineId = fromPipeline;
    }
    // ?tab=all opens directly on the legacy list.
    const fromTab = $page.url.searchParams.get('tab');
    if (fromTab === 'all') activeTab = 'all';

    void refreshCatalogues();
    // Always kick off the cluster fetch — it's the default tab and
    // the count badge in the strip needs to populate even when the
    // operator opened the page on ?tab=all.
    void fetchClusters();
  });

  onDestroy(() => {
    if (refetchTimer) clearTimeout(refetchTimer);
    if (clusterRefetchTimer) clearTimeout(clusterRefetchTimer);
    if (copiedTimer) clearTimeout(copiedTimer);
    if (typeof document !== 'undefined') document.documentElement.style.overflow = '';
  });

  // ─── Header count: meaningful per active tab ────────────────────
  // Clusters tab shows cluster count; All entries shows row total.
  $: headerCount = activeTab === 'clusters' ? clustersTotal : total;
</script>

<div class="dlq-shell">
  <PageHeader
    title={t($locale, 'dlq.title')}
    subtitle={t($locale, 'dlq.pageSubtitle')}
    count={headerCount}
  >
    <svelte:fragment slot="primary">
      <Switch bind:checked={aiNames} label={t($locale, 'dlq.clusters.aiToggle')} />
      <Button
        variant="ghost"
        on:click={() => (activeTab === 'clusters' ? fetchClusters() : refresh())}
        loading={activeTab === 'clusters' ? clustersLoading : busy}
      >
        <RotateCw size={14} aria-hidden="true" />
        <span class="ms-1">{t($locale, 'common.refresh')}</span>
      </Button>
    </svelte:fragment>

    <svelte:fragment slot="stats">
      {#if activeTab === 'clusters'}
        <StatChip
          label={t($locale, 'dlq.clusters.count')}
          value={clustersTotal}
          tone={clustersTotal > 0 ? 'danger' : 'default'}
        />
      {:else}
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
      {/if}
    </svelte:fragment>
  </PageHeader>

  <!-- ─── Tab strip ─────────────────────────────────────────────── -->
  <div class="dlq-tabs" role="tablist" aria-label={t($locale, 'dlq.title')}>
    <button
      type="button"
      role="tab"
      class="dlq-tab"
      class:is-active={activeTab === 'clusters'}
      aria-selected={activeTab === 'clusters'}
      on:click={() => (activeTab = 'clusters')}
    >
      <span>{t($locale, 'dlq.tab.clusters')}</span>
      <Badge variant="neutral">{clustersTotal}</Badge>
    </button>
    <button
      type="button"
      role="tab"
      class="dlq-tab"
      class:is-active={activeTab === 'all'}
      aria-selected={activeTab === 'all'}
      on:click={() => (activeTab = 'all')}
    >
      <span>{t($locale, 'dlq.tab.all')}</span>
    </button>
  </div>

  {#if activeTab === 'clusters'}
    <!-- ─── CLUSTERS TAB ──────────────────────────────────────── -->
    {#if clustersError}
      <Alert variant="error" dismissible on:dismiss={() => (clustersError = '')}>
        {clustersError}
      </Alert>
    {/if}

    <div class="cluster-console" role="tabpanel" aria-label="Clusters">
      <!-- ── Left pane: filter row + cluster list ── -->
      <aside class="cluster-left" aria-label="Cluster filters and list">
        <Card>
          <div class="cluster-filter-row">
            <Select
              bind:value={clusterPipelineId}
              options={clusterPipelineOptions}
              label={t($locale, 'dlq.clusters.filter.pipeline')}
            />
            <Select
              bind:value={clusterSinceHours}
              options={clusterSinceOptions}
              label={t($locale, 'dlq.clusters.filter.since')}
            />
            <Input
              type="number"
              bind:value={clusterMinCount}
              label={t($locale, 'dlq.clusters.filter.minCount')}
            />
          </div>
          {#if hasClusterFilters}
            <div class="cluster-filter-reset">
              <Button variant="ghost" on:click={resetClusterFilters}>
                {t($locale, 'dlq.clusters.empty.filtered.reset')}
              </Button>
            </div>
          {/if}
        </Card>

        <div class="cluster-list" aria-label="Clusters">
          {#if clustersLoading && clusters.length === 0}
            <Skeleton height="64px" />
            <Skeleton height="64px" />
            <Skeleton height="64px" />
          {:else if clusters.length === 0}
            <Card>
              {#if hasClusterFilters}
                <p class="cluster-empty-title">
                  {t($locale, 'dlq.clusters.empty.filtered.title')}
                </p>
                <p class="cluster-empty-body">
                  {t($locale, 'dlq.clusters.empty.filtered.body')}
                </p>
                <div class="cluster-empty-actions">
                  <Button variant="outline" on:click={resetClusterFilters}>
                    {t($locale, 'dlq.clusters.empty.filtered.reset')}
                  </Button>
                </div>
              {:else}
                <p class="cluster-empty-title">
                  {t($locale, 'dlq.clusters.empty.title')}
                </p>
                <p class="cluster-empty-body">
                  {t($locale, 'dlq.clusters.empty.body')}
                </p>
              {/if}
            </Card>
          {:else}
            {#each clusters as cluster (cluster.fingerprint)}
              <ClusterCard
                {cluster}
                selected={selectedCluster?.fingerprint === cluster.fingerprint}
                on:select={selectCluster}
              />
            {/each}
          {/if}
        </div>
      </aside>

      <!-- ── Center pane: cluster detail ── -->
      <section class="cluster-center" aria-label="Cluster detail">
        {#if selectedCluster}
          <ClusterDetail
            cluster={selectedCluster}
            representative={representativeEntry}
            {recentEntries}
            selectedEntryId={selectedEntry?.id ?? null}
            {pipelineMap}
            {deletedLabel}
            on:selectEntry={selectTimelineEntry}
          />
        {:else if clusters.length === 0 && !clustersLoading}
          <EmptyState
            illustration="dlq"
            title={t($locale, 'empty.dlq.title')}
            body={t($locale, 'empty.dlq.body')}
          />
        {:else}
          <Card>
            <p class="cluster-empty-title">
              {t($locale, 'dlq.clusters.empty.selected.title')}
            </p>
            <p class="cluster-empty-body">
              {t($locale, 'dlq.clusters.empty.selected.body')}
            </p>
          </Card>
        {/if}
      </section>

      <!-- ── Right pane: action drawer ── -->
      <aside class="cluster-right" aria-label="Action drawer">
        <ActionDrawer
          entry={selectedEntry}
          {otherRecentIds}
          {replaySim}
          diff={diffResult}
          {busySim}
          {busyDiff}
          bind:compareId
          on:runReplaySim={onRunReplaySim}
          on:pickCompare={onPickCompare}
          on:retry={onDrawerRetry}
          on:askDelete={onDrawerAskDelete}
        />
      </aside>
    </div>
  {:else}
    <!-- ─── ALL ENTRIES TAB (legacy flat list) ───────────────── -->
    <div role="tabpanel" aria-label="All entries" class="space-y-6">
      {#if listError}
        <Alert variant="error" dismissible on:dismiss={() => (listError = '')}>
          {listError}
        </Alert>
      {/if}

      <!-- Filters -->
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

      <!-- Top error patterns -->
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

      <!-- Bulk action bar -->
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
          >
            <svelte:fragment slot="helper">
              <a class="dlq-empty-link" href="/metrics">
                {t($locale, 'empty.dlq.subtitle')}
              </a>
            </svelte:fragment>
          </EmptyState>
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
                {@const pipeName = pipelineLabelOrDeleted(
                  e.pipeline_id,
                  pipelineMap,
                  deletedLabel
                )}
                {@const relWhen = formatRelative(e.created_at, $locale)}
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
                  <td style="color: var(--text-muted)">
                    <button
                      class="dlq-row-button"
                      type="button"
                      on:click={() => openDetail(e)}
                      aria-label="{t(
                        $locale,
                        'dlq.detail.viewRow'
                      )} — {pipeName}, {e.created_at}, {e.error_reason}"
                    >
                      <time datetime={e.created_at} title={e.created_at}>
                        {#if relWhen}
                          <span class="dlq-when-rel">{relWhen}</span>
                          <span class="dlq-when-abs">{e.created_at}</span>
                        {:else}
                          <span>{e.created_at}</span>
                        {/if}
                      </time>
                    </button>
                  </td>
                  <td style="color: var(--text)" title={e.pipeline_id ?? ''}>{pipeName}</td>
                  <td style="color: var(--text-muted)">{e.source_queue || '—'}</td>
                  <td style="color: var(--text)">{e.error_reason}</td>
                  <td>
                    {#if e.retry_count > 0}
                      <span class="dlq-retry-pill" title={e.last_retry_at ?? ''}>
                        <RefreshCw size={11} aria-hidden="true" />
                        {t($locale, 'dlq.row.retryBadge').replace(
                          '{n}',
                          String(e.retry_count)
                        )}
                      </span>
                    {:else}
                      <Badge variant="neutral">0</Badge>
                    {/if}
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
  {/if}
</div>

<!-- ─── Per-row delete confirm (shared between drawer + flat list) ── -->
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

<!-- ─── Bulk confirm (All entries tab only) ────────────────────── -->
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

<!-- ─── Legacy detail drawer (All entries tab) ─────────────────── -->
<svelte:window on:keydown={onDrawerKey} />

{#if viewing}
  {@const decoded = decodePayload(viewing.original_msg)}
  {@const viewingId = viewing.id}
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
        <p id="dlq-detail-title" class="section-heading">
          {t($locale, 'dlq.detail.title')}
        </p>
        <p class="dlq-drawer-subtitle">
          <span class="dlq-drawer-pipe" title={viewing.pipeline_id ?? ''}>
            {pipelineLabelOrDeleted(viewing.pipeline_id, pipelineMap, deletedLabel)}
          </span>
          <span aria-hidden="true">·</span>
          <span class="dlq-drawer-queue">{viewing.source_queue || '—'}</span>
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
            <time class="dlq-meta-value" datetime={viewing.last_retry_at}>
              {viewing.last_retry_at}
            </time>
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
        <span class="sr-only" aria-live="polite">
          {copied ? t($locale, 'dlq.detail.copied') : ''}
        </span>
        {#if decoded.binary}
          <p class="text-xs mt-1" style="color: var(--text-muted)">
            {t($locale, 'dlq.detail.binary')}
          </p>
        {/if}
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
  .dlq-shell {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    /* The cluster console fills the available width — wider than the
       6xl cap on the legacy page because we now show three columns
       side-by-side instead of one column. */
    max-width: 1600px;
  }

  /* ─── Tab strip ───────────────────────────────────────────────
     Bottom-border on active, soft underline on hover. The badge is
     inline so the active tab carries its own count. */
  .dlq-tabs {
    display: inline-flex;
    align-items: center;
    gap: 1.5rem;
    border-block-end: 1px solid var(--border);
    margin-block-end: 0.25rem;
  }
  .dlq-tab {
    appearance: none;
    background: transparent;
    border: 0;
    color: var(--text-muted);
    font: inherit;
    font-size: 0.875rem;
    font-weight: 500;
    cursor: pointer;
    padding-block: 0.625rem;
    padding-inline: 0.125rem;
    margin-block-end: -1px;
    border-block-end: 2px solid transparent;
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    transition: color 120ms ease, border-color 120ms ease;
  }
  .dlq-tab:hover {
    color: var(--text);
    border-block-end-color: var(--border-strong);
  }
  .dlq-tab.is-active {
    color: var(--text);
    border-block-end-color: var(--accent);
  }
  .dlq-tab:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
    border-radius: 2px;
  }

  /* ─── Cluster console grid ─────────────────────────────────────
     Three columns: 300 (cluster list) | 1fr (detail) | 360 (drawer).
     Collapses to a single column under the breakpoint so the page
     stays usable on narrow viewports — at that width the operator
     gets the list first, then detail, then drawer, scrolled. */
  .cluster-console {
    display: grid;
    grid-template-columns: 300px minmax(0, 1fr) 360px;
    gap: 1rem;
    align-items: start;
  }
  @media (max-width: 1200px) {
    .cluster-console {
      grid-template-columns: minmax(0, 1fr);
    }
  }

  .cluster-left {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    min-inline-size: 0;
  }
  .cluster-center {
    min-inline-size: 0;
  }
  .cluster-right {
    min-inline-size: 0;
  }

  /* Filter row — stacks under the breakpoint. */
  .cluster-filter-row {
    display: grid;
    grid-template-columns: 1fr;
    gap: 0.5rem;
  }
  .cluster-filter-reset {
    margin-block-start: 0.5rem;
    display: flex;
    justify-content: flex-end;
  }

  .cluster-list {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    max-block-size: calc(100vh - 22rem);
    overflow-y: auto;
    /* Keep the scrollbar gutter reserved so a card hover doesn't
       shift the rest of the list as a scrollbar appears. */
    scrollbar-gutter: stable;
    padding-inline-end: 0.25rem;
  }

  .cluster-empty-title {
    margin: 0;
    color: var(--text);
    font-size: 0.875rem;
    font-weight: 600;
  }
  .cluster-empty-body {
    margin: 0.25rem 0 0;
    color: var(--text-muted);
    font-size: 0.75rem;
    line-height: 1.5;
  }
  .cluster-empty-actions {
    margin-block-start: 0.5rem;
    display: flex;
    justify-content: flex-end;
  }

  /* ─── All entries tab styles (preserved from the legacy page) ─── */
  td:last-child {
    text-align: end;
  }

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
  .dlq-selected {
    background: color-mix(in srgb, var(--primary) 14%, transparent);
    box-shadow: inset 3px 0 0 var(--primary);
  }
  :global([dir='rtl']) .dlq-selected {
    box-shadow: inset -3px 0 0 var(--primary);
  }
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

  .dlq-when-rel {
    display: block;
    color: var(--text);
    font-size: 13px;
    line-height: 1.2;
  }
  .dlq-when-abs {
    display: block;
    color: var(--text-muted);
    font-size: 11px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    line-height: 1.2;
    margin-block-start: 2px;
  }

  .dlq-retry-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding-block: 2px;
    padding-inline: 8px;
    border-radius: 999px;
    background: color-mix(in srgb, var(--warning) 14%, transparent);
    color: var(--warning);
    border: 1px solid color-mix(in srgb, var(--warning) 30%, transparent);
    font-size: 11px;
    font-weight: 500;
    line-height: 1.3;
  }

  .dlq-empty-link {
    color: var(--text-muted);
    text-decoration: none;
    border-block-end: 1px solid transparent;
    transition: color 120ms ease, border-color 120ms ease;
  }
  .dlq-empty-link:hover,
  .dlq-empty-link:focus-visible {
    color: var(--text);
    border-block-end-color: var(--border-strong, var(--border));
  }

  /* ─── Detail drawer (legacy) ─────────────────────────────────── */
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
  .dlq-drawer-subtitle {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-block-start: 4px;
    margin-block-end: 0;
    color: var(--text-muted);
    font-size: 12px;
  }
  .dlq-drawer-pipe {
    color: var(--text);
    font-weight: 500;
    max-inline-size: 16rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dlq-drawer-queue {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 11px;
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
