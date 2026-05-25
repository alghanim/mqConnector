<!--
  StageOutcomeStrip (DLQ variant) — compact per-stage outcome row used
  by the DLQ Action Drawer's replay-sim panel.

  This is intentionally a slimmed sibling of
  studio/dryrun/StageOutcomeStrip.svelte. The studio version dispatches
  canvas-overlay events and opens a PayloadDiffView on cell click —
  features that only make sense INSIDE the Studio canvas. The DLQ
  drawer just needs the strip's read-only visual treatment: per-stage
  pill, body preview, duration tier, failure marker.

  Duplicating instead of generalising the studio component was the
  cheaper call:
    * Pulling the studio component into here drags in studio types +
      a Dialog instance through PayloadDiffView.
    * The DLQ drawer has its own compare-to-other-entry flow that
      uses the charts/PayloadDiff primitive, not the studio's.
  See task spec, Part C: "better to duplicate than fight types".
-->
<script lang="ts">
  import { locale, t } from '$lib/stores/locale';
  import type { DLQReplaySimStageRun } from '$lib/api';

  export let runs: DLQReplaySimStageRun[] = [];

  // Preview length matches the studio strip — 80 chars feels right
  // for a 12-rem cell width.
  const PREVIEW_LEN = 80;

  function bodyPreview(body: string | undefined): string {
    if (!body) return '';
    const trimmed = body.trim();
    return trimmed.length > PREVIEW_LEN ? trimmed.slice(0, PREVIEW_LEN) + '…' : trimmed;
  }

  function durationMs(ns: number): number {
    return Math.round(ns / 1e6);
  }

  type DurationTier = 'fast' | 'normal' | 'slow' | 'very-slow';
  function durationTier(ms: number): DurationTier {
    if (ms < 2) return 'fast';
    if (ms <= 50) return 'normal';
    if (ms <= 250) return 'slow';
    return 'very-slow';
  }

  // Index of the first failed run, or -1 if every run succeeded.
  $: failedFrom = (() => {
    for (let i = 0; i < runs.length; i++) {
      if (runs[i].failed) return i;
    }
    return -1;
  })();
</script>

<div class="strip" role="list" aria-label={t($locale, 'dlq.clusters.drawer.replaySim')}>
  {#each runs as run, idx (run.name + '-' + idx)}
    {@const ms = durationMs(run.duration_ns)}
    {@const tier = durationTier(ms)}
    {@const downstream = failedFrom !== -1 && idx > failedFrom}
    <div
      class="strip-cell"
      class:is-failed={run.failed}
      class:is-downstream={downstream}
      role="listitem"
      data-stage-name={run.name}
      data-failed={run.failed ? 'true' : 'false'}
      data-tier={tier}
    >
      <div class="strip-cell-head">
        <span class="strip-cell-name" title={run.name}>{run.name}</span>
        <span class="strip-cell-pill strip-cell-pill-{tier}" aria-label="{ms} ms">{ms} ms</span>
      </div>
      <pre class="strip-cell-body">{bodyPreview(run.body)}</pre>
      {#if run.format}
        <span class="strip-cell-format">{run.format}</span>
      {/if}
      {#if run.failed && run.err}
        <p class="strip-cell-err" role="alert">{run.err}</p>
      {/if}
    </div>
  {/each}
</div>

<style>
  .strip {
    display: flex;
    gap: 0.5rem;
    overflow-x: auto;
    overflow-y: hidden;
    padding-block-end: 0.25rem;
    min-inline-size: 0;
  }
  .strip-cell {
    inline-size: 10rem;
    flex-shrink: 0;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .strip-cell.is-failed {
    border-color: var(--danger);
  }
  .strip-cell.is-downstream {
    opacity: 0.55;
  }
  .strip-cell-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.25rem;
  }
  .strip-cell-name {
    font-size: 0.75rem;
    font-weight: 600;
    color: var(--text);
    text-transform: capitalize;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-inline-size: 0;
  }
  .strip-cell-pill {
    font-size: 0.625rem;
    font-weight: 600;
    padding-block: 0.0625rem;
    padding-inline: 0.375rem;
    border-radius: 999px;
    white-space: nowrap;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }
  .strip-cell-pill-fast {
    background: var(--success-bg);
    color: var(--success);
  }
  .strip-cell-pill-normal {
    background: var(--surface-high);
    color: var(--text-muted);
  }
  .strip-cell-pill-slow {
    background: var(--warning-bg);
    color: var(--warning);
  }
  .strip-cell-pill-very-slow {
    background: var(--danger-bg);
    color: var(--danger);
  }
  .strip-cell-body {
    margin: 0;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 0.25rem 0.375rem;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.625rem;
    line-height: 1.4;
    color: var(--text);
    max-block-size: 3.5rem;
    overflow: hidden;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .strip-cell-format {
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-tertiary);
    font-weight: 600;
    font-size: 0.625rem;
  }
  .strip-cell-err {
    margin: 0;
    color: var(--danger);
    font-size: 0.625rem;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    white-space: pre-wrap;
    word-break: break-word;
  }
</style>
