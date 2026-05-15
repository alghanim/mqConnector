<!--
  Skeleton — Brand Guide §5.18.

  Animated shimmer placeholder for async content. Token-driven base +
  highlight, 1.5s ease-in-out infinite per spec. The animation is
  disabled automatically under `prefers-reduced-motion` via the global
  override in app.css.

  Props
    width    — any CSS length (`100%`, `12rem`, `200px`); default 100%.
    height   — any CSS length; default `1em` for text rows.
    radius   — `text` (4 px), `pill` (999 px), or any CSS length.
                 Defaults to brand-correct: 12 px for interactive-sized
                 blocks (avatar tiles), 4 px for text rows. Picked
                 dynamically based on `height` so the caller doesn't
                 have to think about it.
    rows     — render N stacked text rows with 8 px gap. Width of the
                 final row is auto-narrowed for a more natural shape.
-->
<script lang="ts">
  export let width = '100%';
  export let height = '1em';
  export let radius: string | undefined = undefined;
  export let rows = 1;

  // Heuristic: small (≤1.25em) heights are text rows → 4 px radius
  // (matches caption typography). Bigger blocks default to 12 px to
  // respect the spec's "no off-spec radii" rule.
  $: resolvedRadius = (() => {
    if (radius === 'text') return '4px';
    if (radius === 'pill') return '999px';
    if (radius) return radius;
    const looksLikeText = /(em|rem)$/.test(height) && parseFloat(height) <= 1.25;
    return looksLikeText ? '4px' : '12px';
  })();
</script>

{#if rows > 1}
  <div class="skeleton-stack" role="status" aria-live="polite" aria-label="Loading">
    {#each Array(rows) as _, i (i)}
      <span
        class="skeleton"
        style:width={i === rows - 1 ? '70%' : width}
        style:height
        style:border-radius={resolvedRadius}
        aria-hidden="true"
      ></span>
    {/each}
  </div>
{:else}
  <span
    class="skeleton"
    style:width
    style:height
    style:border-radius={resolvedRadius}
    role="status"
    aria-live="polite"
    aria-label="Loading"
  ></span>
{/if}

<style>
  .skeleton-stack {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .skeleton {
    display: block;
    background: linear-gradient(
      90deg,
      var(--shimmer-base) 0%,
      var(--shimmer-highlight) 50%,
      var(--shimmer-base) 100%
    );
    background-size: 200% 100%;
    animation: shimmer 1.5s ease-in-out infinite;
  }
  @keyframes shimmer {
    0%   { background-position: 200% 0; }
    100% { background-position: -200% 0; }
  }
  /* RTL: reverse the shimmer direction so the highlight visually
     travels start→end for both reading directions. */
  :global([dir='rtl']) .skeleton {
    animation-direction: reverse;
  }
</style>
