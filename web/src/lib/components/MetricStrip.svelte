<!--
  MetricStrip — a dense, horizontal band of KPIs. Replaces the four
  oversized cards on the overview with a tight band that fits 5-7
  signals on one row at 1680 px and degrades gracefully on narrow
  viewports.

  Each metric carries:
    label    — uppercase eyebrow (11 sp)
    value    — large tabular number (24 sp)
    unit     — optional inline unit (12 sp, muted)
    delta    — signed string e.g. "+12%" — auto-tones up/down
    sub      — optional one-line context under the value
    tone     — recolours the value: success | warning | danger | accent
    href     — turns the whole tile into a navigable cell
    spark    — optional Sparkline data; rendered behind/under the value

  Visual spec:
    - Single-row strip on desktop with internal hairline dividers
      (var(--divider)) — feels like a Bloomberg tape, not a grid of
      cards.
    - Each tile keeps padding-inline 14 / padding-block 10 so the band
      is ~64 px tall regardless of count.
    - Hover on linkable tiles raises bg to --card-hover-bg and shows
      a chevron at the inline-end edge.
    - Brand-strip across the top edge (3 px gold gradient) is optional
      via `strip` prop — keep it for the top-of-page strip, off for
      sub-sections.
-->
<script lang="ts">
  import { ArrowUpRight, ArrowDownRight, Minus } from 'lucide-svelte';
  import Sparkline from './Sparkline.svelte';

  export let strip = false;

  type Metric = {
    label: string;
    value: string | number;
    unit?: string;
    delta?: string;
    deltaTone?: 'success' | 'danger' | 'neutral';
    sub?: string;
    tone?: 'default' | 'success' | 'warning' | 'danger' | 'accent';
    href?: string;
    spark?: number[];
    sparkTone?: 'primary' | 'secondary' | 'success' | 'warning' | 'danger';
  };

  export let metrics: Metric[] = [];
</script>

<section class="strip" class:strip-brand={strip} aria-label="key metrics">
  {#each metrics as m, i (m.label + i)}
    {#if m.href}
      <a class="m-tile m-link" href={m.href}>
        <span class="m-label">{m.label}</span>
        <span class="m-value-row">
          <span class="m-value" data-tone={m.tone || 'default'}>{m.value}</span>
          {#if m.unit}<span class="m-unit">{m.unit}</span>{/if}
        </span>
        {#if m.delta}
          <span class="m-delta" data-tone={m.deltaTone || 'neutral'}>
            {#if m.deltaTone === 'success'}<ArrowUpRight size={11} aria-hidden="true" />
            {:else if m.deltaTone === 'danger'}<ArrowDownRight size={11} aria-hidden="true" />
            {:else}<Minus size={11} aria-hidden="true" />{/if}
            {m.delta}
          </span>
        {:else if m.sub}
          <span class="m-sub">{m.sub}</span>
        {/if}
        {#if m.spark && m.spark.length > 0}
          <span class="m-spark"><Sparkline data={m.spark} variant={m.sparkTone || 'secondary'} width={88} height={22} /></span>
        {/if}
      </a>
    {:else}
      <div class="m-tile">
        <span class="m-label">{m.label}</span>
        <span class="m-value-row">
          <span class="m-value" data-tone={m.tone || 'default'}>{m.value}</span>
          {#if m.unit}<span class="m-unit">{m.unit}</span>{/if}
        </span>
        {#if m.delta}
          <span class="m-delta" data-tone={m.deltaTone || 'neutral'}>
            {#if m.deltaTone === 'success'}<ArrowUpRight size={11} aria-hidden="true" />
            {:else if m.deltaTone === 'danger'}<ArrowDownRight size={11} aria-hidden="true" />
            {:else}<Minus size={11} aria-hidden="true" />{/if}
            {m.delta}
          </span>
        {:else if m.sub}
          <span class="m-sub">{m.sub}</span>
        {/if}
        {#if m.spark && m.spark.length > 0}
          <span class="m-spark"><Sparkline data={m.spark} variant={m.sparkTone || 'secondary'} width={88} height={22} /></span>
        {/if}
      </div>
    {/if}
  {/each}
</section>

<style>
  .strip {
    position: relative;
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
    background: var(--card-bg);
    border: 1px solid var(--card-border);
    border-radius: 16px;
    overflow: hidden;
    box-shadow: var(--card-shadow);
  }
  .strip-brand::before {
    content: '';
    position: absolute;
    inset-block-start: 0;
    inset-inline: 0;
    block-size: 3px;
    background: var(--brand-gradient);
    z-index: 1;
  }

  .m-tile {
    position: relative;
    display: grid;
    grid-template-columns: 1fr;
    gap: 4px;
    padding: 14px 16px 12px;
    border-inline-start: 1px solid var(--divider);
    color: var(--text);
    text-decoration: none;
    transition: background-color 150ms;
    min-block-size: 84px;
  }
  .m-tile:first-child {
    border-inline-start: 0;
  }
  .m-link:hover {
    background: var(--card-hover-bg);
  }
  .m-link::after {
    content: '↗';
    position: absolute;
    inset-block-start: 10px;
    inset-inline-end: 10px;
    color: var(--text-tertiary);
    font-size: 11px;
    opacity: 0;
    transition: opacity 150ms;
  }
  .m-link:hover::after {
    opacity: 1;
  }

  .m-label {
    color: var(--text-tertiary);
    font-size: 10.5px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    line-height: 1.2;
  }
  .m-value-row {
    display: inline-flex;
    align-items: baseline;
    gap: 4px;
  }
  .m-value {
    color: var(--text);
    font-size: 22px;
    font-weight: 700;
    letter-spacing: -0.01em;
    line-height: 1.1;
    font-variant-numeric: tabular-nums;
  }
  .m-value[data-tone='success'] { color: var(--success); }
  .m-value[data-tone='warning'] { color: var(--warning); }
  .m-value[data-tone='danger']  { color: var(--danger); }
  .m-value[data-tone='accent']  { color: var(--accent); }

  .m-unit {
    color: var(--text-muted);
    font-size: 11px;
    font-weight: 500;
    letter-spacing: 0.01em;
  }
  .m-delta {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    font-size: 11px;
    font-weight: 600;
    line-height: 1.2;
    font-variant-numeric: tabular-nums;
  }
  .m-delta[data-tone='success'] { color: var(--success); }
  .m-delta[data-tone='danger']  { color: var(--danger); }
  .m-delta[data-tone='neutral'] { color: var(--text-tertiary); }

  .m-sub {
    color: var(--text-tertiary);
    font-size: 11px;
    line-height: 1.3;
    font-variant-numeric: tabular-nums;
  }

  .m-spark {
    position: absolute;
    inset-block-end: 6px;
    inset-inline-end: 8px;
    opacity: 0.65;
    pointer-events: none;
  }

  @media (max-width: 760px) {
    .strip {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .m-tile {
      border-inline-start: 0;
      border-block-start: 1px solid var(--divider);
    }
    .m-tile:nth-child(2n) {
      border-inline-start: 1px solid var(--divider);
    }
    .m-tile:first-child,
    .m-tile:nth-child(2) {
      border-block-start: 0;
    }
  }
</style>
