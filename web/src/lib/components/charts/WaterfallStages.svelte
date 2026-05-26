<!--
  WaterfallStages — per-stage latency waterfall.

  Each row carries one stage:
    [ Stage name ] [────────── p99 bar ──────────]  p50/p95/p99 ms

  The bar length is proportional to that stage's p99 share of the
  pipeline's total p99. p50 + p95 are layered as ticks on the bar so
  the operator sees the spread inside that stage too.

  Behaviour:
    • The stage with the highest p99 — "dominant" — gets an outline
      ring + a "dominant" badge so the eye snaps to it first.
    • Each row is a button: clicking emits a `select` event with
      `{ stageName }`. The parent typically uses this to switch the
      open tab / drilldown in a deeper panel.

  Wire shape comes from the latency Explanation's first section
  (`sections[0].kind === 'stages'`). The data block carries
  `{ stages, total_p99 }` where each stage is `{name, p50_ms, p95_ms,
  p99_ms, count, sum_ms}`. We compute `share_of_p99` client-side when
  the backend hasn't precomputed it.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';

  export let stages: {
    name: string;
    p50_ms: number;
    p95_ms: number;
    p99_ms: number;
    share_of_p99?: number;
  }[] = [];
  export let total_p99_ms: number = 0;
  /** Outline the row with the largest p99. */
  export let highlightDominantStage: boolean = true;

  const dispatch = createEventDispatcher<{ select: { stageName: string } }>();

  // Effective scale denominator. Prefer caller-supplied total; fall
  // back to max(stage.p99) so the bar still fills meaningfully when
  // total_p99_ms is missing or zero. A non-zero floor keeps the
  // div-by-zero away.
  $: scaleMax = (() => {
    if (total_p99_ms > 0) return total_p99_ms;
    let m = 0;
    for (const s of stages) if (s.p99_ms > m) m = s.p99_ms;
    return m > 0 ? m : 1;
  })();

  $: dominantName = (() => {
    if (!highlightDominantStage || stages.length === 0) return '';
    let best = stages[0];
    for (const s of stages) if (s.p99_ms > best.p99_ms) best = s;
    return best.name;
  })();

  function shareOf(s: { p99_ms: number; share_of_p99?: number }): number {
    if (typeof s.share_of_p99 === 'number' && s.share_of_p99 > 0) return s.share_of_p99;
    return scaleMax > 0 ? s.p99_ms / scaleMax : 0;
  }

  function pct(v: number): string {
    return Math.max(0, Math.min(100, v * 100)).toFixed(0);
  }

  function fmt(v: number): string {
    if (v >= 1000) return `${(v / 1000).toFixed(2)}s`;
    if (v >= 100) return `${v.toFixed(0)}ms`;
    if (v >= 10) return `${v.toFixed(1)}ms`;
    return `${v.toFixed(2)}ms`;
  }

  function onSelect(name: string) {
    dispatch('select', { stageName: name });
  }

  function onKey(e: KeyboardEvent, name: string) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onSelect(name);
    }
  }
</script>

