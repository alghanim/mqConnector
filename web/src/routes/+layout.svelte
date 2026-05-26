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
  import { goto, afterNavigate } from '$app/navigation';
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
    Network,
    AlertOctagon,
    Activity,
    Users2,
    KeyRound as KeyIcon,
    Webhook as WebhookIcon,
    SlidersHorizontal,
    HelpCircle,
    LifeBuoy,
    Search,
    BellDot
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
        { href: '/topology', label: t($locale, 'nav.topology'), icon: Network },
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
    },
    {
      id: 'resources',
      label: t($locale, 'nav.section.resources'),
      items: [
        { href: '/help', label: t($locale, 'nav.help'), icon: LifeBuoy }
      ] as NavItem[]
    }
  ] satisfies Section[];

  function isActive(href: string, pathname: string): boolean {
    if (href === '/') return pathname === '/';
    return pathname === href || pathname.startsWith(href + '/');
  }

  // ─── Section dropdown state ─────────────────────────────────────
  //
  // The horizontal nav previously rendered every item as a top-level
  // pill — 12 of them after Wave 2 added /topology, which crowded the
  // bar and forced a scroll on smaller viewports. Collapse each
  // section (Operations / Configure / Admin / Resources) into a
  // single dropdown button; the items live in a popover below.
  //
  // A section button is highlighted when any of its items is the
  // active route — preserves the at-a-glance "where am I" cue.
  let openSection: string | null = null;
  function toggleSection(id: string) {
    openSection = openSection === id ? null : id;
  }
  function closeSection() {
    openSection = null;
  }
  // Close any open dropdown when the user navigates (route changed).
  // afterNavigate fires once per successful navigation — much safer
  // than a reactive on $page.url.pathname which can fire from
  // any store update (search params, hash, etc.).
  afterNavigate(() => closeSection());
  // Close on outside click / Escape.
  function onDocClick(e: MouseEvent) {
    if (!openSection) return;
    const target = e.target as HTMLElement | null;
    if (target && target.closest('[data-nav-section]')) return;
    openSection = null;
  }
  function onDocKey(e: KeyboardEvent) {
    if (e.key === 'Escape' && openSection) openSection = null;
  }
  onMount(() => {
    document.addEventListener('click', onDocClick);
    document.addEventListener('keydown', onDocKey);
    return () => {
      document.removeEventListener('click', onDocClick);
      document.removeEventListener('keydown', onDocKey);
    };
  });

  function sectionActive(section: Section, pathname: string): boolean {
    return section.items.some((it) => isActive(it.href, pathname));
  }
  function sectionBadge(section: Section): number {
    return section.items.reduce((sum, it) => sum + (it.badge ?? 0), 0);
  }
</script>

