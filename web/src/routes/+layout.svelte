<!--
  App shell. The chrome is the most-seen surface — every page inherits
  its cues, so it has to read as enterprise on first glance:

    Sidebar
      • Logo with the brand mark + product name + version chip
      • SECTION HEADERS in small caps (OPERATIONS / CONFIGURATION /
        ADMINISTRATION) — instantly hierarchies the IA
      • Icon + label on every nav row; a count badge on /dlq when
        there are dead-letter entries
      • Bottom row: tenant context line (the active tenant + role)

    Header
      • Breadcrumbs on the inline-start side — gives every page free
        context, no hand-coding required
      • Global search trigger ("Press / or ⌘K") right of breadcrumbs
      • SystemHealthPill — one-glance status (Operational / Degraded /
        Outage) — pulses on degraded/outage
      • TenantSwitcher (multi-tenant only)
      • LocaleToggle, ThemeToggle
      • ProfileMenu with avatar, name, sub, active tenant, logout

  Keyboard:
    • / or Ctrl/⌘+K  → command palette
    • ?              → keyboard shortcut sheet (later pass)
    • Escape         → close any open overlay
-->
<script lang="ts">
  import '../app.css';
  import { onMount, onDestroy } from 'svelte';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth';
  import { tenants } from '$lib/stores/tenants';
  import { locale, t } from '$lib/stores/locale';
  import { api, type DLQEntry } from '$lib/api';
  import {
    connect as liveConnect,
    disconnect as liveDisconnect,
    dlqTotal as liveDlqTotal,
    liveMode as liveModeStore
  } from '$lib/stores/live';
  import Logo from '$lib/components/Logo.svelte';
  import ThemeToggle from '$lib/components/ThemeToggle.svelte';
  import LocaleToggle from '$lib/components/LocaleToggle.svelte';
  import TenantSwitcher from '$lib/components/TenantSwitcher.svelte';
  import ProfileMenu from '$lib/components/ProfileMenu.svelte';
  import SystemHealthPill from '$lib/components/SystemHealthPill.svelte';
  import Breadcrumbs from '$lib/components/Breadcrumbs.svelte';
  import CommandPalette from '$lib/components/CommandPalette.svelte';
  import KeyboardShortcuts from '$lib/components/KeyboardShortcuts.svelte';
  import Toaster from '$lib/components/Toaster.svelte';
  import {
    LayoutDashboard,
    Plug,
    Workflow,
    GitFork,
    AlertOctagon,
    Activity,
    Users2,
    KeyRound as KeyIcon,
    Webhook as WebhookIcon,
    SlidersHorizontal,
    Search,
    BellDot,
    KeyRound
  } from 'lucide-svelte';

  let dlqCount = 0;
  let dlqTimer: ReturnType<typeof setInterval> | undefined;
  let paletteOpen = false;
  let shortcutsOpen = false;

  // Bind dlqCount to the shared live store. The badge follows whatever
  // the SSE stream pushed; the polling fallback below keeps it accurate
  // when SSE drops.
  $: dlqCount = $liveDlqTotal;

  onMount(async () => {
    await auth.refresh();
    if (!$auth.user && $page.url.pathname !== '/login') {
      goto('/login');
      return;
    }
    if ($auth.user) {
      await tenants.refresh();
      // Paint the badge from a one-shot fetch while the SSE stream
      // comes up. The shared live store takes over once the first
      // frame arrives.
      void refreshDlqBadge();
      liveConnect(2000);
    }

    document.addEventListener('keydown', onKey);
  });

  onDestroy(() => {
    if (dlqTimer) clearInterval(dlqTimer);
    liveDisconnect();
    document.removeEventListener('keydown', onKey);
  });

  // Fallback polling — engaged whenever the live stream is down. Watches
  // the shared liveMode store and toggles a 30 s interval to keep the
  // badge fresh.
  $: if ($auth.user && !$liveModeStore) {
    if (!dlqTimer) {
      dlqTimer = setInterval(refreshDlqBadge, 30_000);
    }
  } else if (dlqTimer) {
    clearInterval(dlqTimer);
    dlqTimer = undefined;
  }

  function onKey(e: KeyboardEvent) {
    if (!$auth.user) return;
    const target = e.target as HTMLElement | null;
    const inEditable =
      target &&
      (target.tagName === 'INPUT' ||
        target.tagName === 'TEXTAREA' ||
        (target as HTMLElement).isContentEditable);

    // Ctrl/⌘+K from anywhere; "/" only outside text fields.
    const isCmdK = (e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k';
    const isSlash = e.key === '/' && !inEditable;
    if (isCmdK || isSlash) {
      e.preventDefault();
      paletteOpen = true;
      shortcutsOpen = false;
    }
    // "?" → shortcut sheet (Shift+/ on US layouts). Outside text fields only.
    if (e.key === '?' && !inEditable) {
      e.preventDefault();
      shortcutsOpen = !shortcutsOpen;
      if (shortcutsOpen) paletteOpen = false;
    }
    if (e.key === 'Escape') {
      paletteOpen = false;
      shortcutsOpen = false;
    }
  }

  async function refreshDlqBadge() {
    try {
      const res = await api.get<{ total: number; items: DLQEntry[] }>(
        '/v1/dlq?page=1&per_page=1'
      );
      liveDlqTotal.set(res.total ?? 0);
    } catch {
      // Silent — the badge is best-effort.
    }
  }

  $: showChrome = $page.url.pathname !== '/login' && !!$auth.user;

  type NavItem = {
    href: string;
    label: string;
    icon: typeof LayoutDashboard;
    badge?: number;
  };
  type Section = { id: string; label: string; items: NavItem[] };

  $: navSections = [
    {
      id: 'ops',
      label: t($locale, 'nav.section.operations'),
      items: [
        { href: '/', label: t($locale, 'nav.overview'), icon: LayoutDashboard },
        { href: '/metrics', label: t($locale, 'nav.metrics'), icon: Activity },
        { href: '/dlq', label: t($locale, 'nav.dlq'), icon: AlertOctagon, badge: dlqCount }
      ] as NavItem[]
    },
    {
      id: 'cfg',
      label: t($locale, 'nav.section.configuration'),
      items: [
        { href: '/connections', label: t($locale, 'nav.connections'), icon: Plug },
        { href: '/pipelines', label: t($locale, 'nav.pipelines'), icon: Workflow },
        { href: '/flow', label: t($locale, 'nav.flow'), icon: GitFork }
      ] as NavItem[]
    },
    {
      id: 'admin',
      label: t($locale, 'nav.section.administration'),
      items: [
        { href: '/tenants', label: t($locale, 'nav.tenants'), icon: Users2 },
        { href: '/tokens', label: t($locale, 'nav.tokens'), icon: KeyIcon },
        { href: '/webhooks', label: t($locale, 'nav.webhooks'), icon: WebhookIcon },
        { href: '/settings', label: t($locale, 'nav.settings'), icon: SlidersHorizontal }
      ] as NavItem[]
    }
  ] satisfies Section[];

  function isActive(href: string, pathname: string): boolean {
    if (href === '/') return pathname === '/';
    return pathname === href || pathname.startsWith(href + '/');
  }
</script>

{#if showChrome}
  <div class="shell">
    <!-- ─── Sidebar ─────────────────────────────────────────────── -->
    <aside class="sidebar">
      <!-- Brand strip — single decorative line of the gold gradient -->
      <div class="brand-strip" aria-hidden="true"></div>

      <a class="brand" href="/" aria-label="mqConnector">
        <span class="brand-mark"><Logo size={28} /></span>
        <span class="brand-words">
          <span class="brand-name">mq<span class="brand-name-accent">Connector</span></span>
          <span class="brand-tagline">{t($locale, 'app.subtitle')}</span>
        </span>
      </a>

      <nav class="nav" aria-label="primary">
        {#each navSections as section (section.id)}
          <p class="nav-section">{section.label}</p>
          <ul class="nav-list">
            {#each section.items as item (item.href)}
              {@const active = isActive(item.href, $page.url.pathname)}
              <li>
                <a
                  href={item.href}
                  class="nav-item"
                  class:active
                  aria-current={active ? 'page' : undefined}
                >
                  <span class="nav-icon" aria-hidden="true">
                    <svelte:component this={item.icon} size={16} strokeWidth={1.75} />
                  </span>
                  <span class="nav-label">{item.label}</span>
                  {#if item.badge && item.badge > 0}
                    <span class="nav-badge">{item.badge > 99 ? '99+' : item.badge}</span>
                  {/if}
                </a>
              </li>
            {/each}
          </ul>
        {/each}
      </nav>

      <!-- Bottom: keyboard hints -->
      <div class="sidebar-foot">
        <button
          type="button"
          class="cmdk-hint"
          on:click={() => (paletteOpen = true)}
          aria-label={t($locale, 'palette.title')}
        >
          <KeyRound size={14} aria-hidden="true" />
          <span>{t($locale, 'shell.cmdKHint')}</span>
        </button>
        <button
          type="button"
          class="kbd-hint-btn"
          on:click={() => (shortcutsOpen = true)}
          aria-label={t($locale, 'shortcuts.title')}
          title={t($locale, 'shortcuts.title')}
        >
          ?
        </button>
      </div>
    </aside>

    <!-- ─── Main column ─────────────────────────────────────────── -->
    <div class="main">
      <header class="topbar">
        <div class="topbar-start">
          <Breadcrumbs />
        </div>

        <button
          type="button"
          class="search-trigger"
          on:click={() => (paletteOpen = true)}
          aria-label={t($locale, 'shell.searchHint')}
        >
          <Search size={14} aria-hidden="true" />
          <span class="search-trigger-label">{t($locale, 'shell.search')}</span>
          <span class="search-trigger-kbd">⌘K</span>
        </button>

        <div class="topbar-end">
          <SystemHealthPill />
          <button
            type="button"
            class="icon-btn"
            aria-label={t($locale, 'shell.notifications')}
            title={dlqCount > 0
              ? `${dlqCount} ${t($locale, 'nav.dlq')}`
              : t($locale, 'shell.noNotifications')}
            on:click={() => goto('/dlq')}
          >
            <BellDot size={16} aria-hidden="true" />
            {#if dlqCount > 0}
              <span class="icon-btn-dot" aria-hidden="true"></span>
            {/if}
          </button>
          <TenantSwitcher />
          <LocaleToggle />
          <ThemeToggle />
          <ProfileMenu />
        </div>
      </header>

      <main class="page">
        <slot />
      </main>
    </div>
  </div>

  <CommandPalette bind:open={paletteOpen} {dlqCount} />
  <KeyboardShortcuts open={shortcutsOpen} on:close={() => (shortcutsOpen = false)} />
  <Toaster />
{:else}
  <slot />
  <Toaster />
{/if}

<style>
  /* ─── Shell grid ───────────────────────────────────────────────── */
  .shell {
    min-height: 100vh;
    display: grid;
    grid-template-columns: 260px 1fr;
    grid-template-rows: 100vh;
    background: var(--bg);
    color: var(--text);
  }
  .sidebar {
    position: relative;
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border-inline-end: 1px solid var(--border);
    overflow-y: auto;
  }
  .brand-strip {
    height: 3px;
    background: var(--brand-gradient);
  }
  .main {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  /* ─── Brand block ──────────────────────────────────────────────── */
  .brand {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 1.125rem 1.125rem 1rem;
    color: var(--text);
    text-decoration: none;
  }
  .brand-mark {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 36px;
    height: 36px;
    border-radius: 10px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text);
  }
  .brand-words {
    display: flex;
    flex-direction: column;
    line-height: 1.1;
  }
  .brand-name {
    font-size: 1rem;
    font-weight: 700;
    letter-spacing: -0.01em;
  }
  .brand-name-accent {
    color: var(--accent);
  }
  .brand-tagline {
    margin-top: 2px;
    font-size: 0.6875rem;
    color: var(--text-muted);
    letter-spacing: 0.01em;
  }

  /* ─── Nav ──────────────────────────────────────────────────────── */
  .nav {
    flex: 1;
    padding: 0.25rem 0.625rem 0.5rem;
  }
  .nav-section {
    margin: 0.875rem 0.625rem 0.375rem;
    font-size: 0.625rem;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-tertiary);
  }
  .nav-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .nav-item {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    padding: 0.5rem 0.625rem;
    border-radius: 0.5rem;
    color: var(--text-secondary, var(--text));
    text-decoration: none;
    font-size: 0.8125rem;
    font-weight: 500;
    transition:
      background-color 150ms,
      color 150ms;
    min-height: 36px;
  }
  .nav-item:hover {
    background: var(--surface-2);
    color: var(--text);
  }
  .nav-item.active {
    background: var(--surface-2);
    color: var(--text);
    font-weight: 600;
    box-shadow: inset 3px 0 0 var(--accent);
  }
  :global([dir='rtl']) .nav-item.active {
    box-shadow: inset -3px 0 0 var(--accent);
  }
  .nav-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    color: inherit;
    flex-shrink: 0;
  }
  .nav-item.active .nav-icon {
    color: var(--accent);
  }
  .nav-label {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .nav-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    height: 18px;
    min-width: 22px;
    padding-inline: 6px;
    border-radius: 999px;
    background: var(--accent);
    color: #fff;
    font-size: 0.6875rem;
    font-weight: 600;
    letter-spacing: 0.02em;
  }

  /* ─── Sidebar foot ─────────────────────────────────────────────── */
  .sidebar-foot {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.625rem;
    border-top: 1px solid var(--border);
  }
  .cmdk-hint {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex: 1;
    padding: 0.5rem 0.625rem;
    border-radius: 0.5rem;
    border: 1px dashed var(--border-strong);
    background: transparent;
    color: var(--text-muted);
    font-size: 0.75rem;
    cursor: pointer;
    transition:
      color 150ms,
      border-color 150ms,
      background-color 150ms;
  }
  .cmdk-hint:hover {
    color: var(--text);
    border-color: var(--text-tertiary);
    background: var(--surface-2);
  }
  .kbd-hint-btn {
    flex: 0 0 auto;
    inline-size: 30px;
    block-size: 30px;
    border-radius: 6px;
    border: 1px dashed var(--border-strong);
    background: transparent;
    color: var(--text-muted);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.875rem;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    transition:
      color 150ms,
      border-color 150ms,
      background-color 150ms;
  }
  .kbd-hint-btn:hover {
    color: var(--text);
    border-color: var(--text-tertiary);
    background: var(--surface-2);
  }

  /* ─── Topbar ───────────────────────────────────────────────────── */
  .topbar {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.625rem 1.25rem;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
    min-height: 56px;
    position: sticky;
    top: 0;
    z-index: 30;
    backdrop-filter: saturate(140%) blur(6px);
  }
  .topbar-start {
    flex: 1;
    min-width: 0;
    overflow: hidden;
  }
  .topbar-end {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .search-trigger {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding-block: 0.3125rem;
    padding-inline: 0.625rem 0.5rem;
    border-radius: 0.5rem;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text-muted);
    font-size: 0.8125rem;
    min-width: 14rem;
    cursor: pointer;
    transition:
      background-color 150ms,
      border-color 150ms,
      color 150ms;
  }
  .search-trigger:hover {
    color: var(--text);
    background: var(--surface);
    border-color: var(--border-strong);
  }
  .search-trigger-label {
    flex: 1;
    text-align: start;
  }
  .search-trigger-kbd {
    display: inline-flex;
    align-items: center;
    padding-inline: 0.375rem;
    height: 18px;
    border-radius: 4px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--text-tertiary);
    font-size: 0.6875rem;
    font-family: 'SFMono-Regular', Menlo, monospace;
  }

  .icon-btn {
    position: relative;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text);
    cursor: pointer;
    transition:
      background-color 200ms,
      border-color 200ms;
  }
  .icon-btn:hover {
    background: var(--surface);
    border-color: var(--border-strong);
  }
  .icon-btn-dot {
    position: absolute;
    top: 6px;
    inset-inline-end: 7px;
    width: 7px;
    height: 7px;
    border-radius: 999px;
    background: var(--accent);
    box-shadow: 0 0 0 2px var(--surface);
  }

  /* ─── Page area ────────────────────────────────────────────────── */
  .page {
    flex: 1;
    padding: 1.5rem;
    overflow-y: auto;
    max-width: 1400px;
    width: 100%;
  }

  @media (max-width: 900px) {
    .shell {
      grid-template-columns: 1fr;
      grid-template-rows: auto 1fr;
    }
    .sidebar {
      display: none; /* mobile shell is a follow-up — keep the desktop bar tidy for now */
    }
  }
</style>
