<!--
  PercentileBand — p50 / p95 / p99 visualisation primitive.

  Two presentations, picked by the `mode` prop:

    snapshot  — a single horizontal bar with three tick marks (p50,
                p95, p99) labelled inline. Designed to drop inline
                next to a stage name or KPI tile.

    overtime  — a stacked SVG line chart of the three percentiles
                across a time window, with a shaded band drawn
                between p50 and p99 to communicate "spread". Used
                inside the Observability page's lower panel.

  Sizing:
    `width` 0 + a parent with non-zero inline-size triggers a
    ResizeObserver-based reflow so the chart re-derives its x-scale
    to fill the container. Pass an explicit `width` to opt out.

  Reduced motion:
    Series swaps don't animate. The chart re-derives synchronously.

  Colour vocabulary:
    p50 → var(--info)     (calm baseline)
    p95 → var(--warning)  (escalation tier)
    p99 → var(--danger)   (worst-case tail)

  The component is a pure SVG primitive — no third-party libs. Same
  approach as Sparkline + Heatmap; keeps the bundle quiet.
-->
<script lang="ts">
  import { onDestroy, onMount } from 'svelte';

  export let mode: 'overtime' | 'snapshot' = 'snapshot';
  /** Overtime mode — one entry per time bucket, oldest first. */
  export let series: { p50: number; p95: number; p99: number; t?: number }[] = [];
  /** Snapshot mode — a single triple to display. */
  export let point: { p50: number; p95: number; p99: number } | null = null;
  /** Unit suffix on labels (e.g. "ms", "s"). */
  export let unit: string = 'ms';
  /** Manual max for the value axis. `0` = derive from data. */
  export let max: number = 0;
  /** Block-size of the chart in px. */
  export let height: number = 60;
  /** Inline-size of the chart. `0` = autosize via ResizeObserver. */
  export let width: number = 0;

  const PAD_X = 6;
  const PAD_Y = 6;

  // Container measure for autosize. We don't bind:clientWidth because
  // SSR + jsdom don't fire ResizeObserver; the explicit observer with
  // a fallback to a sane default keeps tests deterministic.
  let host: HTMLDivElement | null = null;
  let measured = 0;
  let ro: ResizeObserver | null = null;

  onMount(() => {
    if (typeof ResizeObserver === 'undefined' || !host) return;
    ro = new ResizeObserver((entries) => {
      for (const entry of entries) {
        const cr = entry.contentRect;
        measured = Math.max(0, Math.floor(cr.width));
      }
    });
    ro.observe(host);
    // Prime with the current width — ResizeObserver fires async,
    // first paint should not be a placeholder.
    if (host) measured = Math.max(0, Math.floor(host.getBoundingClientRect().width));
  });
  onDestroy(() => {
    if (ro) ro.disconnect();
  });

  $: effectiveWidth = width > 0 ? width : measured > 0 ? measured : 320;

  // Auto-max — pick the largest p99 across the series, or fall back
  // to the snapshot point. If everything is zero we still need a
  // positive divisor so we don't divide by 0.
  $: effectiveMax = (() => {
    if (max > 0) return max;
    let m = 0;
    if (mode === 'overtime') {
      for (const s of series) {
        if (s.p99 > m) m = s.p99;
      }
    } else if (point) {
      m = point.p99;
    }
    return m > 0 ? m : 1;
  })();

  // ── snapshot helpers ──────────────────────────────────────────
  function snapshotX(value: number): number {
    const v = Math.max(0, Math.min(value, effectiveMax));
    const inner = effectiveWidth - 2 * PAD_X;
    return PAD_X + (v / effectiveMax) * inner;
  }

  // ── overtime helpers ──────────────────────────────────────────
  function seriesX(i: number, n: number): number {
    if (n <= 1) return effectiveWidth / 2;
    return PAD_X + (i * (effectiveWidth - 2 * PAD_X)) / (n - 1);
  }
  function seriesY(v: number): number {
    const inner = height - 2 * PAD_Y;
    return height - PAD_Y - (Math.min(Math.max(v, 0), effectiveMax) / effectiveMax) * inner;
  }
  function lineFor(key: 'p50' | 'p95' | 'p99'): string {
    return series
      .map((s, i) => `${seriesX(i, series.length).toFixed(2)},${seriesY(s[key]).toFixed(2)}`)
      .join(' ');
  }
  function bandFor(): string {
    if (series.length === 0) return '';
    const upper: string[] = [];
    const lower: string[] = [];
    for (let i = 0; i < series.length; i++) {
      const x = seriesX(i, series.length);
      upper.push(`${x.toFixed(2)},${seriesY(series[i].p99).toFixed(2)}`);
      lower.push(`${x.toFixed(2)},${seriesY(series[i].p50).toFixed(2)}`);
    }
    return `${upper.join(' ')} ${lower.reverse().join(' ')}`;
  }

  $: p50Line = mode === 'overtime' ? lineFor('p50') : '';
  $: p95Line = mode === 'overtime' ? lineFor('p95') : '';
  $: p99Line = mode === 'overtime' ? lineFor('p99') : '';
  $: bandPoly = mode === 'overtime' ? bandFor() : '';

  function fmt(v: number): string {
    if (v >= 1000) return `${(v / 1000).toFixed(1)}k`;
    if (v >= 10) return v.toFixed(0);
    return v.toFixed(1);
  }
</script>

