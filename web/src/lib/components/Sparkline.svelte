<!--
  Sparkline — compact inline-SVG trend line, no external deps.

  Used by the dashboard to show short-window throughput per pipeline.
  Colour comes from brand tokens via a CSS variable that maps off the
  `variant` prop, so dark + light themes pick up the right shade
  automatically.

  Empty data, all-zero data, and single-sample data are all handled
  gracefully (flat baseline). The SVG is marked role="img" with an
  aria-label so screen readers get a meaningful summary instead of
  silence.
-->
<script lang="ts">
  export let data: number[] = [];
  export let width = 120;
  export let height = 32;
  export let variant: 'primary' | 'secondary' | 'success' | 'warning' | 'danger' = 'secondary';
  export let label = '';

  const PAD = 2;

  $: max = data.length > 0 ? Math.max(...data) : 0;
  $: min = data.length > 0 ? Math.min(...data) : 0;
  $: range = max - min || 1;

  function xAt(i: number, n: number): number {
    if (n <= 1) return width / 2;
    return PAD + (i * (width - 2 * PAD)) / (n - 1);
  }
  function yAt(v: number): number {
    if (data.length === 0) return height / 2;
    return height - PAD - ((v - min) / range) * (height - 2 * PAD);
  }

  $: points = data.map((v, i) => `${xAt(i, data.length).toFixed(2)},${yAt(v).toFixed(2)}`).join(' ');
  $: lastX = data.length > 0 ? xAt(data.length - 1, data.length) : 0;
  $: lastY = data.length > 0 ? yAt(data[data.length - 1]) : 0;
  $: last = data.length > 0 ? data[data.length - 1] : 0;
  $: ariaLabel =
    label ||
    (data.length === 0
      ? 'no data'
      : `trend ${data.length} samples, current ${last}, min ${min}, max ${max}`);
</script>

<svg
  class="sparkline sparkline-{variant}"
  {width}
  {height}
  viewBox="0 0 {width} {height}"
  role="img"
  aria-label={ariaLabel}
  preserveAspectRatio="none"
>
  {#if data.length >= 2}
    <polyline points={points} fill="none" stroke="currentColor" stroke-width="1.5" />
    <circle cx={lastX} cy={lastY} r="2" fill="currentColor" />
  {:else if data.length === 1}
    <line
      x1={PAD}
      x2={width - PAD}
      y1={height / 2}
      y2={height / 2}
      stroke="currentColor"
      stroke-width="1"
      stroke-dasharray="2 2"
      opacity="0.6"
    />
  {:else}
    <line
      x1={PAD}
      x2={width - PAD}
      y1={height / 2}
      y2={height / 2}
      stroke="currentColor"
      stroke-width="1"
      stroke-dasharray="2 2"
      opacity="0.35"
    />
  {/if}
</svg>

<style>
  .sparkline {
    display: inline-block;
    vertical-align: middle;
  }
  .sparkline-primary {
    color: var(--primary);
  }
  .sparkline-secondary {
    color: var(--secondary);
  }
  /* :global needed because data-theme lives on <html>. */
  :global([data-theme='light']) .sparkline-secondary {
    color: var(--primary);
  }
  .sparkline-success {
    color: var(--success-solid);
  }
  .sparkline-warning {
    color: var(--warning);
  }
  .sparkline-danger {
    color: var(--danger);
  }
</style>