{#if showChrome}
  <!--
    Skip-to-main-content link. Visually hidden but keyboard-focusable
    via Tab from the top of the page. Required for WCAG 2.1 SC 2.4.1
    (Bypass Blocks) — screen-reader and keyboard users skip past the
    sidebar without tabbing through every nav item.
  -->
  <a href="#main-content" class="skip-link">{t($locale, 'a11y.skipToMain')}</a>
  <div class="shell">
    <!-- ─── Top nav (was a left sidebar; flipped horizontal so the main
         content area gets the full viewport width) ─────────────────── -->
    <header class="topnav">
      <div class="brand-strip" aria-hidden="true"></div>
      <div class="topnav-row">
        <a class="brand" href="/" aria-label="mqConnector">
          <span class="brand-mark"><Logo size={24} /></span>
          <span class="brand-name">mq<span class="brand-name-accent">Connector</span></span>
        </a>

        <nav class="nav-row" aria-label="primary">
          {#each navSections as section (section.id)}
            {@const active = sectionActive(section, $page.url.pathname)}
            {@const badge = sectionBadge(section)}
            {@const isOpen = openSection === section.id}
            <div class="nav-section" data-nav-section={section.id}>
              <button
                type="button"
                class="nav-section-btn"
                class:active
                class:open={isOpen}
                aria-haspopup="menu"
                aria-expanded={isOpen}
                aria-current={active ? 'page' : undefined}
                title={section.label}
                on:click|stopPropagation={() => toggleSection(section.id)}
              >
                <span class="nav-label">{section.label}</span>
                {#if badge > 0}
                  <span class="nav-badge">{badge > 99 ? '99+' : badge}</span>
                {/if}
                <span class="nav-chevron" aria-hidden="true">
                  <svg viewBox="0 0 10 6" width="10" height="6" fill="none">
                    <path d="M1 1l4 4 4-4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
                  </svg>
                </span>
              </button>

              {#if isOpen}
                <div class="nav-menu" role="menu" aria-label={section.label}>
                  {#each section.items as item (item.href)}
                    {@const itemActive = isActive(item.href, $page.url.pathname)}
                    <a
                      href={item.href}
                      class="nav-menu-item"
                      class:active={itemActive}
                      role="menuitem"
                      aria-current={itemActive ? 'page' : undefined}
                    >
                      <span class="nav-menu-icon" aria-hidden="true">
                        <svelte:component this={item.icon} size={15} strokeWidth={1.75} />
                      </span>
                      <span class="nav-menu-label">{item.label}</span>
                      {#if item.badge && item.badge > 0}
                        <span class="nav-badge nav-badge-inline">{item.badge > 99 ? '99+' : item.badge}</span>
                      {/if}
                    </a>
                  {/each}
                </div>
              {/if}
            </div>
          {/each}
        </nav>

        <div class="topnav-end">
          <button
            type="button"
            class="search-trigger"
            on:click={() => (paletteOpen = true)}
            aria-label={t($locale, 'shell.searchHint')}
          >
            <Search size={14} aria-hidden="true" />
            <span class="search-trigger-kbd">⌘K</span>
          </button>
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
      </div>
      <div class="topnav-crumbs">
        <Breadcrumbs />
      </div>
    </header>

    <main id="main-content" class="page" tabindex="-1">
      <slot />
    </main>
  </div>

  <CommandPalette bind:open={paletteOpen} {dlqCount} />
  <KeyboardShortcuts open={shortcutsOpen} on:close={() => (shortcutsOpen = false)} />
  <Toaster />
{:else}
  <slot />
  <Toaster />
{/if}

<style>
  /* ─── Skip link (WCAG 2.1 SC 2.4.1) ─────────────────────────────── */
  /* Visually hidden until focused. Tab from top-of-page reveals it
     above everything else so keyboard users can bypass the sidebar. */
  .skip-link {
    position: absolute;
    inset-inline-start: 8px;
    top: 8px;
    z-index: 100;
    padding: 8px 12px;
    background: var(--accent);
    color: var(--accent-contrast);
    text-decoration: none;
    font-weight: 600;
    border-radius: 12px;
    transform: translateY(-150%);
    transition: transform 0.15s ease;
  }
  .skip-link:focus,
  .skip-link:focus-visible {
    transform: translateY(0);
    outline: 2px solid var(--text);
    outline-offset: 2px;
  }

  /* ─── Shell grid ───────────────────────────────────────────────── */
  .shell {
    min-height: 100vh;
    display: grid;
    grid-template-rows: auto 1fr;
    background: var(--bg);
    color: var(--text);
    inline-size: 100%;
    max-inline-size: 100vw;
    overflow-x: hidden;
  }
  .topnav {
    position: sticky;
    top: 0;
    z-index: 30;
    background: var(--surface);
    border-block-end: 1px solid var(--border);
    inline-size: 100%;
    min-inline-size: 0;
    overflow: visible;
    /* No overflow clip here — the section dropdowns hang below the
       row and need to escape. Horizontal clipping is handled at
       .shell + .nav-row instead. Isolation ensures the topnav's
       sticky z-index doesn't trap children's higher z-indexes. */
    isolation: isolate;
  }
  .brand-strip {
    height: 3px;
    background: var(--brand-gradient);
  }
  .topnav-row {
    position: relative;
    z-index: 2;
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding-inline: 1rem;
    padding-block: 0.5rem;
    min-block-size: 56px;
    inline-size: 100%;
    min-inline-size: 0;
    box-sizing: border-box;
  }
  .topnav-crumbs {
    position: relative;
    z-index: 1;
    padding-inline: 1rem;
    padding-block: 0.5rem 0.625rem;
    border-block-start: 1px solid var(--border);
  }

  /* ─── Brand block (compact horizontal form) ────────────────────── */
  .brand {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    color: var(--text);
    text-decoration: none;
    flex-shrink: 0;
  }
  .brand-mark {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: 10px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text);
  }
  .brand-name {
    font-size: 0.9375rem;
    font-weight: 700;
    letter-spacing: -0.01em;
  }
  .brand-name-accent {
    color: var(--text);
  }

  /* ─── Horizontal nav (top bar) ─────────────────────────────────── */
  .nav-row {
    flex: 1 1 0;
    display: flex;
    align-items: center;
    gap: 0.25rem;
    overflow-x: auto;
    overflow-y: hidden;
    min-inline-size: 0;
    inline-size: 0;
    scrollbar-width: thin;
  }
  .nav-divider {
    inline-size: 1px;
    block-size: 20px;
    background: var(--border);
    margin-inline: 0.375rem;
    flex-shrink: 0;
  }

  /* ─── Section dropdown buttons ─────────────────────────────────── */
  .nav-section {
    position: relative;
    display: inline-flex;
    flex-shrink: 0;
    /* Always above any sibling chrome (breadcrumbs row, page header) so
       the popover never gets occluded. Don't rely on :has() for this —
       it works in modern browsers but is too easy to lose to a
       stacking-context bug. A constant high z-index is bulletproof. */
    z-index: 60;
  }
  .nav-section-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    padding-inline: 0.75rem;
    padding-block: 0.4rem;
    border-radius: 0.625rem;
    border: 1px solid transparent;
    background: transparent;
    color: var(--text-muted);
    font-size: 0.8125rem;
    font-weight: 500;
    font-family: inherit;
    cursor: pointer;
    white-space: nowrap;
    transition: background-color 150ms, color 150ms, border-color 150ms;
  }
  .nav-section-btn:hover {
    background: var(--surface-2);
    color: var(--text);
  }
  .nav-section-btn.active {
    background: var(--surface-2);
    color: var(--text);
    font-weight: 600;
    box-shadow: inset 0 -2px 0 var(--primary);
  }
  .nav-section-btn.open {
    background: var(--surface-2);
    color: var(--text);
    border-color: var(--border);
  }
  .nav-chevron {
    display: inline-flex;
    color: var(--text-tertiary);
    transition: transform 150ms;
  }
  .nav-section-btn.open .nav-chevron {
    transform: rotate(180deg);
  }
  .nav-section-btn:focus-visible {
    outline: 2px solid var(--primary);
    outline-offset: 2px;
  }

  .nav-menu {
    position: absolute;
    top: calc(100% + 6px);
    inset-inline-start: 0;
    z-index: 70;
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-inline-size: 13rem;
    padding: 0.5rem;
    background: var(--surface-high);
    border: 1px solid var(--border-strong);
    border-radius: 0.75rem;
    box-shadow:
      0 1px 2px rgba(0, 0, 0, 0.18),
      0 12px 32px -10px rgba(0, 0, 0, 0.42);
  }
  :global([data-theme='light']) .nav-menu {
    background: var(--surface);
    box-shadow:
      0 1px 2px rgba(51, 63, 72, 0.10),
      0 12px 32px -10px rgba(51, 63, 72, 0.22);
  }
  .nav-menu-item {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding-inline: 0.625rem;
    padding-block: 0.45rem;
    border-radius: 0.5rem;
    color: var(--text-muted);
    text-decoration: none;
    font-size: 0.8125rem;
    font-weight: 500;
    white-space: nowrap;
    transition: background-color 120ms, color 120ms;
  }
  .nav-menu-item:hover,
  .nav-menu-item:focus-visible {
    background: var(--surface-2);
    color: var(--text);
    outline: none;
  }
  .nav-menu-item.active {
    background: var(--surface-2);
    color: var(--text);
    font-weight: 600;
    box-shadow: inset 2px 0 0 var(--primary);
  }
  :global([dir='rtl']) .nav-menu-item.active {
    box-shadow: inset -2px 0 0 var(--primary);
  }
  .nav-menu-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 16px;
    color: inherit;
    flex-shrink: 0;
  }
  .nav-menu-item.active .nav-menu-icon {
    color: var(--primary);
  }
  .nav-menu-label {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .nav-badge-inline {
    margin-inline-start: auto;
  }

  .nav-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /*
   * Nav badge = count badge (DLQ count). §5.5 allows pill + maroon here:
   * count badges are the one exception where pill radius AND --accent
   * background are both correct.
   */
  .nav-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    height: 18px;
    min-width: 22px;
    padding-inline: 6px;
    border-radius: 999px;
    background: var(--accent);
    color: var(--accent-on);
    font-size: 0.6875rem;
    font-weight: 600;
    letter-spacing: 0.02em;
  }

  /* ─── Topbar right cluster ─────────────────────────────────────── */
  .topnav-end {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    flex-shrink: 0;
  }
  .search-trigger {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding-block: 0.3125rem;
    padding-inline: 0.5rem;
    border-radius: 0.625rem;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text-muted);
    font-size: 0.75rem;
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

  /*
   * 32×32 icon button — 12dp radius per brand-guide Rule 9 (pill is
   * reserved for count badges). The maroon dot indicator (-dot) DOES
   * stay pill: it's a count-indicator, the one allowed pill use.
   */
  .icon-btn {
    position: relative;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: 12px;
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
    padding: 1.125rem 1.5rem 2rem;
    overflow-y: auto;
    inline-size: 100%;
    max-inline-size: 1680px;
    margin-inline: auto;
    min-block-size: 0;
  }

  @media (max-width: 900px) {
    .nav-section-btn {
      padding-inline: 0.5rem;
    }
  }
</style>
