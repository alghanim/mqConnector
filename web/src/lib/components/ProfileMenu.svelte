<!--
  Profile dropdown anchored to the user avatar in the top-right of the
  app shell. Closes on outside click or Escape. Provides:
    - User identity (name, sub)
    - Active tenant (with link to tenants page)
    - Logout
  Keyboard:
    - Tab cycles within the menu
    - Escape closes
-->
<script lang="ts">
  import { onMount, createEventDispatcher } from 'svelte';
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth';
  import { tenants } from '$lib/stores/tenants';
  import { locale, t } from '$lib/stores/locale';
  import Avatar from '$lib/components/Avatar.svelte';
  import { LogOut, User, Building2, ChevronDown } from 'lucide-svelte';

  const dispatch = createEventDispatcher();
  let open = false;
  let rootEl: HTMLDivElement | null = null;
  let firstItem: HTMLAnchorElement | null = null;

  onMount(() => {
    const onDocClick = (e: MouseEvent) => {
      if (rootEl && !rootEl.contains(e.target as Node)) open = false;
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') open = false;
    };
    document.addEventListener('click', onDocClick);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('click', onDocClick);
      document.removeEventListener('keydown', onKey);
    };
  });

  $: name = $auth.user?.preferred_username || $auth.user?.name || $auth.user?.sub || '';
  $: sub = $auth.user?.sub ?? '';
  $: activeName = $tenants.active?.tenant.name ?? '';
  $: activeRole = $tenants.active?.role ?? '';

  async function logout() {
    open = false;
    tenants.reset();
    await auth.logout();
    goto('/login');
  }

  function go(href: string) {
    open = false;
    goto(href);
  }
</script>

<div class="profile-menu" bind:this={rootEl}>
  <button
    class="profile-trigger"
    aria-haspopup="menu"
    aria-expanded={open}
    on:click={() => (open = !open)}
  >
    <Avatar {name} {sub} size="sm" />
    <span class="profile-name">{name}</span>
    <ChevronDown size={14} aria-hidden="true" class="chev" />
  </button>

  {#if open}
    <div role="menu" class="profile-popover">
      <div class="profile-head">
        <Avatar {name} {sub} size="lg" />
        <div class="min-w-0">
          <p class="profile-head-name">{name}</p>
          {#if sub}
            <p class="profile-head-sub" title={sub}>{sub}</p>
          {/if}
        </div>
      </div>

      {#if activeName}
        <div class="profile-tenant">
          <Building2 size={14} aria-hidden="true" />
          <div class="min-w-0">
            <p class="profile-tenant-label">{t($locale, 'tenants.title')}</p>
            <p class="profile-tenant-name">{activeName} <span class="opacity-70">· {activeRole}</span></p>
          </div>
        </div>
      {/if}

      <div class="profile-sep"></div>

      <a
        href="/tenants"
        bind:this={firstItem}
        class="profile-item"
        role="menuitem"
        on:click|preventDefault={() => go('/tenants')}
      >
        <Building2 size={16} aria-hidden="true" />
        <span>{t($locale, 'nav.tenants')}</span>
      </a>
      <a
        href="/account"
        class="profile-item"
        role="menuitem"
        on:click|preventDefault={() => go('/account')}
      >
        <User size={16} aria-hidden="true" />
        <span>{t($locale, 'profile.account')}</span>
      </a>

      <div class="profile-sep"></div>

      <button class="profile-item danger" role="menuitem" on:click={logout}>
        <LogOut size={16} aria-hidden="true" />
        <span>{t($locale, 'nav.logout')}</span>
      </button>
    </div>
  {/if}
</div>

<style>
  .profile-menu {
    position: relative;
    display: inline-flex;
    align-items: center;
  }
  .profile-trigger {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.25rem 0.5rem 0.25rem 0.25rem;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text);
    cursor: pointer;
    transition: background-color 200ms, border-color 200ms;
  }
  .profile-trigger:hover {
    background: var(--surface);
    border-color: var(--border-strong);
  }
  .profile-name {
    font-size: 0.8125rem;
    font-weight: 500;
    max-width: 9rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  :global(.chev) {
    color: var(--text-muted);
  }

  .profile-popover {
    position: absolute;
    top: calc(100% + 8px);
    inset-inline-end: 0;
    min-width: 18rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 0.875rem;
    box-shadow: 0 16px 40px rgba(0, 0, 0, 0.25), 0 2px 6px rgba(0, 0, 0, 0.12);
    padding: 0.625rem;
    z-index: 50;
  }
  .profile-head {
    display: flex;
    gap: 0.75rem;
    align-items: center;
    padding: 0.5rem 0.5rem 0.75rem;
  }
  .profile-head-name {
    font-size: 0.9375rem;
    font-weight: 600;
    color: var(--text);
    line-height: 1.2;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .profile-head-sub {
    font-size: 0.6875rem;
    color: var(--text-muted);
    font-family: 'SFMono-Regular', Menlo, monospace;
    margin-top: 2px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 13rem;
  }

  .profile-tenant {
    display: flex;
    gap: 0.625rem;
    align-items: center;
    padding: 0.5rem 0.625rem;
    background: var(--surface-2);
    border-radius: 0.625rem;
    color: var(--text);
  }
  .profile-tenant-label {
    font-size: 0.625rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }
  .profile-tenant-name {
    font-size: 0.8125rem;
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .profile-sep {
    height: 1px;
    background: var(--border);
    margin: 0.5rem 0;
  }

  .profile-item {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    width: 100%;
    padding: 0.5rem 0.625rem;
    border: 0;
    background: transparent;
    color: var(--text);
    border-radius: 0.5rem;
    font: inherit;
    font-size: 0.875rem;
    cursor: pointer;
    text-align: start;
    text-decoration: none;
  }
  .profile-item:hover {
    background: var(--surface-2);
  }
  .profile-item.danger {
    color: var(--danger);
  }
  .profile-item.danger:hover {
    background: color-mix(in srgb, var(--danger) 12%, transparent);
  }
</style>
