<!--
  Studio header — the chrome strip at the top of /pipelines/{id}/studio.
  Communicates:

    Top row:
      • Pipeline name (large, semibold)
      • Enabled toggle
      • Dirty/state chip
      • Validate / Deploy buttons (right-aligned)

    Summary row:
      • Source type-icon + name → Destination type-icon + name (the flow
        summary the operator looks for first when they land here)
      • Right-aligned metric strip with live rev, throughput, failed, DLQ

  A hairline gold brand-gradient strip across the very top edge matches
  the app's top-of-shell strip so the header reads as continuous chrome.

  Below the row, a cyan progress strip animates while the deploy is in
  flight (state === 'deploying') using `--info`.

  State chip table — keep in sync with StudioState in $lib/stores/studio.ts.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Button from '$lib/components/Button.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import ConnectionTypeIcon from '$lib/components/ConnectionTypeIcon.svelte';
  import type { StudioState } from '$lib/stores/studio';
  import type { ConnectionType } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import { ArrowRight } from 'lucide-svelte';

  export let pipelineId: string;
  export let name: string;
  export let enabled: boolean;
  export let dirtyCount: number;
  export let state: StudioState;
  export let latestRev: number | null = null;
  export let deployedRev: number | null = null;
  export let comparisonFrom: number | null = null;
  export let comparisonTo: number | null = null;

  // Summary-strip inputs. Parent threads source + destination connection
  // metadata + live throughput/failed/DLQ counters. All optional — the
  // header degrades to fewer pills if values are missing rather than
  // showing dashes that look like an error.
  export let sourceName: string | null = null;
  export let sourceType: ConnectionType | null = null;
  export let destName: string | null = null;
  export let destType: ConnectionType | null = null;
  export let throughputPerMin: number | null = null;
  export let failedTotal: number | null = null;
  export let dlqTotal: number | null = null;

  const dispatch = createEventDispatcher<{
    validate: void;
    deploy: void;
    'toggle-enabled': boolean;
  }>();

  type ChipDescriptor = {
    label: string;
    variant: 'success' | 'warning' | 'danger' | 'neutral' | 'info';
    pulsing?: boolean;
  };

  function describeState(
    s: StudioState,
    count: number,
    latest: number | null,
    deployed: number | null,
    cmpFrom: number | null,
    cmpTo: number | null
  ): ChipDescriptor {
    switch (s) {
      case 'empty':
        return { label: t($locale, 'studio.chip.empty'), variant: 'neutral' };
      case 'building':
        return { label: t($locale, 'studio.chip.building'), variant: 'neutral' };
      case 'dirty':
        return {
          label: `${t($locale, 'studio.chip.dirty')} (${count})`,
          variant: 'warning'
        };
      case 'validating':
        return {
          label: t($locale, 'studio.chip.validating'),
          variant: 'info',
          pulsing: true
        };
      case 'deploying': {
        const rev = latest ?? deployed ?? 0;
        const label =
          rev > 0
            ? `${t($locale, 'studio.chip.deploying')} rev #${rev}…`
            : `${t($locale, 'studio.chip.deploying')}…`;
        return { label, variant: 'info', pulsing: true };
      }
      case 'error':
        return { label: t($locale, 'studio.chip.error'), variant: 'danger' };
      case 'simulating': {
        const rev = latest ?? 0;
        const label =
          rev > 0
            ? `${t($locale, 'studio.chip.simulating')} #${rev}`
            : t($locale, 'studio.chip.simulating');
        return { label, variant: 'info' };
      }
      case 'version-comparing': {
        const from = cmpFrom ?? '?';
        const to = cmpTo ?? '?';
        return {
          label: `${t($locale, 'studio.chip.comparing')} rev #${from} → #${to}`,
          variant: 'info'
        };
      }
    }
  }

  $: chip = describeState(state, dirtyCount, latestRev, deployedRev, comparisonFrom, comparisonTo);
  $: deployDisabled = state === 'error' || state === 'deploying' || state === 'empty';

  // The metric strip only shows pills the parent has supplied — null
  // values are skipped so we never render an "—" that reads as broken.
  // The revision pill is special-cased: it shows whenever deployedRev
  // OR latestRev is set, biasing toward deployedRev (the live answer).
  $: liveRevDisplay = deployedRev ?? latestRev;

  // Formatter: pretty-print bigger numbers (12345 → "12.3k") so the pill
  // doesn't blow out the row at high traffic. We keep tabular-nums on
  // the value font so the pills don't shimmy as digits change.
  function compactNumber(v: number): string {
    if (v < 1000) return String(v);
    if (v < 1_000_000) return `${(v / 1000).toFixed(v < 10_000 ? 1 : 0)}k`;
    return `${(v / 1_000_000).toFixed(1)}M`;
  }

  function onToggle() {
    dispatch('toggle-enabled', enabled);
  }
