<!--
  Studio header — the chrome strip at the top of /pipelines/{id}/studio.
  Communicates:

    • Pipeline name (large, left)
    • Enabled toggle (drives the live executor on/off)
    • State chip — mirrors the studio store's `state`. Eight values, one
      mapping each — keep this table in sync with StudioState in
      $lib/stores/studio.ts.
    • Validate / Deploy buttons. Deploy is disabled in 'error' state.

  Below the row, a cyan progress strip animates while the deploy is in
  flight (state === 'deploying'). The bar uses `--info` for the colour
  per the spec — no raw hex.

  Wave 1 / Task 8 — the events are emitted as DOM events. Wiring (what
  Validate / Deploy actually do) lands in Tasks 11 / 12.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Button from '$lib/components/Button.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import type { StudioState } from '$lib/stores/studio';
  import { locale, t } from '$lib/stores/locale';

  export let pipelineId: string;
  export let name: string;
  export let enabled: boolean;
  export let dirtyCount: number;
  export let state: StudioState;
  export let latestRev: number | null = null;
  export let deployedRev: number | null = null;
  // For state='version-comparing'; the parent (Studio) passes the
  // compare-from/to once the comparison is open. Both default to null
  // so the chip falls back to a generic "Comparing" label if a future
  // caller forgets to thread them through.
  export let comparisonFrom: number | null = null;
  export let comparisonTo: number | null = null;

  const dispatch = createEventDispatcher<{
    validate: void;
    deploy: void;
    'toggle-enabled': boolean;
  }>();

  // Map state → chip descriptor. Centralised here so a behaviour change
  // can't drift between the visual cue and the underlying state. Tasks
  // 9-12 only ever consume StudioState through the store, so the only
  // place the literal labels live is here. The locale strings are short
  // nouns/verbs ("Deploying", "Comparing") and the dynamic bits
  // (revision number, dirty count) get appended client-side — `t()` is
  // a flat key lookup with no interpolation by design.
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

  // Re-emit Switch's two-way bind as a semantic event so the parent can
  // persist or revert it. Without this the parent only sees the new
  // `enabled` value after Svelte's reactive cycle has already mutated
  // it locally — fine for state, no good for round-trips.
  function onToggle() {
    dispatch('toggle-enabled', enabled);
  }
</script>

<header class="studio-header" data-pipeline-id={pipelineId}>
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
   * Borders + surfaces all read from brand tokens — no raw hex.
   */
  .studio-header {
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border-block-end: 1px solid var(--border);
  }
  .studio-header-row {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.75rem 1rem;
    min-block-size: 56px;
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
    font-size: 1.125rem;
    font-weight: 600;
    color: var(--text);
    line-height: 1.2;
    margin: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-inline-size: 24rem;
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
  /* RTL — flip the moving highlight so it still travels start→end. */
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

  /* Compact actions on narrow screens — keep the chip + name on top,
     actions wrap under. The PageHeader pattern uses the same
     flex-wrap-on-narrow-screen behaviour. */
  @media (max-inline-size: 640px) {
    .studio-header-row {
      flex-wrap: wrap;
      align-items: flex-start;
    }
    .studio-header-actions {
      margin-inline-start: 0;
    }
  }
</style>
