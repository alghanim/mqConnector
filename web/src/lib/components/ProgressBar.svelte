<!--
  ProgressBar — Design system §5.17.

  Determinate when `value` is provided (0–100). Indeterminate sliding
  bar when `value` is `null` / undefined, useful for "we're working on
  it but can't measure" cases (preview-running, save-in-flight).

  Active track = `--progress-fill` (Copper on dark, Dark Gold on light).
  Inactive track = `--progress-track`. Reduces motion through the
  global `prefers-reduced-motion` override.

  Props
    value      — 0..100 for determinate; null for indeterminate
    label      — optional aria-label for screen readers
-->
<script lang="ts">
  export let value: number | null = null;
  export let label = 'Loading';

  $: pct = value == null ? null : Math.max(0, Math.min(100, value));
</script>

<div
  class="progress"
  role="progressbar"
  aria-label={label}
  aria-valuemin="0"
  aria-valuemax="100"
  aria-valuenow={pct ?? undefined}
>
  {#if pct == null}
    <span class="progress-indeterminate"></span>
  {:else}
    <span class="progress-fill" style="inline-size: {pct}%"></span>
  {/if}
</div>

<style>
  .progress {
    position: relative;
    inline-size: 100%;
    block-size: 4px;
    background: var(--progress-track);
    border-radius: 999px;
    overflow: hidden;
  }
  .progress-fill {
    display: block;
    block-size: 100%;
    background: var(--progress-fill);
    border-radius: inherit;
    transition: inline-size 200ms;
  }
  .progress-indeterminate {
    position: absolute;
    inset-block: 0;
    inline-size: 40%;
    background: var(--progress-fill);
    border-radius: inherit;
    animation: indeterminate 1.5s ease-in-out infinite;
  }
  @keyframes indeterminate {
    0%   { inset-inline-start: -40%; }
    60%  { inset-inline-start: 100%; }
    100% { inset-inline-start: 100%; }
  }
</style>
