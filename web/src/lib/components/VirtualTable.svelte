<!--
  VirtualTable — fixed-row-height windowed renderer for large tabular
  datasets.

  Why windowing matters here:
    The DLQ console can be set to per_page=1000 during incident triage so
    the operator can scroll a wide error backlog without clicking through
    pagination. Rendering 1000 DOM rows costs tens of MB of layout state
    and tanks interaction latency on mid-range hardware. Windowing keeps
    only ~30 visible rows + a buffer in the DOM at any time, so memory +
    paint stay constant regardless of dataset size.

  Why not just CSS content-visibility?
    `content-visibility: auto` would skip painting off-screen rows, but
    the rows still hit layout (which still allocates DOM nodes + style
    state per row). Real windowing avoids both.

  Contract:
    - Caller passes `items` (the array) and `rowHeight` (px, MUST match
      the actual rendered height — visual jank otherwise).
    - Caller passes a slot `row` that receives each item; the slot is
      rendered into a positioned <div role="row"> with `aria-rowindex`
      for screen-reader correctness.
    - Caller passes a slot `header` rendered above the scroll viewport;
      a sticky <thead>-style strip the user sees while scrolling.
    - `viewportHeight` controls the visible window. The remaining
      height of the page (or a wrapping flex container) is the natural
      choice; pass via prop so the caller can react to `100vh -
      otherChrome`.

  Accessibility:
    - role="table" / role="rowgroup" / role="row" wired explicitly
      because we lose the native <table> for windowing.
    - aria-rowcount + aria-rowindex give screen readers position-in-set
      information that would otherwise be invisible.

  Limitations (deliberate, documented):
    - Fixed rowHeight only. Variable-height rows would require a
      measure pass; out of scope for this use case (DLQ rows are
      always 1 line of metadata + 1 line of reason).
-->
<script lang="ts" generics="T">
  import { onMount } from 'svelte';

  export let items: T[] = [];
  /** Pixels per row. Must equal the rendered height of the slotted row. */
  export let rowHeight = 56;
  /** Pixels of buffer above + below the visible window. Smooths fast scrolls. */
  export let overscan = 6;
  /** Pixel height of the scroll viewport. */
  export let viewportHeight = 480;
  /** Stable identity for each row — keys the rendered slot for fast diffs. */
  export let keyFn: (item: T, index: number) => string | number = (_i, idx) => idx;

  let scrollTop = 0;
  let viewportEl: HTMLDivElement;
  let resizeObserver: ResizeObserver | null = null;
  let measuredHeight = viewportHeight;

  $: effectiveHeight = measuredHeight || viewportHeight;
  $: total = items.length;
  $: contentHeight = total * rowHeight;
  $: firstVisible = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  $: visibleCount = Math.ceil(effectiveHeight / rowHeight) + overscan * 2;
  $: lastVisible = Math.min(total, firstVisible + visibleCount);
  $: windowItems = items.slice(firstVisible, lastVisible);
  $: offsetY = firstVisible * rowHeight;

  function onScroll(e: Event) {
    scrollTop = (e.currentTarget as HTMLDivElement).scrollTop;
  }

  onMount(() => {
    if (!viewportEl || typeof ResizeObserver === 'undefined') return;
    resizeObserver = new ResizeObserver((entries) => {
      for (const entry of entries) {
        measuredHeight = entry.contentRect.height;
      }
    });
    resizeObserver.observe(viewportEl);
    return () => resizeObserver?.disconnect();
  });
</script>

<div class="vt" role="table" aria-rowcount={total}>
  <!--
    Sticky header strip — caller renders headings + filter chips here.
    We deliberately don't use a real <thead>; the windowed body uses
    role="rowgroup" so a screen reader still pairs them correctly.
  -->
  <div class="vt-header" role="rowgroup">
    <slot name="header" />
  </div>

  <div
    bind:this={viewportEl}
    class="vt-viewport"
    style:height="{viewportHeight}px"
    on:scroll={onScroll}
    role="rowgroup"
  >
    <div class="vt-spacer" style:height="{contentHeight}px">
      <div class="vt-window" style:transform="translateY({offsetY}px)">
        {#each windowItems as item, i (keyFn(item, firstVisible + i))}
          <div
            class="vt-row"
            role="row"
            aria-rowindex={firstVisible + i + 2}
            style:height="{rowHeight}px"
          >
            <slot name="row" {item} index={firstVisible + i} />
          </div>
        {/each}
      </div>
    </div>
  </div>
</div>

<style>
  .vt {
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  .vt-header {
    position: sticky;
    inset-block-start: 0;
    z-index: 2;
    background: var(--surface);
    border-block-end: 1px solid var(--divider);
  }
  .vt-viewport {
    overflow-y: auto;
    overflow-x: hidden;
    position: relative;
  }
  .vt-spacer {
    position: relative;
    inline-size: 100%;
  }
  .vt-window {
    position: absolute;
    inset-inline: 0;
    inset-block-start: 0;
    will-change: transform;
  }
  .vt-row {
    display: block;
    border-block-end: 1px solid var(--divider-subtle);
  }
  .vt-row:last-child {
    border-block-end: none;
  }
</style>