<div class="waterfall" data-testid="waterfall-stages">
  {#if stages.length === 0}
    <p class="wf-empty">no stages</p>
  {:else}
    <div class="wf-rows">
      {#each stages as s (s.name)}
        {@const isDominant = highlightDominantStage && s.name === dominantName}
        {@const share = shareOf(s)}
        {@const p50Pct = scaleMax > 0 ? (s.p50_ms / scaleMax) * 100 : 0}
        {@const p95Pct = scaleMax > 0 ? (s.p95_ms / scaleMax) * 100 : 0}
        <button
          type="button"
          class="wf-row"
          class:wf-row-dominant={isDominant}
          data-stage={s.name}
          data-dominant={isDominant ? 'true' : 'false'}
          on:click={() => onSelect(s.name)}
          on:keydown={(e) => onKey(e, s.name)}
          aria-label="Stage {s.name}, p99 {fmt(s.p99_ms)}, {pct(share)}% of total"
        >
          <span class="wf-name">
            <span class="wf-name-text">{s.name}</span>
            {#if isDominant}
              <span class="wf-dom-badge" aria-label="dominant stage">dominant</span>
            {/if}
          </span>
          <span class="wf-bar-wrap">
            <span
              class="wf-bar"
              class:wf-bar-dominant={isDominant}
              style="inline-size: {pct(share)}%"
            ></span>
            {#if s.p50_ms > 0 && p50Pct > 0.5}
              <span class="wf-tick wf-tick-p50" style="inset-inline-start: {p50Pct}%" aria-hidden="true"></span>
            {/if}
            {#if s.p95_ms > 0 && p95Pct > 0.5}
              <span class="wf-tick wf-tick-p95" style="inset-inline-start: {p95Pct}%" aria-hidden="true"></span>
            {/if}
          </span>
          <span class="wf-vals">
            <span class="wf-val wf-val-p50" title="p50">{fmt(s.p50_ms)}</span>
            <span class="wf-val wf-val-p95" title="p95">{fmt(s.p95_ms)}</span>
            <span class="wf-val wf-val-p99" title="p99">{fmt(s.p99_ms)}</span>
          </span>
        </button>
      {/each}
    </div>
    {#if total_p99_ms > 0}
      <p class="wf-total">
        total p99 · <span class="wf-total-val">{fmt(total_p99_ms)}</span>
      </p>
    {/if}
  {/if}
</div>

<style>
  .waterfall {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .wf-rows {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .wf-row {
    display: grid;
    grid-template-columns: minmax(80px, 0.8fr) minmax(0, 2.2fr) auto;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    text-align: start;
    font: inherit;
    cursor: pointer;
    transition: background-color 120ms, border-color 120ms;
  }
  .wf-row:hover,
  .wf-row:focus-visible {
    background: var(--surface);
    border-color: var(--border-strong);
    outline: none;
  }
  .wf-row:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 1px;
  }
  .wf-row-dominant {
    outline: 2px solid var(--primary);
    outline-offset: -2px;
  }
  .wf-name {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-inline-size: 0;
  }
  .wf-name-text {
    font-weight: 600;
    font-size: 12px;
    color: var(--text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .wf-dom-badge {
    display: inline-flex;
    align-items: center;
    padding-inline: 6px;
    padding-block: 1px;
    border-radius: 4px;
    background: color-mix(in srgb, var(--primary) 18%, transparent);
    color: var(--primary);
    font-size: 9px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .wf-bar-wrap {
    position: relative;
    block-size: 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
    min-inline-size: 0;
    overflow: visible;
  }
  .wf-bar {
    display: block;
    block-size: 100%;
    background: linear-gradient(
      90deg,
      color-mix(in srgb, var(--info) 70%, transparent),
      color-mix(in srgb, var(--warning) 70%, transparent),
      color-mix(in srgb, var(--danger) 80%, transparent)
    );
    border-radius: 3px;
    transition: inline-size 200ms ease-out;
  }
  :global([dir='rtl']) .wf-bar {
    background: linear-gradient(
      -90deg,
      color-mix(in srgb, var(--info) 70%, transparent),
      color-mix(in srgb, var(--warning) 70%, transparent),
      color-mix(in srgb, var(--danger) 80%, transparent)
    );
  }
  .wf-bar-dominant {
    box-shadow: inset 0 0 0 1px var(--primary);
  }
  .wf-tick {
    position: absolute;
    top: -2px;
    bottom: -2px;
    inline-size: 2px;
    border-radius: 1px;
    transform: translateX(-1px);
    pointer-events: none;
  }
  :global([dir='rtl']) .wf-tick {
    transform: translateX(1px);
  }
  .wf-tick-p50 {
    background: var(--info);
  }
  .wf-tick-p95 {
    background: var(--warning);
  }
  .wf-vals {
    display: inline-flex;
    gap: 6px;
    font-variant-numeric: tabular-nums;
    font-size: 11px;
    color: var(--text-muted);
    white-space: nowrap;
  }
  .wf-val {
    padding-block: 1px;
    padding-inline: 4px;
    border-radius: 4px;
    background: var(--surface);
    border: 1px solid var(--border);
  }
  .wf-val-p50 {
    color: var(--info);
  }
  .wf-val-p95 {
    color: var(--warning);
  }
  .wf-val-p99 {
    color: var(--danger);
    font-weight: 600;
  }
  .wf-total {
    margin: 0;
    color: var(--text-tertiary);
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 600;
  }
  .wf-total-val {
    color: var(--text);
    font-variant-numeric: tabular-nums;
    text-transform: none;
    letter-spacing: 0;
    margin-inline-start: 4px;
  }
  .wf-empty {
    color: var(--text-tertiary);
    font-size: 12px;
    font-style: italic;
    margin: 0;
  }

  @media (max-width: 700px) {
    .wf-row {
      grid-template-columns: minmax(60px, 1fr) minmax(0, 1.5fr) auto;
      gap: 6px;
      padding: 6px 8px;
    }
    .wf-vals {
      font-size: 10px;
    }
  }
</style>
