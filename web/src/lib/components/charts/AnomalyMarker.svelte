<!--
  AnomalyMarker — inline overlay marker for time-series charts.

  Designed to be composed INSIDE another SVG primitive (typically
  PercentileBand in overtime mode), positioning a small triangle at
  `xScale(t), y` for each marker, with severity-coloured fill and a
  hover-revealed <title> tooltip.

  The caller owns the x-scale: it knows where time bucket `t` maps in
  its own viewport, so we don't try to derive that here. Just pass a
  function.

  Visuals:
    info     → var(--info)
    warning  → var(--warning)
    critical → var(--danger)

  The component renders an SVG `<g>` group so the host chart can drop
  it inside its own `<svg>` without doubling up.
-->
<script lang="ts">
  export let markers: { t: number; label: string; severity: 'info' | 'warning' | 'critical' }[] = [];
  /** Caller-supplied function: maps time bucket `t` to SVG x. */
  export let xScale: (t: number) => number;
  /** Absolute SVG y for the marker bar (typically near the top edge). */
  export let y: number = 8;
  /** Marker triangle height in px. */
  export let size: number = 7;

  function colourFor(sev: 'info' | 'warning' | 'critical'): string {
    if (sev === 'critical') return 'var(--danger)';
    if (sev === 'warning') return 'var(--warning)';
    return 'var(--info)';
  }
</script>

<g class="anomaly-overlay" data-testid="anomaly-marker">
  {#each markers as m, i (i)}
    {@const x = xScale(m.t)}
    {@const fill = colourFor(m.severity)}
    <g
      class="anomaly-mark"
      data-severity={m.severity}
      transform="translate({x},{y})"
    >
      <title>{m.label}</title>
      <!-- Downward triangle, point on the chart at (x, y+size).
           Stroke + fill share the severity colour so the marker reads
           cleanly on either light or dark theme. -->
      <polygon
        points="{-size},0 {size},0 0,{size}"
        fill={fill}
        stroke={fill}
        stroke-width="1"
        stroke-linejoin="round"
      />
    </g>
  {/each}
</g>

<style>
  .anomaly-mark {
    cursor: help;
  }
  /* The host SVG carries any animation token — markers themselves stay
     static. */
</style>
