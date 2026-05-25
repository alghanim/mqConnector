<!--
  VersionRail — left-rail revisions list for the Pipeline Studio.

  Layout:

      ┌──────────────────────────────┐
      │ ▼ Revisions   (12)   Compare │
      ├──────────────────────────────┤
      │ #12  alice · 3m ago     [Live]│
      │      "tighten DLQ retry"      │
      ├──────────────────────────────┤
      │ #11  bob   · 2h ago    [Draft]│
      │      "add validate stage"     │
      ├──────────────────────────────┤
      │ … etc                        │
      └──────────────────────────────┘

  Collapsed (~32px): one-line header w/ chevron + count.

  Selection model — local component state, two slots:
    primary       — single-click; intended target of the Compare button.
    secondary     — Cmd/Ctrl-click; the *against* side of the comparison.
                    Falls back to deployedRev when not set.
    Re-clicking the primary deselects.

  Compare action:
    - Two primaries chosen: compare primary vs secondary.
    - Only primary chosen: compare primary vs live (deployedRev).
    Dispatches `compare {from, to, diff}` to the parent; the parent
    threads the diff into the studio store via setComparison.

  Row context menu (kebab):
    - Compare to live      → same as the Compare button against live.
    - Deploy this          → opens DeployDialog kind='deploy'.
    - Rollback to this     → opens DeployDialog kind='rollback'.
    - View only            → selects without opening compare.

  Long-list optimisation: ≥ 50 entries, switch to VirtualTable for
  windowed rendering. The dock-sized list usually fits ~12 rows; the
  cutover keeps memory bounded for long-lived pipelines.
