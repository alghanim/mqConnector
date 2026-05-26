<!--
  StageOutcomeStrip — horizontal scrollable row of per-stage outcome
  cells, rendered inside the DryRunDock after a /preview run completes.

  Layout: one column per stage_run from the preview response. Each
  column shows:
    - stage name (top)
    - duration pill (color-graded by latency)
    - first-80-char body preview
    - tiny diff badge vs the previous stage's body (≠ shows a chevron)
    - error inline (and downstream cells greyed) on failure

  Click a cell with index > 0 → opens <PayloadDiffView> with this body
  and the previous stage's body. The first cell can't diff against
  anything (there's no upstream) so it's not click-targetable.

  Side-effect: the strip dispatches an 'overlays' event with the
  per-stage outcome zipped against the draft's stage IDs (the runs use
  stage.name; the canvas needs stage.id). The dock forwards the event
  to <StudioCanvas> via bind so the overlay dots/badges land on the
  graph.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { locale, t } from '$lib/stores/locale';
  import type { Stage } from '$lib/api';
  import PayloadDiffView from './PayloadDiffView.svelte';
  import type { StageRun, CanvasOverlay } from './types';

  export let runs: StageRun[] = [];
  // The draft stages (in stage_order) — used purely for the runs→ids
  // zip that produces canvas overlays. Optional; if absent we still
  // render the strip but skip the overlay dispatch.
  export let stages: Stage[] = [];

  const dispatch = createEventDispatcher<{
    overlays: CanvasOverlay[];
  }>();

  // Body-preview clip length. 80 chars is the spec; we strip whitespace
  // first so a JSON pretty-print doesn't waste the budget on indentation.
  const PREVIEW_LEN = 80;

  function bodyPreview(body: string | undefined): string {
    if (!body) return '';
    const trimmed = body.trim();
    return trimmed.length > PREVIEW_LEN ? trimmed.slice(0, PREVIEW_LEN) + '…' : trimmed;
  }

  function durationMs(ns: number): number {
    return Math.round(ns / 1e6);
  }

  // Pill color thresholds. Match the spec — green <2ms, neutral 2-50,
  // amber 50-250, red >250.
  type DurationTier = 'fast' | 'normal' | 'slow' | 'very-slow';
  function durationTier(ms: number): DurationTier {
    if (ms < 2) return 'fast';
    if (ms <= 50) return 'normal';
    if (ms <= 250) return 'slow';
    return 'very-slow';
  }

  // diffMark returns 'changed' if the body differs from the previous
  // stage's body, 'same' if not, and null for the first cell (no
  // upstream to diff against). The strip uses this to render a small
  // chevron pill.
  function diffMark(idx: number): 'changed' | 'same' | null {
    if (idx === 0) return null;
    const prev = runs[idx - 1]?.body ?? '';
    const cur = runs[idx]?.body ?? '';
    return prev === cur ? 'same' : 'changed';
  }

  // failedFrom returns the index of the first failed run, or -1 if
  // every run succeeded. Cells at index > failedFrom render greyed so
  // it's obvious nothing downstream actually ran.
  $: failedFrom = (() => {
    for (let i = 0; i < runs.length; i++) {
      if (runs[i].failed) return i;
    }
    return -1;
  })();

  // ─── Canvas overlay dispatch ────────────────────────────────────
  // The runs list uses stage names (set by pipeline.Build). Stages on
  // the draft carry the canvas's data-node-id verbatim. We zip them by
  // index — the backend executor walks stages in stage_order, same
  // ordering the canvas uses, so positional zip is safe.
  $: {
    const overlays: CanvasOverlay[] = [];
    const n = Math.min(runs.length, stages.length);
    for (let i = 0; i < n; i++) {
      const sid = stages[i]?.id ?? '';
      if (!sid) continue;
      overlays.push({
        stageId: sid,
        failed: runs[i].failed,
        durationMs: durationMs(runs[i].duration_ns)
      });
    }
    // Dispatch on every reactive update — the dock listens and forwards
    // to the canvas. Empty list is meaningful too (canvas clears any
    // prior overlay).
    dispatch('overlays', overlays);
  }

  // Diff modal state.
  let diffOpen = false;
  let diffBefore = '';
  let diffAfter = '';
  let diffFormat = '';
  let diffBeforeLabel = '';
  let diffAfterLabel = '';

  function openDiff(idx: number) {
    if (idx === 0) return;
    const prev = runs[idx - 1];
    const cur = runs[idx];
    diffBefore = prev?.body ?? '';
    diffAfter = cur?.body ?? '';
    diffFormat = cur?.format ?? prev?.format ?? '';
    diffBeforeLabel = prev?.name ?? '';
    diffAfterLabel = cur?.name ?? '';
    diffOpen = true;
  }

  function onCellKey(e: KeyboardEvent, idx: number) {
    if (idx === 0) return;
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      openDiff(idx);
    }
  }