<div class="pband" bind:this={host} data-mode={mode} data-testid="percentile-band">
  {#if mode === 'snapshot'}
    {#if point}
      <svg
        class="pband-svg"
        width={effectiveWidth}
        height={height}
        viewBox="0 0 {effectiveWidth} {height}"
        role="img"
        aria-label="p50 {fmt(point.p50)} {unit}, p95 {fmt(point.p95)} {unit}, p99 {fmt(
          point.p99
        )} {unit}"
        preserveAspectRatio="none"
      >
        <!-- Rail -->
        <rect
          x={PAD_X}
          y={height / 2 - 4}
          width={Math.max(0, effectiveWidth - 2 * PAD_X)}
          height="8"
          rx="4"
          class="pband-rail"
        />
        <!-- Filled span p50 → p99 -->
        <rect
          x={snapshotX(point.p50)}
          y={height / 2 - 3}
          width={Math.max(2, snapshotX(point.p99) - snapshotX(point.p50))}
          height="6"
          rx="3"
          class="pband-span"
        />
        <!-- Tick: p50 -->
        <line
          x1={snapshotX(point.p50)}
          x2={snapshotX(point.p50)}
          y1={height / 2 - 9}
          y2={height / 2 + 9}
          class="pband-tick pband-tick-p50"
          data-tier="p50"
        />
        <!-- Tick: p95 -->
        <line
          x1={snapshotX(point.p95)}
          x2={snapshotX(point.p95)}
          y1={height / 2 - 9}
          y2={height / 2 + 9}
          class="pband-tick pband-tick-p95"
          data-tier="p95"
        />
        <!-- Tick: p99 -->
        <line
          x1={snapshotX(point.p99)}
          x2={snapshotX(point.p99)}
          y1={height / 2 - 11}
          y2={height / 2 + 11}
          class="pband-tick pband-tick-p99"
          data-tier="p99"
        />
      </svg>
      <div class="pband-legend" aria-hidden="true">
        <span class="leg leg-p50">p50 · {fmt(point.p50)}{unit}</span>
        <span class="leg leg-p95">p95 · {fmt(point.p95)}{unit}</span>
        <span class="leg leg-p99">p99 · {fmt(point.p99)}{unit}</span>
      </div>
    {:else}
      <p class="pband-empty">no data</p>
    {/if}
  {:else if series.length === 0}
    <p class="pband-empty">no data</p>
  {:else}
    <svg
      class="pband-svg"
      width={effectiveWidth}
      height={height}
      viewBox="0 0 {effectiveWidth} {height}"
      role="img"
      aria-label="p50/p95/p99 over {series.length} samples"
      preserveAspectRatio="none"
    >
      <!-- Grid: a single midline so the chart has a horizon -->
      <line
        x1={PAD_X}
        x2={effectiveWidth - PAD_X}
        y1={height / 2}
        y2={height / 2}
        class="pband-grid"
      />
      <!-- Band fill p50 → p99 -->
      <polygon points={bandPoly} class="pband-band" />
      <!-- Lines -->
      <polyline points={p50Line} class="pband-line pband-line-p50" data-tier="p50" />
      <polyline points={p95Line} class="pband-line pband-line-p95" data-tier="p95" />
      <polyline points={p99Line} class="pband-line pband-line-p99" data-tier="p99" />
      <!-- Slot for overlays (AnomalyMarker etc) — caller composes
           markers in via the named slot below; we expose the local
           xScale via slot props so callers can place markers without
           re-deriving geometry. The xScale closure already captures
           series.length, so callers get a clean (t) => x signature. -->
      <slot
        name="overlay"
        xScale={(t: number) => seriesX(t, series.length)}
        yScale={seriesY}
        {height}
      />
    </svg>
  {/if}
</div>

<style>
  .pband {
    display: block;
    inline-size: 100%;
    min-inline-size: 0;
  }
  .pband-svg {
    display: block;
    inline-size: 100%;
    block-size: auto;
  }
  .pband-rail {
    fill: var(--surface-2);
    stroke: var(--border);
    stroke-width: 1;
  }
  .pband-span {
    fill: color-mix(in srgb, var(--warning) 22%, transparent);
    stroke: none;
  }
  .pband-tick {
    stroke-width: 2.5;
    stroke-linecap: round;
  }
  .pband-tick-p50 {
    stroke: var(--info);
  }
  .pband-tick-p95 {
    stroke: var(--warning);
  }
  .pband-tick-p99 {
    stroke: var(--danger);
  }

  .pband-grid {
    stroke: var(--text-tertiary);
    stroke-width: 0.5;
    opacity: 0.35;
  }
  .pband-band {
    fill: color-mix(in srgb, var(--warning) 12%, transparent);
    stroke: none;
  }
  .pband-line {
    fill: none;
    stroke-width: 1.5;
    stroke-linejoin: round;
    stroke-linecap: round;
  }
  .pband-line-p50 {
    stroke: var(--info);
  }
  .pband-line-p95 {
    stroke: var(--warning);
  }
  .pband-line-p99 {
    stroke: var(--danger);
  }
  .pband-legend {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-block-start: 4px;
    font-variant-numeric: tabular-nums;
    font-size: 11px;
    color: var(--text-tertiary);
  }
  .leg {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .leg::before {
    content: '';
    display: inline-block;
    inline-size: 8px;
    block-size: 8px;
    border-radius: 999px;
    background: currentColor;
  }
  .leg-p50 {
    color: var(--info);
  }
  .leg-p95 {
    color: var(--warning);
  }
  .leg-p99 {
    color: var(--danger);
  }
  .pband-empty {
    margin: 0;
    color: var(--text-tertiary);
    font-size: 12px;
    font-style: italic;
  }
  @media (prefers-reduced-motion: reduce) {
    .pband-svg :global(*) {
      transition: none !important;
      animation: none !important;
    }
  }
</style>