-->
<script lang="ts">
  import { createEventDispatcher, onDestroy, onMount } from 'svelte';
  import { studio, type PipelineRevision, type StudioStateData } from '$lib/stores/studio';
  import { api, type ApiError } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Badge from '$lib/components/Badge.svelte';
  import VirtualTable from '$lib/components/VirtualTable.svelte';
  import { MoreVertical, ChevronDown, ChevronRight, GitCompare, History } from 'lucide-svelte';

  const STORAGE_KEY = 'mqc.studio.versionrail.collapsed';
  // Cutover for virtual rendering. Matches the editor's other heuristics
  // (DLQ uses 50 too) — below this the simple list is more accessible
  // because every row is reachable via Tab without a manual scroll.
  const VIRTUAL_THRESHOLD = 50;
  // Row height for the virtual table. MUST equal .row's rendered height
  // (see the style block below) or the windowing miscomputes spacers.
  const ROW_HEIGHT = 64;

  const dispatch = createEventDispatcher<{
    compare: { from: number; to: number; diff: unknown };
    rollback: { rev: number };
    deploy: { rev: number };
  }>();

  let s: StudioStateData;
  const unsub = studio.subscribe((v) => (s = v));
  onDestroy(unsub);

  let collapsed = false;
  // Selection state — primary is the row the user single-clicked;
  // secondary is the Cmd/Ctrl-clicked row, used as the *against* side
  // of the comparison when set.
  let primarySelected: number | null = null;
  let secondarySelected: number | null = null;
  // Kebab menu — only one open at a time. We close on outside click via
  // a window listener attached when a menu opens.
  let openMenu: number | null = null;
  // Loading state for the in-flight diff fetch. Disables the Compare
  // button so a double-click doesn't fire two requests.
  let comparing = false;
  let comparisonError: string | null = null;

  onMount(() => {
    try {
      const v = localStorage.getItem(STORAGE_KEY);
      if (v === '1') collapsed = true;
    } catch {
      /* localStorage blocked — fall back to default */
    }
    if (typeof window === 'undefined') return;
    const onClickAway = () => {
      openMenu = null;
    };
    window.addEventListener('click', onClickAway);
    return () => {
      window.removeEventListener('click', onClickAway);
    };
  });

  function setCollapsed(next: boolean) {
    collapsed = next;
    try {
      localStorage.setItem(STORAGE_KEY, next ? '1' : '0');
    } catch {
      /* no-op */
    }
  }

  // Public method — CommandPalette ("Studio: Compare to live") expands
  // the rail and stages the latest revision for compare.
  export function expandForCompare(): void {
    setCollapsed(false);
    if (s?.latestRev) {
      primarySelected = s.latestRev.revision_number;
      secondarySelected = null;
    }
  }

  // ─── Derived helpers ───────────────────────────────────────────────
  $: revisions = s?.revisions ?? [];
  $: deployedRevNum = s?.deployedRev?.revision_number ?? null;
  $: latestRevNum = s?.latestRev?.revision_number ?? null;
  $: useVirtual = revisions.length >= VIRTUAL_THRESHOLD;
  $: canCompare = primarySelected !== null && !comparing;

  // Relative time formatter. Coarse buckets — minutes/hours/days — match
  // the rest of the app (PageHeader/RouteHealthMatrix use the same
  // approach). For long pipelines the row's tooltip carries the ISO
  // timestamp so the operator can still get a precise value.
  function relativeTime(iso: string | undefined | null): string {
    if (!iso) return '';
    const then = new Date(iso).getTime();
    if (Number.isNaN(then)) return iso ?? '';
    const diff = Math.max(0, Date.now() - then);
    const sec = Math.floor(diff / 1000);
    if (sec < 60) return `${sec}s ago`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ago`;
    const hr = Math.floor(min / 60);
    if (hr < 48) return `${hr}h ago`;
    const day = Math.floor(hr / 24);
    return `${day}d ago`;
  }

  function isLive(r: PipelineRevision): boolean {
    return deployedRevNum !== null && r.revision_number === deployedRevNum;
  }
  function isDraft(r: PipelineRevision): boolean {
    return latestRevNum !== null && r.revision_number === latestRevNum && !isLive(r);
  }

  function onRowClick(e: MouseEvent, r: PipelineRevision) {
    e.stopPropagation();
    if (e.metaKey || e.ctrlKey) {
      // Secondary toggle. Clicking the row that's already secondary
      // clears it; clicking a new row replaces.
      secondarySelected = secondarySelected === r.revision_number ? null : r.revision_number;
      return;
    }
    if (primarySelected === r.revision_number) {
      primarySelected = null;
      return;
    }
    primarySelected = r.revision_number;
  }

  function onKebabClick(e: MouseEvent, r: PipelineRevision) {
    e.stopPropagation();
    openMenu = openMenu === r.revision_number ? null : r.revision_number;
  }

  // Keyboard handler for the row's role="button" hit area. Enter/Space
  // mirror a click; we synthesise a MouseEvent-like detail so the
  // single onRowClick handler can branch on metaKey/ctrlKey. (We treat
  // Cmd/Ctrl+Enter as secondary-select to match the click semantics.)
  function onRowKey(e: KeyboardEvent, r: PipelineRevision) {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    e.preventDefault();
    const synthetic = {
      stopPropagation: () => {},
      metaKey: e.metaKey,
      ctrlKey: e.ctrlKey
    } as unknown as MouseEvent;
    onRowClick(synthetic, r);
  }

  // Compare against another revision. The diff fetch path is the same
  // for both the toolbar button and the kebab "Compare to live" entry.
  async function runCompare(from: number, to: number) {
    if (!s?.pipelineId) return;
    if (from === to) return;
    comparing = true;
    comparisonError = null;
    try {
      // The backend lifts `diff` from a wrapper envelope; we only need
      // the inner SnapshotDiff for the viewer.
      const res = await api.get<{ from: number; to: number; diff: unknown }>(
        `/v1/pipelines/${s.pipelineId}/revisions/${from}/diff?against=${to}`
      );
      dispatch('compare', { from, to, diff: res.diff });
    } catch (err) {
      const msg = (err as ApiError)?.message ?? 'failed to load diff';
      comparisonError = msg;
    } finally {
      comparing = false;
    }
  }

  function onCompareClick() {
    if (primarySelected === null) return;
    const against = secondarySelected ?? deployedRevNum ?? latestRevNum ?? primarySelected;
    void runCompare(primarySelected, against);
  }

  function onMenuCompare(r: PipelineRevision) {
    openMenu = null;
    const live = deployedRevNum ?? latestRevNum ?? r.revision_number;
    void runCompare(r.revision_number, live);
  }

  function onMenuRollback(r: PipelineRevision) {
    openMenu = null;
    dispatch('rollback', { rev: r.revision_number });
  }

  function onMenuDeploy(r: PipelineRevision) {
    openMenu = null;
    dispatch('deploy', { rev: r.revision_number });
  }

  function onMenuView(r: PipelineRevision) {
    openMenu = null;
    primarySelected = r.revision_number;
    secondarySelected = null;
  }
</script>

<section class="rail" class:is-collapsed={collapsed} aria-label={t($locale, 'studio.versions.heading')}>
  <header class="rail-head">
    <button
      type="button"
      class="rail-toggle"
      aria-expanded={!collapsed}
      aria-controls="rail-body"
      aria-label={t($locale, 'studio.versions.collapseLabel')}
      on:click={() => setCollapsed(!collapsed)}
    >
      <span class="rail-toggle-chevron" aria-hidden="true">
        {#if collapsed}
          <ChevronRight size={14} />
        {:else}
          <ChevronDown size={14} />
        {/if}
      </span>
      <span class="rail-title">{t($locale, 'studio.versions.heading')}</span>
      {#if revisions.length > 0}
        <span class="rail-count" aria-label={`${revisions.length} revisions`}>
          {revisions.length}
        </span>
      {/if}
    </button>
    {#if !collapsed}
      <button
        type="button"
        class="rail-compare-btn"
        on:click={onCompareClick}
        disabled={!canCompare}
        aria-label={t($locale, 'studio.versions.compare')}
      >
        <GitCompare size={12} aria-hidden="true" />
        <span>{t($locale, 'studio.versions.compare')}</span>
      </button>
    {/if}
  </header>

  {#if !collapsed}
    <div id="rail-body" class="rail-body">
      {#if comparisonError}
        <p class="rail-error" role="alert">{comparisonError}</p>
      {/if}

      {#if revisions.length === 0}
        <!-- Empty state — replaces the previous generic illustration +
             floating placeholder dots with a focused History icon, a
             one-line headline, and concrete next-step copy that names
             the Deploy button. -->
        <div class="rail-empty">
          <span class="rail-empty-icon" aria-hidden="true">
            <History size={22} />
          </span>
          <p class="rail-empty-title">{t($locale, 'studio.versions.empty.title')}</p>
          <p class="rail-empty-body">{t($locale, 'studio.versions.empty.body')}</p>
        </div>
      {:else if useVirtual}
        <div class="rail-virtual">
          <VirtualTable
            items={revisions}
            rowHeight={ROW_HEIGHT}
            viewportHeight={360}
            keyFn={(r) => r.revision_number}
          >
            <svelte:fragment slot="row" let:item>
              {@const r = item}
              <div
                class="row"
                class:is-primary={primarySelected === r.revision_number}
                class:is-secondary={secondarySelected === r.revision_number}
              >
                <!-- The row body is the primary interactive surface — a
                     <div role="button"> rather than a real <button> so
                     it can host the kebab + menu without an SSR-illegal
                     nested-button structure. Keyboard parity via the
                     onRowKey handler. -->
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <div
                  class="row-hit"
                  role="button"
                  tabindex="0"
                  aria-pressed={primarySelected === r.revision_number}
                  on:click={(e) => onRowClick(e, r)}
                  on:keydown={(e) => onRowKey(e, r)}
                >
                  <div class="row-top">
                    <span class="row-num">#{r.revision_number}</span>
                    <span class="row-meta">
                      {r.author_username || r.author_sub || 'unknown'}
                      <span class="row-dot" aria-hidden="true">·</span>
                      <time title={r.created_at}>{relativeTime(r.created_at)}</time>
                    </span>
                    {#if isLive(r)}
                      <Badge variant="success">{t($locale, 'studio.versions.badge.live')}</Badge>
                    {:else if isDraft(r)}
                      <Badge variant="warning">{t($locale, 'studio.versions.badge.draft')}</Badge>
                    {/if}
                  </div>
                  <p class="row-summary" title={r.change_summary}>
                    {r.change_summary || '—'}
                  </p>
                </div>
                <button
                  type="button"
                  class="row-kebab"
                  aria-label={t($locale, 'studio.versions.menu.label')}
                  on:click={(e) => onKebabClick(e, r)}
                >
                  <MoreVertical size={14} />
                </button>
                {#if openMenu === r.revision_number}
                  <div class="row-menu" role="menu" tabindex="-1">
                    <button type="button" role="menuitem" on:click={() => onMenuCompare(r)}>
                      {t($locale, 'studio.versions.compareToLive')}
                    </button>
                    <button type="button" role="menuitem" on:click={() => onMenuDeploy(r)}>
                      {t($locale, 'studio.versions.deployThis')}
                    </button>
                    <button type="button" role="menuitem" on:click={() => onMenuRollback(r)}>
                      {t($locale, 'studio.versions.rollbackTo')}
                    </button>
                    <button type="button" role="menuitem" on:click={() => onMenuView(r)}>
                      {t($locale, 'studio.versions.view')}
                    </button>
                  </div>
                {/if}
              </div>
            </svelte:fragment>
          </VirtualTable>
        </div>
      {:else}
        <ul class="rail-list" role="list">
          {#each revisions as r (r.revision_number)}
            <li>
              <div
                class="row"
                class:is-primary={primarySelected === r.revision_number}
                class:is-secondary={secondarySelected === r.revision_number}
              >
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <div
                  class="row-hit"
                  role="button"
                  tabindex="0"
                  aria-pressed={primarySelected === r.revision_number}
                  on:click={(e) => onRowClick(e, r)}
                  on:keydown={(e) => onRowKey(e, r)}
                >
                  <div class="row-top">
                    <span class="row-num">#{r.revision_number}</span>
                    <span class="row-meta">
                      {r.author_username || r.author_sub || 'unknown'}
                      <span class="row-dot" aria-hidden="true">·</span>
                      <time title={r.created_at}>{relativeTime(r.created_at)}</time>
                    </span>
                    {#if isLive(r)}
                      <Badge variant="success">{t($locale, 'studio.versions.badge.live')}</Badge>
                    {:else if isDraft(r)}
                      <Badge variant="warning">{t($locale, 'studio.versions.badge.draft')}</Badge>
                    {/if}
                  </div>
                  <p class="row-summary" title={r.change_summary}>
                    {r.change_summary || '—'}
                  </p>
                </div>
                <button
                  type="button"
                  class="row-kebab"
                  aria-label={t($locale, 'studio.versions.menu.label')}
                  on:click={(e) => onKebabClick(e, r)}
                >
                  <MoreVertical size={14} />
                </button>
                {#if openMenu === r.revision_number}
                  <div class="row-menu" role="menu" tabindex="-1">
                    <button type="button" role="menuitem" on:click={() => onMenuCompare(r)}>
                      {t($locale, 'studio.versions.compareToLive')}
                    </button>
                    <button type="button" role="menuitem" on:click={() => onMenuDeploy(r)}>
                      {t($locale, 'studio.versions.deployThis')}
                    </button>
                    <button type="button" role="menuitem" on:click={() => onMenuRollback(r)}>
                      {t($locale, 'studio.versions.rollbackTo')}
                    </button>
                    <button type="button" role="menuitem" on:click={() => onMenuView(r)}>
                      {t($locale, 'studio.versions.view')}
                    </button>
                  </div>
                {/if}
              </div>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
</section>

<style>
  /*
   * Rail container — mirrors DryRunDock's collapsible idiom (chevron
   * + transition + persisted state) so collapsing one panel feels the
   * same as collapsing the other.
   */
  .rail {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 0.5rem;
    min-block-size: 2.25rem;
    max-block-size: 32rem;
    overflow: hidden;
  }
  .rail.is-collapsed {
    max-block-size: 2.25rem;
    padding-block: 0.375rem;
  }
  .rail-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .rail-toggle {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    background: transparent;
    border: none;
    color: var(--text);
    font-size: 0.75rem;
    font-weight: 600;
    cursor: pointer;
    padding-block: 0.25rem;
    padding-inline: 0.25rem;
  }
  .rail-toggle:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
    border-radius: 4px;
  }
  .rail-toggle-chevron {
    color: var(--text-tertiary);
    display: inline-flex;
  }
  .rail-title {
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-size: 0.6875rem;
  }
  .rail-count {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-inline-size: 1.25rem;
    block-size: 1.125rem;
    padding-inline: 0.375rem;
    background: var(--surface-2);
    color: var(--text-muted);
    border-radius: 999px;
    font-size: 0.6875rem;
    font-weight: 600;
  }
  .rail-compare-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding-block: 0.25rem;
    padding-inline: 0.5rem;
    border-radius: 8px;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text);
    font-size: 0.6875rem;
    font-weight: 600;
    cursor: pointer;
    transition: border-color 120ms, background-color 120ms;
  }
  .rail-compare-btn:hover:not(:disabled) {
    border-color: var(--accent);
    background: var(--surface-high);
  }
  .rail-compare-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .rail-body {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    overflow-y: auto;
    min-block-size: 0;
  }
  .rail-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .rail-virtual {
    min-block-size: 0;
    display: flex;
    flex-direction: column;
  }

  .rail-error {
    margin: 0;
    padding: 0.375rem 0.5rem;
    background: var(--danger-bg);
    color: var(--danger);
    border: 1px solid var(--danger);
    border-radius: 8px;
    font-size: 0.75rem;
  }

  /* Row — fixed height so VirtualTable maths line up.
     Box-sizing: border-box accounts for the 1px border. The row hosts
     two interactive descendants (the .row-hit hit area and the .row-kebab
     menu trigger) — keeping them as siblings avoids the nested-button
     hydration-mismatch SSR warning. */
  .row {
    position: relative;
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.25rem;
    box-sizing: border-box;
    inline-size: 100%;
    block-size: 64px;
    padding: 0.375rem 0.5rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    transition: border-color 120ms, background-color 120ms;
  }
  .row:hover {
    border-color: var(--accent);
  }
  .row.is-primary {
    border-color: var(--accent);
    background: var(--surface-high);
  }
  .row.is-secondary {
    border-style: dashed;
    border-color: var(--info);
  }
  .row-hit {
    display: flex;
    flex-direction: column;
    gap: 0.125rem;
    cursor: pointer;
    text-align: start;
    min-inline-size: 0;
  }
  .row-hit:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
    border-radius: 4px;
  }
  .row-top {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    min-inline-size: 0;
  }
  .row-num {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-weight: 700;
    font-size: 0.75rem;
    color: var(--text);
  }
  .row-meta {
    flex: 1;
    min-inline-size: 0;
    font-size: 0.6875rem;
    color: var(--text-muted);
    display: inline-flex;
    align-items: baseline;
    gap: 0.25rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row-dot {
    color: var(--text-tertiary);
  }
  .row-summary {
    margin: 0;
    font-size: 0.75rem;
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row-kebab {
    align-self: start;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    inline-size: 1.5rem;
    block-size: 1.5rem;
    border-radius: 4px;
    background: transparent;
    border: 0;
    color: var(--text-muted);
    cursor: pointer;
    padding: 0;
  }
  .row-kebab:hover {
    background: var(--surface-high);
    color: var(--text);
  }
  .row-kebab:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .row-menu {
    position: absolute;
    inset-block-start: 100%;
    inset-inline-end: 0.25rem;
    z-index: 5;
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    box-shadow: var(--dialog-shadow);
    min-inline-size: 11rem;
    padding: 0.25rem;
  }
  .row-menu button {
    display: block;
    inline-size: 100%;
    padding: 0.375rem 0.5rem;
    background: transparent;
    border: 0;
    color: var(--text);
    text-align: start;
    cursor: pointer;
    font: inherit;
    font-size: 0.75rem;
    border-radius: 4px;
  }
  .row-menu button:hover {
    background: var(--surface-2);
  }

  /* Empty state for the rail. Replaces the generic illustration with a
     compact icon + headline + helper so the operator immediately knows
     what to do (deploy). Sized to fit inside the rail's max-block-size
     without scrolling. */
  .rail-empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.375rem;
    padding-block: 1rem;
    padding-inline: 0.75rem;
    text-align: center;
  }
  .rail-empty-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    inline-size: 36px;
    block-size: 36px;
    border-radius: 999px;
    background: var(--primary-container);
    color: var(--on-primary-container);
  }
  .rail-empty-title {
    margin: 0;
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--text);
  }
  .rail-empty-body {
    margin: 0;
    font-size: 0.6875rem;
    color: var(--text-muted);
    line-height: 1.4;
    max-inline-size: 18rem;
  }
</style>