</script>

<header class="studio-header" data-pipeline-id={pipelineId}>
  <div class="studio-header-strip" aria-hidden="true"></div>

  <div class="studio-header-row">
    <div class="studio-header-meta">
      <h1 class="studio-header-name" title={name}>{name || '—'}</h1>
      <div class="studio-header-enable">
        <Switch
          bind:checked={enabled}
          label={enabled ? t($locale, 'common.enabled') : t($locale, 'common.disabled')}
          on:change={onToggle}
        />
      </div>
      <span class="studio-header-chip" class:pulsing={chip.pulsing} data-state={state}>
        <Badge variant={chip.variant}>{chip.label}</Badge>
      </span>
    </div>

    <div class="studio-header-actions">
      <Button variant="ghost" on:click={() => dispatch('validate')}>
        {t($locale, 'studio.action.validate')}
      </Button>
      <Button on:click={() => dispatch('deploy')} disabled={deployDisabled}>
        {t($locale, 'studio.action.deploy')}
      </Button>
    </div>
  </div>

  <!--
    Summary row. The flow summary on the left tells the operator at a
    glance which broker pair this pipeline bridges; the metric pills on
    the right tell them whether the bridge is healthy right now. Both
    survive narrow viewports by wrapping below the actions row.
  -->
  <div class="studio-header-sub">
    <div class="studio-header-flow" aria-label={t($locale, 'studio.inspector.overview.flow')}>
      <span class="studio-flow-end" data-end="source">
        <ConnectionTypeIcon type={sourceType ?? undefined} size={14} />
        <span class="studio-flow-name" title={sourceName ?? ''}>
          {sourceName ?? t($locale, 'studio.inspector.overview.noSource')}
        </span>
      </span>
      <span class="studio-flow-arrow" aria-hidden="true">
        <ArrowRight size={14} />
      </span>
      <span class="studio-flow-end" data-end="destination">
        <ConnectionTypeIcon type={destType ?? undefined} size={14} />
        <span class="studio-flow-name" title={destName ?? ''}>
          {destName ?? t($locale, 'studio.inspector.overview.noDestination')}
        </span>
      </span>
    </div>

    <div class="studio-header-metrics" role="group" aria-label="Pipeline metrics">
      {#if liveRevDisplay !== null}
        <span class="studio-metric" data-tone="primary">
          <span class="studio-metric-label">{t($locale, 'studio.header.metric.revision')}</span>
          <span class="studio-metric-value studio-metric-mono">#{liveRevDisplay}</span>
        </span>
      {/if}
      {#if throughputPerMin !== null}
        <span class="studio-metric">
          <span class="studio-metric-label">{t($locale, 'studio.header.metric.throughput')}</span>
          <span class="studio-metric-value">{compactNumber(throughputPerMin)}</span>
        </span>
      {/if}
      {#if failedTotal !== null && failedTotal > 0}
        <span class="studio-metric" data-tone="danger">
          <span class="studio-metric-label">{t($locale, 'studio.header.metric.failed')}</span>
          <span class="studio-metric-value">{compactNumber(failedTotal)}</span>
        </span>
      {/if}
      {#if dlqTotal !== null && dlqTotal > 0}
        <span class="studio-metric" data-tone="warning">
          <span class="studio-metric-label">{t($locale, 'studio.header.metric.dlq')}</span>
          <span class="studio-metric-value">{compactNumber(dlqTotal)}</span>
        </span>
      {/if}
    </div>
  </div>

  {#if state === 'deploying'}
    <div
      class="studio-header-progress"
      role="progressbar"
      aria-label={`${t($locale, 'studio.chip.deploying')}`}
      aria-busy="true"
    ></div>
  {/if}
</header>

<style>
  /*
   * Studio chrome strip. Sits at the top of the three-pane grid.
   * Two rows now: identity/actions + summary (flow + metric pills).
   * Every colour/border reads off brand tokens; no raw hex.
   */
  .studio-header {
    position: relative;
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border-block-end: 1px solid var(--border);
  }
  /* Hairline gold gradient at the very top edge — matches the app
     shell strip so the studio reads as a continuous surface. */
  .studio-header-strip {
    block-size: 2px;
    background: var(--brand-gradient);
    flex: 0 0 auto;
  }

  .studio-header-row {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.625rem 1rem 0.5rem;
    min-block-size: 48px;
  }
  .studio-header-meta {
    display: flex;
    align-items: center;
    gap: 0.875rem;
    flex: 1;
    min-inline-size: 0;
    flex-wrap: wrap;
  }
  .studio-header-name {
    font-size: 1.25rem;
    font-weight: 600;
    color: var(--text);
    line-height: 1.2;
    margin: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-inline-size: 24rem;
    letter-spacing: -0.005em;
  }
  .studio-header-enable {
    display: inline-flex;
    align-items: center;
  }
  .studio-header-chip {
    display: inline-flex;
    align-items: center;
  }
  .studio-header-chip.pulsing {
    animation: studio-pulse 1.6s ease-in-out infinite;
  }
  @keyframes studio-pulse {
    0%, 100% { opacity: 1; }
    50%      { opacity: 0.55; }
  }
  @media (prefers-reduced-motion: reduce) {
    .studio-header-chip.pulsing { animation: none; }
  }

  .studio-header-actions {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    margin-inline-start: auto;
  }

  /* ── Summary row ───────────────────────────────────────────────── */
  .studio-header-sub {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0 1rem 0.625rem;
    flex-wrap: wrap;
    min-block-size: 28px;
  }
  .studio-header-flow {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.8125rem;
    color: var(--text-muted);
    min-inline-size: 0;
    flex: 1;
    flex-wrap: wrap;
  }
  .studio-flow-end {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding-block: 0.125rem;
    padding-inline: 0.5rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    max-inline-size: 16rem;
    min-inline-size: 0;
  }
  .studio-flow-end[data-end='source'] { color: var(--primary); }
  .studio-flow-end[data-end='destination'] { color: var(--primary); }
  .studio-flow-name {
    color: var(--text);
    font-weight: 500;
    font-size: 0.75rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-inline-size: 0;
  }
  .studio-flow-arrow {
    color: var(--text-tertiary);
    display: inline-flex;
    align-items: center;
  }
  /* RTL: the canvas's compass is locked LTR, but the header flow
     summary follows the document direction so it reads naturally in
     Arabic — the arrow stays pointing inline-end via the icon's
     rotation. */
  :global([dir='rtl']) .studio-flow-arrow {
    transform: scaleX(-1);
  }

  .studio-header-metrics {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    margin-inline-start: auto;
    flex-wrap: wrap;
  }
  .studio-metric {
    display: inline-flex;
    align-items: baseline;
    gap: 0.375rem;
    padding-block: 0.125rem;
    padding-inline: 0.5rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 999px;
    color: var(--text);
    font-size: 0.75rem;
    line-height: 1.2;
  }
  .studio-metric-label {
    color: var(--text-tertiary);
    font-size: 0.6875rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-weight: 600;
  }
  .studio-metric-value {
    font-weight: 700;
    color: var(--text);
    font-variant-numeric: tabular-nums;
  }
  .studio-metric-mono {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-weight: 600;
  }
  .studio-metric[data-tone='primary'] .studio-metric-value {
    color: var(--primary);
  }
  .studio-metric[data-tone='danger'] .studio-metric-value {
    color: var(--danger);
  }
  .studio-metric[data-tone='warning'] .studio-metric-value {
    color: var(--warning);
  }

  .studio-header-progress {
    block-size: 4px;
    background: linear-gradient(
      90deg,
      transparent 0%,
      var(--info) 50%,
      transparent 100%
    );
    background-size: 200% 100%;
    animation: studio-progress 1.4s linear infinite;
  }
  @keyframes studio-progress {
    0%   { background-position: 200% 0; }
    100% { background-position: -200% 0; }
  }
  :global([dir='rtl']) .studio-header-progress {
    animation-direction: reverse;
  }
  @media (prefers-reduced-motion: reduce) {
    .studio-header-progress {
      animation: none;
      background: var(--info);
      opacity: 0.6;
    }
  }

  @media (max-inline-size: 640px) {
    .studio-header-row {
      flex-wrap: wrap;
      align-items: flex-start;
    }
    .studio-header-actions {
      margin-inline-start: 0;
    }
    .studio-header-metrics {
      margin-inline-start: 0;
    }
  }
</style>
