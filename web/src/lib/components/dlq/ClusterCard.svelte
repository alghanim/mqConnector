<!--
  ClusterCard — left-panel item in the /dlq Cluster console.

  One card per DLQCluster row from GET /api/v1/dlq/clusters. The card
  carries:
    - Title: AI-generated short title (when present) else the cluster
      template, truncated to a single line.
    - AI badge when the title came from the LLM or the deterministic
      fallback (so an operator can tell at a glance which titles came
      from the naming service vs the raw template).
    - Small-caps fingerprint snippet (first 8 hex chars) — gives the
      eye a stable visual anchor across refreshes.
    - Count badge (`variant="count"` → maroon pill per §5.5).
    - "X pipelines · Y stages" affected.
    - "last seen Nm ago" — relative time.
    - Pulse-dot when last_seen is within the last 5 minutes (cluster
      is actively recurring right now).

  Selection state is owned by the parent; we expose `selected` as a
  prop and dispatch `select` on click + Enter / Space.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import Badge from '$lib/components/Badge.svelte';
  import AINameBadge from './AINameBadge.svelte';
  import type { DLQCluster } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';

  export let cluster: DLQCluster;
  export let selected = false;

  const dispatch = createEventDispatcher<{ select: { cluster: DLQCluster } }>();

  // Fingerprint snippet. Backend fingerprints are 16 hex chars; we
  // show the first 8 so the badge stays inline-friendly. Full value
  // remains available via the title attribute for hover inspection.
  $: fpShort = cluster.fingerprint.slice(0, 8);

  // Title: AI name (when present) wins; falls back to the raw template.
  // We truncate at ~64 chars at display time so a long template can't
  // blow the card layout — full text in title attr.
  $: titleText = cluster.ai_name?.title?.trim() || cluster.template || '(unnamed cluster)';
  $: titleTrimmed = titleText.length > 64 ? titleText.slice(0, 64) + '…' : titleText;

  // "Pulse dot" — active in the last 5 minutes. Anything older is
  // dormant; we just show last-seen relative time without the pulse.
  $: isActive = (() => {
    const t = Date.parse(cluster.last_seen);
    if (Number.isNaN(t)) return false;
    return Date.now() - t < 5 * 60_000;
  })();

  // Local copy of the formatRelative helper. Same logic as the page,
  // kept inline so the card has no dep on the parent. We accept the
  // current locale via the store so re-renders stay reactive.
  function formatRelative(ts: string | undefined, lc: 'en' | 'ar'): string {
    if (!ts) return '';
    const at = Date.parse(ts);
    if (Number.isNaN(at)) return '';
    const diffSec = (Date.now() - at) / 1000;
    const abs = Math.abs(diffSec);
    const past = diffSec >= 0;
    type Unit = 'second' | 'minute' | 'hour' | 'day';
    let value: number;
    let unit: Unit;
    if (abs < 60) {
      value = Math.max(1, Math.round(abs));
      unit = 'second';
    } else if (abs < 3600) {
      value = Math.round(abs / 60);
      unit = 'minute';
    } else if (abs < 86_400) {
      value = Math.round(abs / 3600);
      unit = 'hour';
    } else {
      value = Math.round(abs / 86_400);
      unit = 'day';
    }
    const key = `time.${unit}${value === 1 ? '' : 's'}`;
    const unitLabel = t(lc, key);
    return past
      ? t(lc, 'time.ago').replace('{n}', String(value)).replace('{unit}', unitLabel)
      : t(lc, 'time.in').replace('{n}', String(value)).replace('{unit}', unitLabel);
  }

  $: lastSeenRel = formatRelative(cluster.last_seen, $locale);
  $: affectedLine = t($locale, 'dlq.clusters.card.affected')
    .replace('{n}', String(cluster.pipelines_affected.length))
    .replace('{m}', String(cluster.failing_stages.length));

  function onClick() {
    dispatch('select', { cluster });
  }
  function onKey(e: KeyboardEvent) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      dispatch('select', { cluster });
    }
  }
</script>

<button
  type="button"
  class="cluster-card"
  class:is-selected={selected}
  data-fingerprint={cluster.fingerprint}
  data-testid="cluster-card"
  aria-pressed={selected}
  on:click={onClick}
  on:keydown={onKey}
>
  <div class="cluster-card-head">
    <span class="cluster-card-title" title={titleText}>{titleTrimmed}</span>
    {#if cluster.ai_source}
      <AINameBadge source={cluster.ai_source} />
    {/if}
  </div>
  <div class="cluster-card-meta">
    <code class="cluster-card-fp" title={cluster.fingerprint}>{fpShort}</code>
    <Badge variant="count">{cluster.count}</Badge>
  </div>
  <p class="cluster-card-affected">{affectedLine}</p>
  <div class="cluster-card-foot">
    {#if isActive}
      <span class="cluster-card-pulse" aria-label={t($locale, 'dlq.clusters.card.recent')}>
        <span class="cluster-card-pulse-dot" aria-hidden="true"></span>
      </span>
    {/if}
    <time class="cluster-card-when" datetime={cluster.last_seen} title={cluster.last_seen}>
      {t($locale, 'dlq.clusters.card.lastSeen').replace('{when}', lastSeenRel || cluster.last_seen)}
    </time>
  </div>
</button>

<style>
  .cluster-card {
    appearance: none;
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    inline-size: 100%;
    text-align: start;
    padding: 0.625rem 0.75rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 10px;
    cursor: pointer;
    color: inherit;
    font: inherit;
    transition: background 120ms ease, border-color 120ms ease, transform 80ms ease;
  }
  .cluster-card:hover {
    background: var(--surface-2);
    border-color: var(--border-strong);
  }
  .cluster-card:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .cluster-card.is-selected {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 8%, var(--surface));
    box-shadow: inset 3px 0 0 var(--accent);
  }
  :global([dir='rtl']) .cluster-card.is-selected {
    box-shadow: inset -3px 0 0 var(--accent);
  }
  .cluster-card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.375rem;
    min-inline-size: 0;
  }
  .cluster-card-title {
    flex: 1;
    color: var(--text);
    font-size: 0.8125rem;
    font-weight: 600;
    line-height: 1.3;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-inline-size: 0;
  }
  .cluster-card-meta {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .cluster-card-fp {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.6875rem;
    color: var(--text-tertiary);
    letter-spacing: 0.04em;
    text-transform: lowercase;
  }
  .cluster-card-affected {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.6875rem;
  }
  .cluster-card-foot {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    color: var(--text-tertiary);
    font-size: 0.6875rem;
  }
  .cluster-card-pulse {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    inline-size: 0.5rem;
    block-size: 0.5rem;
    position: relative;
  }
  .cluster-card-pulse-dot {
    inline-size: 0.5rem;
    block-size: 0.5rem;
    border-radius: 50%;
    background: var(--danger);
    box-shadow: 0 0 0 0 color-mix(in srgb, var(--danger) 60%, transparent);
    animation: cluster-pulse 1.6s ease-out infinite;
  }
  .cluster-card-when {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }
  @keyframes cluster-pulse {
    0%   { box-shadow: 0 0 0 0 color-mix(in srgb, var(--danger) 60%, transparent); }
    70%  { box-shadow: 0 0 0 6px color-mix(in srgb, var(--danger) 0%, transparent); }
    100% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--danger) 0%, transparent); }
  }
  @media (prefers-reduced-motion: reduce) {
    .cluster-card-pulse-dot {
      animation: none;
    }
    .cluster-card {
      transition: none;
    }
  }
</style>
