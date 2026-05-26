<!--
  Heatmap — a hand-rolled 7-day × 24-hour calendar heatmap.

  Used by the rebuilt /dlq Cluster console to visualise per-cluster
  occurrence density over the last week. 168 cells = 7 rows × 24 cols,
  one cell per hour. Each cell is a <rect> with a deterministic fill
  tier picked from the count quantile.

  Design rationale
  ────────────────
  * Pure SVG, no third-party. The whole component is one <svg>; we don't
    use D3 because the spacing is hand-tuned and the dataset is fixed-
    size. Keeps the bundle quiet.
  * Five fill tiers (none → very-low → low → mid → high → max) match the
    Sparkline / TopologyGraph tier vocabulary used elsewhere — gives the
    surface a coherent visual language.
  * Fills go from `--surface-2` (empty) through `--warning` (mid) to
    `--danger` (max). The progression reads as "calm → concerning →
    on-fire" without needing a legend.
  * RTL: the x-axis is TIME (hours of day); time still reads left-to-
    right under RTL because that's how the rest of the app draws
    timelines. Only the optional `label` is logical-aligned.
  * Reduced motion: there's no animation — heatmaps are historical, no
    fade-in. Skip the `transition` rule under reduce-motion just so the
    initial render lands without any hover transition either.
-->
<script lang="ts">
  export let buckets: number[] = [];
  /** Optional clamp ceiling. `0` means "compute from buckets". */
  export let max = 0;
  /** Caption rendered above the grid. Empty → no caption. */
  export let label = '';
  /** Side length per cell, px. */
  export let cellSize = 12;
  /** Inter-cell spacing, px. */
  export let gap = 2;

  // Grid dimensions are fixed: 7 days × 24 hours = 168 cells. We
  // intentionally do NOT make this parametric — the component is sold
  // as a "weekly hourly heatmap"; if a caller wants something else they
  // should reach for a different primitive (or extend this with a
  // second prop pair when there's a real second use-case).
  const ROWS = 7;
  const COLS = 24;
  const TOTAL = ROWS * COLS;

  // Padded buckets — defensive against undersized arrays. Bigger arrays
  // get clipped to 168; smaller ones get zero-padded so we still render
  // the full grid (otherwise the layout collapses).
  $: padded = (() => {
    if (buckets.length === TOTAL) return buckets;
    const out = new Array<number>(TOTAL).fill(0);
    const n = Math.min(buckets.length, TOTAL);
    for (let i = 0; i < n; i++) out[i] = buckets[i] ?? 0;
    return out;
  })();

  // Effective max — caller-supplied wins; otherwise compute from data.
  // If everything is zero we still want a valid divisor so the tier
  // calc doesn't divide-by-zero; fall back to 1.
  $: effectiveMax = (() => {
    if (max > 0) return max;
    let m = 0;
    for (const v of padded) if (v > m) m = v;
    return m > 0 ? m : 1;
  })();

  // 5-tier classifier. Tier 0 is "no events"; tiers 1-4 are quintiles
  // of the [0, effectiveMax] range. The quintile thresholds are chosen
  // so the lowest non-zero bucket always renders at tier 1 (visible
  // signal) rather than blending into the background. Without that the
  // common "1 failure on Tuesday at 03:00" case is invisible.
  function tierFor(v: number, m: number): 0 | 1 | 2 | 3 | 4 {
    if (v <= 0) return 0;
    const ratio = v / m;
    if (ratio <= 0.25) return 1;
    if (ratio <= 0.5) return 2;
    if (ratio <= 0.75) return 3;
    return 4;
  }

  // Pre-compute the cell layout so the template stays declarative. Each
  // entry carries the absolute (x,y) + the tier so SVG props are flat.
  // Indices are (day * 24) + hour, oldest day at the top.
  $: cells = padded.map((value, idx) => {
    const row = Math.floor(idx / COLS);
    const col = idx % COLS;
    return {
      idx,
      row,
      col,
      x: col * (cellSize + gap),
      y: row * (cellSize + gap),
      value,
      tier: tierFor(value, effectiveMax)
    };
  });

  // Total inline-size / block-size of the rendered SVG. We need both
  // dimensions explicitly so the SVG doesn't inflate to fill its parent
  // — a heatmap in a sidebar should reserve a predictable footprint.
  $: width = COLS * cellSize + (COLS - 1) * gap;
  $: height = ROWS * cellSize + (ROWS - 1) * gap;

  // Day labels for the y-axis. Names stay in English — RTL flips the
  // overall component layout but per-cell labels in tooltips are
  // produced in the user's locale via the formatter elsewhere; here
  // we want stable short codes so the visual reads cleanly even when
  // the locale string is verbose.
  const dayShort = ['D-6', 'D-5', 'D-4', 'D-3', 'D-2', 'D-1', 'D'];

  // Title text for hover tooltips. Folds day + hour + count into one
  // string — screen readers + browsers both surface this via the SVG
  // <title> tag pattern.
  function titleFor(row: number, col: number, value: number): string {
    const dayLabel = row === ROWS - 1 ? 'today' : `${ROWS - 1 - row}d ago`;
    const hourLabel = `${String(col).padStart(2, '0')}:00`;
    return `${dayLabel} · ${hourLabel} · ${value} ${value === 1 ? 'event' : 'events'}`;
  }
