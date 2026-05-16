<!--
  EmptyState — drop-in panel for "you have no rows yet" screens. Shows
  a brand-aligned illustration, a real headline, a helper paragraph,
  and the page's primary CTA. NEVER replace this with the string
  "no items" — that's a sentence, not a UI.

  Illustrations are inline SVG. They use brand tokens so dark/light
  swap automatically.

  Usage:
    <EmptyState
      illustration="connections"
      title={t($locale, 'empty.connections.title')}
      body={t($locale, 'empty.connections.body')}>
      <Button slot="action" on:click={startNew}>Add connection</Button>
    </EmptyState>
-->
<script lang="ts">
  export let illustration: 'connections' | 'pipelines' | 'dlq' | 'metrics' | 'tenants' | 'audit' =
    'connections';
  export let title = '';
  export let body = '';
</script>

<div class="empty">
  <div class="empty-art" aria-hidden="true">
    {#if illustration === 'connections'}
      <svg viewBox="0 0 200 120" width="200" height="120">
        <defs>
          <linearGradient id="es-gold" x1="0" y1="0" x2="1" y2="1">
            <stop offset="0%" stop-color="#F8E08E" stop-opacity=".8" />
            <stop offset="100%" stop-color="#8F6A2A" stop-opacity=".6" />
          </linearGradient>
        </defs>
        <rect x="14" y="40" width="60" height="40" rx="10" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <rect x="126" y="40" width="60" height="40" rx="10" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <path d="M74 60 H126" stroke="url(#es-gold)" stroke-width="2.5" stroke-dasharray="4 4" />
        <circle cx="100" cy="60" r="6" fill="#8B153D" />
        <text x="44" y="65" text-anchor="middle" fill="var(--text-muted)" font-size="10" font-family="SFMono-Regular, Menlo, monospace">SRC</text>
        <text x="156" y="65" text-anchor="middle" fill="var(--text-muted)" font-size="10" font-family="SFMono-Regular, Menlo, monospace">DST</text>
      </svg>
    {:else if illustration === 'pipelines'}
      <svg viewBox="0 0 200 120" width="200" height="120">
        <defs>
          <linearGradient id="es-pipe" x1="0" y1="0" x2="1" y2="0">
            <stop offset="0%" stop-color="#F8E08E" stop-opacity=".75" />
            <stop offset="100%" stop-color="#8F6A2A" stop-opacity=".55" />
          </linearGradient>
        </defs>
        <circle cx="22" cy="60" r="10" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <rect x="50" y="50" width="36" height="20" rx="6" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <rect x="96" y="50" width="36" height="20" rx="6" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <rect x="142" y="50" width="36" height="20" rx="6" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <circle cx="190" cy="60" r="10" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <path d="M32 60 H50 M86 60 H96 M132 60 H142 M178 60 H180" stroke="url(#es-pipe)" stroke-width="2" />
        <circle cx="68" cy="60" r="2" fill="#8B153D" />
        <circle cx="114" cy="60" r="2" fill="#8B153D" />
        <circle cx="160" cy="60" r="2" fill="#8B153D" />
      </svg>
    {:else if illustration === 'dlq'}
      <svg viewBox="0 0 200 120" width="200" height="120">
        <rect x="50" y="30" width="100" height="60" rx="10" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <path d="M70 60 H130" stroke="var(--text-tertiary)" stroke-width="1.5" stroke-dasharray="3 3" />
        <circle cx="100" cy="20" r="14" fill="none" stroke="#3FB950" stroke-width="2.5" />
        <path d="M93 20 l5 5 l9 -10" stroke="#3FB950" stroke-width="2.5" fill="none" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
    {:else if illustration === 'metrics'}
      <svg viewBox="0 0 200 120" width="200" height="120">
        <defs>
          <linearGradient id="es-line" x1="0" y1="0" x2="1" y2="0">
            <stop offset="0%" stop-color="#F8E08E" />
            <stop offset="100%" stop-color="#8F6A2A" />
          </linearGradient>
        </defs>
        <line x1="20" y1="100" x2="180" y2="100" stroke="var(--border-strong)" />
        <line x1="20" y1="20" x2="20" y2="100" stroke="var(--border-strong)" />
        <polyline points="22,80 50,72 80,82 110,52 140,60 180,32" fill="none" stroke="url(#es-line)" stroke-width="2.5" stroke-linejoin="round" stroke-linecap="round" />
        <circle cx="180" cy="32" r="3.5" fill="#8B153D" />
      </svg>
    {:else if illustration === 'tenants'}
      <svg viewBox="0 0 200 120" width="200" height="120">
        <rect x="14" y="46" width="50" height="50" rx="8" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <rect x="74" y="32" width="50" height="64" rx="8" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <rect x="134" y="50" width="50" height="46" rx="8" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <circle cx="39" cy="38" r="6" fill="#8B153D" />
        <circle cx="99" cy="22" r="6" fill="#F8E08E" stroke="#8F6A2A" stroke-width="1" />
        <circle cx="159" cy="42" r="6" fill="#A59F8A" />
      </svg>
    {:else}
      <svg viewBox="0 0 200 120" width="200" height="120">
        <rect x="40" y="40" width="120" height="50" rx="8" fill="var(--surface-2)" stroke="var(--border-strong)" />
        <line x1="55" y1="55" x2="135" y2="55" stroke="var(--border-strong)" />
        <line x1="55" y1="65" x2="105" y2="65" stroke="var(--border-strong)" />
        <line x1="55" y1="75" x2="125" y2="75" stroke="var(--border-strong)" />
      </svg>
    {/if}
  </div>
  <h3 class="empty-title">{title}</h3>
  <p class="empty-body">{body}</p>
  {#if $$slots.action}
    <div class="empty-cta">
      <slot name="action" />
    </div>
  {/if}
  {#if $$slots.helper}
    <div class="empty-helper">
      <slot name="helper" />
    </div>
  {/if}
</div>

<style>
  .empty {
    text-align: center;
    padding: 2.5rem 1.5rem;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.75rem;
  }
  .empty-art {
    width: 200px;
    height: 120px;
    color: var(--text);
    margin-bottom: 0.25rem;
  }
  .empty-title {
    margin: 0;
    color: var(--text);
    font-size: 1.0625rem;
    font-weight: 600;
    letter-spacing: -0.005em;
  }
  .empty-body {
    margin: 0;
    max-width: 36rem;
    color: var(--text-muted);
    font-size: 0.875rem;
    line-height: 1.55;
  }
  .empty-cta {
    margin-top: 0.5rem;
  }
  .empty-helper {
    margin-top: 0.25rem;
    color: var(--text-tertiary);
    font-size: 0.75rem;
  }
</style>
