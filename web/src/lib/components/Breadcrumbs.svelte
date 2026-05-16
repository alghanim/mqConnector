<!--
  Derives breadcrumbs from the current URL. Static map of segment → label
  so we don't show "id"-like raw segments unstyled. Renders nothing on the
  root route; otherwise: Overview / Section / Detail.
-->
<script lang="ts">
  import { page } from '$app/stores';
  import { locale, t } from '$lib/stores/locale';
  import { ChevronRight, Home } from 'lucide-svelte';

  // Map of static path segments to translation keys.
  const labels: Record<string, string> = {
    '': 'nav.overview',
    connections: 'nav.connections',
    pipelines: 'nav.pipelines',
    flow: 'nav.flow',
    dlq: 'nav.dlq',
    metrics: 'nav.metrics',
    tenants: 'nav.tenants',
    account: 'profile.account'
  };

  type Crumb = { href: string; label: string; isLast: boolean; isDynamic: boolean };

  $: crumbs = (() => {
    const segs = $page.url.pathname.split('/').filter(Boolean);
    const out: Crumb[] = [];
    let path = '';
    for (let i = 0; i < segs.length; i++) {
      const seg = segs[i];
      path += '/' + seg;
      const tkey = labels[seg];
      const isUUID = /^[0-9a-f]{8}-[0-9a-f]{4}-/.test(seg);
      out.push({
        href: path,
        label: tkey ? t($locale, tkey) : isUUID ? seg.slice(0, 8) + '…' : seg,
        isLast: i === segs.length - 1,
        isDynamic: !tkey
      });
    }
    return out;
  })();

  $: rtl = $locale === 'ar';
</script>

{#if crumbs.length > 0}
  <nav aria-label="breadcrumb" class="crumbs">
    <a href="/" class="crumb home" aria-label={t($locale, 'nav.overview')}>
      <Home size={14} aria-hidden="true" />
    </a>
    {#each crumbs as c (c.href)}
      <span class="sep" aria-hidden="true">
        <ChevronRight size={12} style="transform: {rtl ? 'scaleX(-1)' : 'none'}" />
      </span>
      {#if c.isLast}
        <span class="crumb current" aria-current="page" class:dyn={c.isDynamic}>{c.label}</span>
      {:else}
        <a href={c.href} class="crumb" class:dyn={c.isDynamic}>{c.label}</a>
      {/if}
    {/each}
  </nav>
{/if}

<style>
  .crumbs {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.8125rem;
    color: var(--text-muted);
  }
  .crumb {
    color: var(--text-muted);
    text-decoration: none;
    border-radius: 4px;
    padding-inline: 2px;
    transition: color 150ms;
  }
  .crumb:hover {
    color: var(--text);
  }
  .crumb.current {
    color: var(--text);
    font-weight: 500;
  }
  .crumb.home {
    display: inline-flex;
    align-items: center;
    color: var(--text-muted);
  }
  .crumb.dyn {
    font-family: 'SFMono-Regular', Menlo, monospace;
    font-size: 0.75rem;
  }
  .sep {
    display: inline-flex;
    align-items: center;
    color: var(--text-tertiary);
    opacity: 0.7;
  }
</style>
