<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth';
  import { locale, t } from '$lib/stores/locale';
  import ThemeToggle from '$lib/components/ThemeToggle.svelte';
  import LocaleToggle from '$lib/components/LocaleToggle.svelte';

  onMount(async () => {
    await auth.refresh();
    if (!$auth.user && $page.url.pathname !== '/login') {
      goto('/login');
    }
  });

  async function logout() {
    await auth.logout();
    goto('/login');
  }

  $: showChrome = $page.url.pathname !== '/login' && !!$auth.user;
  $: navItems = [
    { href: '/', label: t($locale, 'nav.overview') },
    { href: '/connections', label: t($locale, 'nav.connections') },
    { href: '/pipelines', label: t($locale, 'nav.pipelines') },
    { href: '/flow', label: t($locale, 'nav.flow') },
    { href: '/dlq', label: t($locale, 'nav.dlq') },
    { href: '/metrics', label: t($locale, 'nav.metrics') }
  ];
</script>

{#if showChrome}
  <div class="flex min-h-screen">
    <!-- Sidebar -->
    <aside
      class="w-64 flex-shrink-0 border-e flex flex-col"
      style="background: var(--surface); border-color: var(--border);"
    >
      <!-- Brand strip -->
      <div style="height: 3px; background: var(--brand-gradient);"></div>

      <div class="p-5">
        <h1 class="text-lg font-semibold" style="color: var(--text)">
          {t($locale, 'app.title')}
        </h1>
        <p class="text-xs mt-1" style="color: var(--text-muted)">
          {t($locale, 'app.subtitle')}
        </p>
      </div>

      <nav class="px-2 py-2 flex-1 space-y-1">
        {#each navItems as item}
          {@const active = $page.url.pathname === item.href ||
            (item.href !== '/' && $page.url.pathname.startsWith(item.href))}
          <a
            href={item.href}
            class="block rounded-interactive px-3 py-2 text-sm min-h-touch flex items-center"
            style:background={active ? 'var(--surface-2)' : 'transparent'}
            style:color={active ? 'var(--secondary)' : 'var(--text)'}
            style:font-weight={active ? '600' : '500'}
          >
            {item.label}
          </a>
        {/each}
      </nav>

      <div class="p-2 border-t" style="border-color: var(--border);">
        <button
          on:click={logout}
          class="block w-full text-start rounded-interactive px-3 py-2 text-sm min-h-touch"
          style="color: var(--text); background: transparent;"
        >
          {t($locale, 'nav.logout')}
        </button>
      </div>
    </aside>

    <!-- Main -->
    <div class="flex-1 flex flex-col min-w-0">
      <header
        class="flex items-center justify-between px-6 py-3 border-b"
        style="background: var(--surface); border-color: var(--border);"
      >
        <div class="text-sm" style="color: var(--text-muted)">
          {#if $auth.user}
            <span style="color: var(--text)">
              {$auth.user.preferred_username || $auth.user.name || $auth.user.sub}
            </span>
          {/if}
        </div>
        <div class="flex items-center gap-2">
          <LocaleToggle />
          <ThemeToggle />
        </div>
      </header>

      <main class="flex-1 p-6 overflow-auto">
        <slot />
      </main>
    </div>
  </div>
{:else}
  <slot />
{/if}
