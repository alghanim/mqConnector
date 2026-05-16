<!--
  TenantSwitcher — a compact dropdown in the top nav.

  Behaviour:
    - Shows the active tenant's name + role.
    - Click opens a list of every tenant the user belongs to.
    - Selecting a different tenant hits POST /api/v1/tenants/{id}/switch,
      refreshes the store, then forces a page reload so every
      tenant-scoped list (connections, pipelines, etc.) re-fetches
      cleanly. Reload is cheap and avoids hand-wiring invalidation
      across half a dozen unrelated stores.

  Single-tenant deployments (one membership) render as a static badge
  instead of a clickable dropdown — keeps the chrome quiet.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { tenants } from '$lib/stores/tenants';
  import { locale, t } from '$lib/stores/locale';
  import Badge from '$lib/components/Badge.svelte';

  let open = false;
  let rootEl: HTMLDivElement | null = null;

  onMount(() => {
    if (!$tenants.initialised) {
      void tenants.refresh();
    }
    const onClick = (e: MouseEvent) => {
      if (rootEl && !rootEl.contains(e.target as Node)) open = false;
    };
    document.addEventListener('click', onClick);
    return () => document.removeEventListener('click', onClick);
  });

  async function pick(tenantId: string) {
    if ($tenants.active?.tenant.id === tenantId) {
      open = false;
      return;
    }
    const ok = await tenants.switchTo(tenantId);
    if (ok) {
      // Hard reload — every tenant-scoped list invalidates at once
      // and the URL stays the same so the operator lands where they
      // were, but inside the new tenant.
      window.location.reload();
    } else {
      open = false;
    }
  }
</script>

<div class="tenant-switcher" bind:this={rootEl}>
  {#if $tenants.memberships.length <= 1}
    <!-- Single tenant: static badge, no dropdown chrome. -->
    {#if $tenants.active}
      <span class="static" title={$tenants.active.tenant.id}>
        {$tenants.active.tenant.name}
        <Badge variant="neutral">{$tenants.active.role}</Badge>
      </span>
    {/if}
  {:else}
    <button class="trigger" on:click={() => (open = !open)} aria-haspopup="menu" aria-expanded={open}>
      {#if $tenants.active}
        <span class="name">{$tenants.active.tenant.name}</span>
        <Badge variant="neutral">{$tenants.active.role}</Badge>
      {:else}
        <span class="name">{t($locale, 'tenants.pick')}</span>
      {/if}
      <span class="chev" aria-hidden="true">▾</span>
    </button>
    {#if open}
      <ul role="menu" class="menu">
        {#each $tenants.memberships as m (m.tenant.id)}
          <li>
            <button
              role="menuitem"
              class="item"
              class:active={m.is_active}
              on:click={() => pick(m.tenant.id)}>
              <span class="name">{m.tenant.name}</span>
              <Badge variant={m.is_active ? 'success' : 'neutral'}>{m.role}</Badge>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>

<style>
  .tenant-switcher {
    position: relative;
    display: inline-flex;
    align-items: center;
  }
  .static,
  .trigger {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding-inline: 0.75rem;
    padding-block: 0.375rem;
    border-radius: 0.5rem;
    background: var(--surface-2);
    color: var(--text);
    border: 1px solid var(--border);
    font-size: 0.875rem;
  }
  .trigger {
    cursor: pointer;
  }
  .trigger:hover {
    background: var(--surface);
  }
  .chev {
    color: var(--text-muted);
    font-size: 0.7rem;
  }
  .menu {
    position: absolute;
    top: calc(100% + 0.25rem);
    inset-inline-end: 0;
    min-width: 12rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 0.625rem;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
    list-style: none;
    margin: 0;
    padding: 0.25rem;
    z-index: 50;
  }
  .item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    width: 100%;
    padding-inline: 0.625rem;
    padding-block: 0.5rem;
    border: 0;
    background: transparent;
    border-radius: 0.5rem;
    color: var(--text);
    font: inherit;
    cursor: pointer;
    text-align: start;
  }
  .item:hover {
    background: var(--surface-2);
  }
  .item.active {
    background: var(--surface-2);
  }
  @media (prefers-reduced-motion: reduce) {
    .menu {
      box-shadow: none;
    }
  }
</style>