</script>

<div class="heatmap" role="group" aria-label={label || 'Heatmap of cluster occurrences over the last 7 days'}>
  {#if label}
    <p class="heatmap-label">{label}</p>
  {/if}
  <div class="heatmap-grid-wrap">
    <ul class="heatmap-day-labels" aria-hidden="true" style="height: {height}px">
      {#each dayShort as d (d)}
        <li class="heatmap-day-label">{d}</li>
      {/each}
    </ul>
    <svg
      class="heatmap-svg"
      data-testid="heatmap-svg"
      viewBox="0 0 {width} {height}"
      width={width}
      height={height}
      role="img"
      aria-label={label || 'Heatmap grid'}
    >
      {#each cells as cell (cell.idx)}
        <rect
          class="heatmap-cell"
          data-tier={cell.tier}
          data-value={cell.value}
          x={cell.x}
          y={cell.y}
          width={cellSize}
          height={cellSize}
          rx={2}
          ry={2}
        >
          <title>{titleFor(cell.row, cell.col, cell.value)}</title>
        </rect>
      {/each}
    </svg>
  </div>
  <!-- Hour ruler — keeps left-to-right in RTL (time stays LTR). -->
  <div class="heatmap-hour-ruler" aria-hidden="true">
    <span>00</span>
    <span>06</span>
    <span>12</span>
    <span>18</span>
    <span>23</span>
  </div>
</div>

<style>
  .heatmap {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    color: var(--text);
  }
  .heatmap-label {
    margin: 0;
    font-size: 0.6875rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-muted);
  }
  .heatmap-grid-wrap {
    display: flex;
    align-items: flex-start;
    gap: 0.375rem;
  }
  /* Day labels — a tiny LTR-style stacked list aligned to the grid rows.
     Under RTL we keep numeric ordering but the label is on the leading
     edge naturally because we sit in a flex row that flips. */
  .heatmap-day-labels {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    font-size: 0.625rem;
    color: var(--text-tertiary);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }
  .heatmap-day-label {
    line-height: 1;
  }
  .heatmap-svg {
    display: block;
    /* Time always flows LTR regardless of document direction. */
    direction: ltr;
  }
  .heatmap-cell {
    fill: var(--surface-2);
    stroke: var(--border);
    stroke-width: 0.5;
    transition: fill 120ms ease, transform 80ms ease;
  }
  .heatmap-cell[data-tier='1'] {
    fill: color-mix(in srgb, var(--warning) 22%, var(--surface-2));
    stroke: color-mix(in srgb, var(--warning) 30%, transparent);
  }
  .heatmap-cell[data-tier='2'] {
    fill: color-mix(in srgb, var(--warning) 50%, var(--surface-2));
    stroke: color-mix(in srgb, var(--warning) 45%, transparent);
  }
  .heatmap-cell[data-tier='3'] {
    fill: color-mix(in srgb, var(--danger) 55%, var(--warning));
    stroke: color-mix(in srgb, var(--danger) 40%, transparent);
  }
  .heatmap-cell[data-tier='4'] {
    fill: var(--danger);
    stroke: color-mix(in srgb, var(--danger) 70%, transparent);
  }
  .heatmap-cell:hover {
    transform: scale(1.08);
    transform-box: fill-box;
    transform-origin: center;
    stroke: var(--accent);
    stroke-width: 1;
  }
  .heatmap-hour-ruler {
    display: flex;
    justify-content: space-between;
    /* The ruler aligns under the SVG, not the day labels — push it by
       the same width the day-label column takes (≈22px + gap). */
    margin-inline-start: calc(22px + 0.375rem);
    font-size: 0.625rem;
    color: var(--text-tertiary);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    /* Hour ruler stays LTR even under RTL. */
    direction: ltr;
  }
  @media (prefers-reduced-motion: reduce) {
    .heatmap-cell {
      transition: none;
    }
    .heatmap-cell:hover {
      transform: none;
    }
  }
</style>