</script>

<div class="strip" role="list" aria-label={t($locale, 'studio.dryrun.strip.heading')}>
  {#if runs.length === 0}
    <p class="strip-empty">{t($locale, 'studio.dryrun.strip.empty')}</p>
  {:else}
    {#each runs as run, idx (run.name + '-' + idx)}
      {@const ms = durationMs(run.duration_ns)}
      {@const tier = durationTier(ms)}
      {@const mark = diffMark(idx)}
      {@const downstream = failedFrom !== -1 && idx > failedFrom}
      <!-- A "listitem" that's also activatable for the diff view. We use
           role="button" when clickable so the tabindex + keyboard handler
           are coherent with a11y; non-clickable cells stay listitems. The
           a11y linter can't see through the conditional role/tabindex
           pairing — both are valid in their respective branches. -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div
        class="strip-cell"
        class:is-failed={run.failed}
        class:is-downstream={downstream}
        class:is-clickable={idx > 0}
        role={idx > 0 ? 'button' : 'listitem'}
        data-stage-name={run.name}
        data-failed={run.failed ? 'true' : 'false'}
        data-tier={tier}
        tabindex={idx > 0 ? 0 : -1}
        on:click={() => openDiff(idx)}
        on:keydown={(e) => onCellKey(e, idx)}
      >
        <div class="strip-cell-head">
          <span class="strip-cell-name" title={run.name}>{run.name}</span>
          <span class="strip-cell-pill strip-cell-pill-{tier}" aria-label="{ms} ms">{ms} ms</span>
        </div>
        <pre class="strip-cell-body" aria-label={t($locale, 'studio.dryrun.strip.bodyLabel')}>{bodyPreview(run.body)}</pre>
        <div class="strip-cell-foot">
          {#if mark === 'changed'}
            <span class="strip-cell-mark strip-cell-mark-changed" aria-label={t($locale, 'studio.dryrun.strip.changed')}
              >Δ</span>
          {:else if mark === 'same'}
            <span class="strip-cell-mark strip-cell-mark-same" aria-label={t($locale, 'studio.dryrun.strip.same')}
              >=</span>
          {/if}
          {#if run.format}
            <span class="strip-cell-format">{run.format}</span>
          {/if}
        </div>
        {#if run.failed && run.err}
          <p class="strip-cell-err" role="alert">{run.err}</p>
        {/if}
      </div>
    {/each}
  {/if}
</div>

<PayloadDiffView
  open={diffOpen}
  before={diffBefore}
  after={diffAfter}
  format={diffFormat}
  beforeLabel={diffBeforeLabel}
  afterLabel={diffAfterLabel}
  on:close={() => (diffOpen = false)}
/>

<style>
  .strip {
    display: flex;
    gap: 0.5rem;
    overflow-x: auto;
    overflow-y: hidden;
    padding-block-end: 0.25rem;
    /* The strip can grow wider than its container — scroll horizontally
       so a 20-stage pipeline still fits. */
    min-inline-size: 0;
  }
  .strip-empty {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.8125rem;
    font-style: italic;
  }
  .strip-cell {
    inline-size: 12rem;
    flex-shrink: 0;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    cursor: default;
    outline: none;
    transition: border-color 120ms, background 120ms, transform 80ms;
  }
  .strip-cell.is-clickable {
    cursor: pointer;
  }
  .strip-cell.is-clickable:hover,
  .strip-cell.is-clickable:focus-visible {
    border-color: var(--accent);
    background: var(--surface-bright);
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
    gap: 0.375rem;
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
    font-size: 0.6875rem;
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
    padding: 0.375rem 0.5rem;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.6875rem;
    line-height: 1.4;
    color: var(--text);
    max-block-size: 4.5rem;
    overflow: hidden;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .strip-cell-foot {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    font-size: 0.6875rem;
  }
  .strip-cell-mark {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-inline-size: 1.125rem;
    padding-block: 0.0625rem;
    padding-inline: 0.25rem;
    border-radius: 4px;
    font-weight: 700;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }
  .strip-cell-mark-changed {
    background: var(--info-bg);
    color: var(--info);
  }
  .strip-cell-mark-same {
    background: var(--surface-high);
    color: var(--text-tertiary);
  }
  .strip-cell-format {
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-tertiary);
    font-weight: 600;
  }
  .strip-cell-err {
    margin: 0;
    color: var(--danger);
    font-size: 0.6875rem;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    white-space: pre-wrap;
    word-break: break-word;
  }
</style>
