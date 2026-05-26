<!--
  Command Palette — Cmd/Ctrl+K (or "/") opens a fuzzy launcher with
  three sections: Navigate, Actions, and resource search results.

  The palette is its own modal, focus-trapped, dismisses on Escape or
  click-outside. The arrow keys move the highlight; Enter executes.

  Resource search live-queries /api/v1/connections + /api/v1/pipelines
  + /api/v1/dlq for matches against the query. Result count is capped
  so the panel never grows tall; deeper search lives on each list page.
-->
<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { api, type Connection, type Pipeline, type DLQEntry } from '$lib/api';
  import { auth } from '$lib/stores/auth';
  import { tenants } from '$lib/stores/tenants';
  import { locale, t } from '$lib/stores/locale';
  import { theme } from '$lib/stores/theme';
  import { studio, studioState } from '$lib/stores/studio';
  import {
    Search,
    LayoutDashboard,
    Plug,
    Workflow,
    GitFork,
    Network,
    AlertOctagon,
    Activity,
    LineChart,
    Users2,
    SunMoon,
    Languages,
    Plus,
    RotateCw,
    LogOut,
    FileText,
    CornerDownLeft,
    Rocket,
    GitCompare,
    Undo2
  } from 'lucide-svelte';

  export let open = false;
  export let dlqCount = 0;

  let query = '';
  let highlight = 0;
  let inputEl: HTMLInputElement | null = null;
  let listEl: HTMLDivElement | null = null;

  // Search results
  let foundConnections: Connection[] = [];
  let foundPipelines: Pipeline[] = [];
  let foundDLQ: DLQEntry[] = [];
  let searching = false;
  let debounceTimer: ReturnType<typeof setTimeout> | undefined;

  type Item = {
    id: string;
    label: string;
    sublabel?: string;
    icon: typeof Search;
    section: string;
    action: () => void | Promise<void>;
    keywords?: string[];
  };

  $: navItems = [
    { id: 'nav.overview', label: t($locale, 'nav.overview'), icon: LayoutDashboard, action: () => goto('/') },
    { id: 'nav.topology', label: t($locale, 'nav.topology'), icon: Network, action: () => goto('/topology') },
    { id: 'nav.observability', label: t($locale, 'nav.observability'), icon: LineChart, action: () => goto('/observability') },
    { id: 'nav.metrics', label: t($locale, 'nav.metrics'), icon: Activity, action: () => goto('/metrics') },
    {
      id: 'nav.dlq',
      label: t($locale, 'nav.dlq'),
      sublabel: dlqCount > 0 ? `${dlqCount}` : undefined,
      icon: AlertOctagon,
      action: () => goto('/dlq')
    },
    { id: 'nav.connections', label: t($locale, 'nav.connections'), icon: Plug, action: () => goto('/connections') },
    { id: 'nav.pipelines', label: t($locale, 'nav.pipelines'), icon: Workflow, action: () => goto('/pipelines') },
    { id: 'nav.flow', label: t($locale, 'nav.flow'), icon: GitFork, action: () => goto('/flow') },
    { id: 'nav.tenants', label: t($locale, 'nav.tenants'), icon: Users2, action: () => goto('/tenants') }
  ] as Item[];

  $: actionItems = [
    {
      id: 'act.newConnection',
      label: t($locale, 'palette.cmd.newConnection'),
      icon: Plus,
      action: () => goto('/connections?new=1')
    },
    {
      id: 'act.newPipeline',
      label: t($locale, 'palette.cmd.newPipeline'),
      icon: Plus,
      action: () => goto('/pipelines?new=1')
    },
    {
      id: 'act.reload',
      label: t($locale, 'palette.cmd.reloadPipelines'),
      icon: RotateCw,
      action: async () => {
        await api.post('/v1/reload');
      }
    },
    {
      id: 'act.toggleTheme',
      label: t($locale, 'palette.cmd.toggleTheme'),
      icon: SunMoon,
      action: () => theme.toggle()
    },
    {
      id: 'act.toggleLocale',
      label: t($locale, 'palette.cmd.toggleLocale'),
      icon: Languages,
      action: () => locale.set($locale === 'en' ? 'ar' : 'en')
    },
    {
      id: 'act.openDocs',
      label: t($locale, 'palette.cmd.openDocs'),
      icon: FileText,
      action: () => window.open('/api/openapi.yaml', '_blank')
    },
    {
      id: 'act.logout',
      label: t($locale, 'palette.cmd.logout'),
      icon: LogOut,
      action: async () => {
        tenants.reset();
        await auth.logout();
        goto('/login');
      }
    }
  ] as Item[];

  // Studio entries are only meaningful while the user is on a Studio
  // route (`/pipelines/<id>/studio`). The palette pre-filters them
  // out everywhere else so they don't clutter the global launcher.
  // The Studio shell listens for the window-level events the palette
  // dispatches here ('studio:requestDeploy' / 'studio:openCompare') —
  // see Studio.svelte.onWindow* handlers for the Task-12 wiring.
  $: onStudioRoute = /^\/pipelines\/[^/]+\/studio(\/|$|\?)/.test($page.url.pathname + ($page.url.search || ''));
  $: studioDirty = $studioState?.dirtyCount > 0;

  $: studioItems = (
    [
      {
        id: 'act.studioDeploy',
        label: t($locale, 'palette.cmd.studioDeploy'),
        icon: Rocket,
        action: () => {
          // Studio.svelte's window listener opens the DeployDialog
          // against the latest revision (re-deploy of current) or
          // surfaces a toast when there's no revision history yet.
          // Wave 1 doesn't have a separate "save draft" path, so the
          // dirty branch falls through to the same dialog — Wave 2
          // will introduce explicit draft-vs-deploy separation.
          if (typeof window !== 'undefined') {
            window.dispatchEvent(new CustomEvent('studio:requestDeploy'));
          }
        }
      },
      {
        id: 'act.studioCompare',
        label: t($locale, 'palette.cmd.studioCompare'),
        icon: GitCompare,
        action: () => {
          // Studio.svelte expands the VersionRail (via its exposed
          // expandForCompare method) and stages the latest revision
          // for compare. The operator then taps Compare from the rail.
          if (typeof window !== 'undefined') {
            window.dispatchEvent(new CustomEvent('studio:openCompare'));
          }
        }
      },
      {
        id: 'act.studioDiscard',
        label: t($locale, 'palette.cmd.studioDiscard'),
        icon: Undo2,
        // Always show on studio routes; disabled when nothing to discard.
        action: () => {
          if (studioDirty) studio.resetDraft();
        }
      }
    ] as Item[]
  ).filter((it) => {
    if (!onStudioRoute) return false;
    if (it.id === 'act.studioDiscard') return studioDirty;
    return true;
  });

  $: filteredNav = filterItems(navItems, query);
  $: filteredActions = filterItems([...actionItems, ...studioItems], query);

  function filterItems(items: Item[], q: string): Item[] {
    const needle = q.trim().toLowerCase();
    if (!needle) return items;
    return items.filter((it) => {
      const hay = [it.label, it.sublabel ?? '', ...(it.keywords ?? [])]
        .join(' ')
        .toLowerCase();
      return hay.includes(needle);
    });
  }

  // ─── Resource search (debounced) ──────────────────────────────
  $: scheduleResourceSearch(query);
  function scheduleResourceSearch(q: string) {
    if (debounceTimer) clearTimeout(debounceTimer);
    if (!open || q.trim().length < 2) {
      foundConnections = [];
      foundPipelines = [];
      foundDLQ = [];
      return;
    }
    debounceTimer = setTimeout(() => void runResourceSearch(q.trim()), 200);
  }

  async function runResourceSearch(q: string) {
    searching = true;
    try {
      const [conns, pipes, dlq] = await Promise.all([
        api.get<Connection[]>('/v1/connections').catch(() => []),
        api.get<Pipeline[]>('/v1/pipelines').catch(() => []),
        api
          .get<{ items: DLQEntry[] }>(`/v1/dlq?error=${encodeURIComponent(q)}&per_page=5`)
          .catch(() => ({ items: [] }))
      ]);
      const needle = q.toLowerCase();
      foundConnections =
        (conns ?? [])
          .filter(
            (c) =>
              c.name.toLowerCase().includes(needle) ||
              (c.type || '').toLowerCase().includes(needle)
          )
          .slice(0, 5);
      foundPipelines =
        (pipes ?? []).filter((p) => p.name.toLowerCase().includes(needle)).slice(0, 5);
      foundDLQ = (dlq?.items ?? []).slice(0, 5);
    } catch {
      foundConnections = [];
      foundPipelines = [];
      foundDLQ = [];
    } finally {
      searching = false;
    }
  }

  $: resourceItems = (() => {
    const out: Item[] = [];
    for (const c of foundConnections) {
      out.push({
        id: `conn.${c.id}`,
        label: c.name,
        sublabel: `${c.type} connection`,
        icon: Plug,
        section: 'resources',
        action: () => goto(`/connections#${c.id}`)
      });
    }
    for (const p of foundPipelines) {
      out.push({
        id: `pipe.${p.id}`,
        label: p.name,
        sublabel: `pipeline · ${p.output_format}`,
        icon: Workflow,
        section: 'resources',
        // Task 14: /pipelines/{id} now redirects into the Studio anyway;
        // jump straight there so the palette doesn't trigger the
        // through-redirect (which would flicker an "loading" frame).
        action: () => goto(`/pipelines/${p.id}/studio`)
      });
    }
    for (const d of foundDLQ) {
      out.push({
        id: `dlq.${d.id}`,
        label: (d.error_reason || 'dead-letter').slice(0, 80),
        sublabel: `dlq · ${d.pipeline_id || 'unknown pipeline'}`,
        icon: AlertOctagon,
        section: 'resources',
        action: () => goto(`/dlq`)
      });
    }
    return out;
  })();

  // Build a flat ordered list for keyboard nav.
  type FlatGroup = { section: string; items: Item[] };
  $: flatGroups = (
    [
      { section: t($locale, 'palette.section.navigate'), items: filteredNav },
      { section: t($locale, 'palette.section.actions'), items: filteredActions },
      { section: t($locale, 'palette.section.recents'), items: resourceItems }
    ] as FlatGroup[]
  ).filter((g) => g.items.length > 0);

  $: flat = flatGroups.flatMap((g) => g.items);
  $: if (open && highlight >= flat.length) highlight = Math.max(0, flat.length - 1);

  async function activate() {
    if (!open) return;
    const item = flat[highlight];
    if (!item) return;
    await item.action();
    close();
  }

  function close() {
    open = false;
    query = '';
    highlight = 0;
    foundConnections = [];
    foundPipelines = [];
    foundDLQ = [];
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      highlight = Math.min(flat.length - 1, highlight + 1);
      ensureVisible();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      highlight = Math.max(0, highlight - 1);
      ensureVisible();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      void activate();
    } else if (e.key === 'Escape') {
      close();
    }
  }

  async function ensureVisible() {
    await tick();
    const el = listEl?.querySelector(`[data-idx='${highlight}']`) as HTMLElement | null;
    if (el) el.scrollIntoView({ block: 'nearest' });
  }

  // Focus the input whenever the palette opens.
  $: if (open) {
    queueMicrotask(() => inputEl?.focus());
  }
