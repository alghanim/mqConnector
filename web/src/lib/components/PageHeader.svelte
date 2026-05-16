<!--
  PageHeader — the heading band on every top-level page. Communicates:

    • Title (big and unambiguous)
    • Subtitle (one-line description of the page's job)
    • Count badge (e.g. "12 connections")
    • Stat chips (optional — short, scannable numbers)
    • Primary action slot ("Add connection", "Reload", etc.)
    • Secondary action slot
    • Filter / search slot (below the title row)

  Every page that doesn't use this is silently second-class. Use it.
-->
<script lang="ts">
  export let title: string;
  export let subtitle: string | undefined = undefined;
  export let count: number | undefined = undefined;
</script>

<header class="ph">
  <div class="ph-row">
    <div class="ph-meta">
      <h1 class="ph-title">
        {title}
        {#if count !== undefined}
          <span class="ph-count">{count}</span>
        {/if}
      </h1>
      {#if subtitle}
        <p class="ph-sub">{subtitle}</p>
      {/if}
    </div>
    <div class="ph-actions">
      <slot name="secondary" />
      <slot name="primary" />
    </div>
  </div>

  {#if $$slots.stats}
    <div class="ph-stats">
      <slot name="stats" />
    </div>
  {/if}

  {#if $$slots.filters}
    <div class="ph-filters">
      <slot name="filters" />
    </div>
  {/if}
</header>

<style>
  .ph {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    margin-block-end: 1.25rem;
  }
  .ph-row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
    flex-wrap: wrap;
  }
  .ph-meta {
    min-width: 0;
  }
  .ph-title {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin: 0;
    color: var(--text);
    font-size: 1.5rem;
    font-weight: 600;
    letter-spacing: -0.01em;
  }
  .ph-count {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    height: 22px;
    min-width: 26px;
    padding-inline: 8px;
    border-radius: 999px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text-muted);
    font-size: 0.75rem;
    font-weight: 600;
    letter-spacing: 0.01em;
  }
  .ph-sub {
    margin: 0.375rem 0 0;
    color: var(--text-muted);
    font-size: 0.875rem;
    line-height: 1.5;
    max-width: 60ch;
  }
  .ph-actions {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    flex-shrink: 0;
  }

  .ph-stats {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }

  .ph-filters {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
</style>