</script>

{#if open}
  <div
    class="overlay"
    role="presentation"
    on:click|self={close}
    on:keydown={onKey}
  >
    <div
      class="palette"
      role="dialog"
      aria-modal="true"
      aria-label={t($locale, 'palette.title')}
    >
      <div class="palette-input">
        <Search size={16} aria-hidden="true" />
        <input
          bind:this={inputEl}
          bind:value={query}
          on:keydown={onKey}
          placeholder={t($locale, 'palette.placeholder')}
          autocomplete="off"
          spellcheck="false"
          aria-label={t($locale, 'palette.placeholder')}
        />
        <span class="palette-input-kbd">ESC</span>
      </div>

      <div class="palette-list" bind:this={listEl}>
        {#if flat.length === 0}
          <p class="palette-empty">{t($locale, 'palette.empty')}</p>
        {:else}
          {#each flatGroups as group (group.section)}
            <p class="palette-section">{group.section}</p>
            {#each group.items as item, gi (item.id)}
              {@const idx = flat.indexOf(item)}
              <button
                type="button"
                class="palette-item"
                class:active={idx === highlight}
                data-idx={idx}
                on:mouseenter={() => (highlight = idx)}
                on:click={() => void activate()}
              >
                <span class="palette-item-icon">
                  <svelte:component this={item.icon} size={16} strokeWidth={1.75} />
                </span>
                <span class="palette-item-label">{item.label}</span>
                {#if item.sublabel}
                  <span class="palette-item-sub">{item.sublabel}</span>
                {/if}
                {#if idx === highlight}
                  <span class="palette-item-enter" aria-hidden="true">
                    <CornerDownLeft size={12} />
                  </span>
                {/if}
              </button>
            {/each}
          {/each}
        {/if}
      </div>

      <div class="palette-foot">
        <span><kbd>↑</kbd><kbd>↓</kbd> navigate</span>
        <span><kbd>↵</kbd> select</span>
        <span><kbd>esc</kbd> close</span>
        {#if searching}
          <span class="palette-foot-searching">searching…</span>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.45);
    backdrop-filter: blur(3px);
    z-index: 100;
    display: flex;
    align-items: flex-start;
    justify-content: center;
    padding-block-start: 10vh;
  }
  @media (prefers-reduced-motion: reduce) {
    .overlay {
      backdrop-filter: none;
    }
  }

  .palette {
    width: min(640px, 92vw);
    max-height: 70vh;
    background: var(--surface);
    border: 1px solid var(--border);
    /* §7 rule 10: containers are 16dp. The previous 14px was midway
       between interactive (12) and container (16) — pick a side. */
    border-radius: 16px;
    /* §5.14 elevation via the brand-token shadow; the previous custom
       multi-layer was darker than the spec and not theme-aware. */
    box-shadow: var(--dialog-shadow);
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .palette-input {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    padding: 0.875rem 1rem;
    border-bottom: 1px solid var(--border);
    color: var(--text-muted);
  }
  .palette-input input {
    flex: 1;
    border: 0;
    background: transparent;
    color: var(--text);
    font: inherit;
    font-size: 0.9375rem;
    outline: none;
  }
  .palette-input input::placeholder {
    color: var(--text-tertiary);
  }
  .palette-input-kbd {
    display: inline-flex;
    align-items: center;
    padding-inline: 0.375rem;
    height: 18px;
    border-radius: 4px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text-tertiary);
    font-size: 0.625rem;
    font-family: 'SFMono-Regular', Menlo, monospace;
  }

  .palette-list {
    flex: 1;
    overflow-y: auto;
    padding: 0.375rem 0.375rem 0.5rem;
  }
  .palette-section {
    padding: 0.5rem 0.75rem 0.25rem;
    font-size: 0.625rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-tertiary);
    font-weight: 700;
  }

  .palette-item {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    width: 100%;
    padding: 0.5rem 0.75rem;
    border-radius: 8px;
    border: 0;
    background: transparent;
    color: var(--text);
    font: inherit;
    font-size: 0.875rem;
    text-align: start;
    cursor: pointer;
    transition: background-color 100ms;
  }
  .palette-item:hover,
  .palette-item.active {
    background: var(--surface-2);
  }
  .palette-item-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    color: var(--text-muted);
  }
  .palette-item.active .palette-item-icon {
    color: var(--accent);
  }
  .palette-item-label {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .palette-item-sub {
    color: var(--text-muted);
    font-size: 0.75rem;
    font-family: 'SFMono-Regular', Menlo, monospace;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 14rem;
  }
  .palette-item-enter {
    display: inline-flex;
    align-items: center;
    color: var(--text-muted);
  }

  .palette-empty {
    padding: 1.25rem;
    text-align: center;
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .palette-foot {
    display: flex;
    align-items: center;
    gap: 0.875rem;
    padding: 0.5rem 1rem;
    border-top: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text-muted);
    font-size: 0.6875rem;
  }
  .palette-foot kbd {
    display: inline-flex;
    align-items: center;
    height: 14px;
    padding-inline: 4px;
    margin-inline: 1px;
    border-radius: 3px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--text-tertiary);
    font-size: 0.625rem;
    font-family: 'SFMono-Regular', Menlo, monospace;
  }
  .palette-foot-searching {
    margin-inline-start: auto;
    color: var(--text-tertiary);
  }
</style>
