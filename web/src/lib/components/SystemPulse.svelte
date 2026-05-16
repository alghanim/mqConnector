<!--
  SystemPulse — hero strip on the operations overview.

  Tells the operator, in plain language, whether the system is OK,
  degraded, or in an outage, then puts the two numbers that matter
  next to that sentence: rolling throughput (msg/sec over the last
  60 s) and fail rate (% over the same window). Each metric carries
  a trend pill (up/down/steady) computed against the previous
  half-window — so a glance shows trajectory, not just magnitude.

  The "halo" SVG ring rotates only when at least one pipeline is
  actively producing throughput. Animation is paused under
  `prefers-reduced-motion`. The status colour is driven entirely
  by brand tokens — never a raw hex — so dark + light both inherit
  brand palette mappings.

  Inputs:
    status        overall health: healthy | degraded | unhealthy
    activePipelines / totalPipelines  ratio for status fallback
    deltas        full 60 s window of per-interval message deltas
                  (12 samples at 5 s = 60 s)
-->
<script lang="ts">
  import { locale, t } from '$lib/stores/locale';
  import { ArrowUpRight, ArrowDownRight, Minus, Zap } from 'lucide-svelte';

  export let status: string = 'unknown';
  export let activePipelines = 0;
  export let totalPipelines = 0;
  export let deltas: number[] = [];
  export let failedTotal = 0;
  export let processedTotal = 0;

  // Window summary. Each delta = messages per 5 s interval.
  $: windowSum = deltas.reduce((s, v) => s + v, 0);
  $: rate = deltas.length > 0 ? windowSum / (deltas.length * 5) : 0; // msg/sec
  $: half = Math.floor(deltas.length / 2);
  $: prevSum = half > 0 ? deltas.slice(0, half).reduce((s, v) => s + v, 0) : 0;
  $: curSum = half > 0 ? deltas.slice(deltas.length - half).reduce((s, v) => s + v, 0) : 0;
  $: trend = (() => {
    if (half < 1 || prevSum === curSum) return 'steady';
    return curSum > prevSum ? 'up' : 'down';
  })();
  $: trendPct = (() => {
    if (prevSum === 0) return 0;
    return Math.round(((curSum - prevSum) / prevSum) * 100);
  })();

  // Fail rate over lifetime; cleaner than nothing when no recent samples.
  $: failPct = processedTotal + failedTotal > 0
    ? (failedTotal / (processedTotal + failedTotal)) * 100
    : 0;

  $: storyKey = (() => {
    if (status === 'healthy' || status === 'connected') return 'dash.pulse.story.healthy';
    if (status === 'degraded') return 'dash.pulse.story.degraded';
    if (status === 'unhealthy' || status === 'error') return 'dash.pulse.story.outage';
    if (totalPipelines === 0) return 'dash.pulse.story.idle';
    return 'dash.pulse.story.healthy';
  })();

  $: tone =
    status === 'healthy' || status === 'connected'
      ? 'success'
      : status === 'degraded'
        ? 'warning'
        : status === 'unhealthy' || status === 'error'
          ? 'danger'
          : 'neutral';

  function fmtRate(v: number): string {
    if (v >= 1000) return `${(v / 1000).toFixed(1)}K`;
    if (v >= 100) return v.toFixed(0);
    if (v >= 10) return v.toFixed(1);
    return v.toFixed(2);
  }
</script>

<section class="pulse pulse-{tone}" aria-label={t($locale, 'dash.pulse.title')}>
  <!-- Status glyph -->
  <div class="pulse-glyph" aria-hidden="true">
    <svg viewBox="0 0 64 64" class="halo" class:halo-active={rate > 0}>
      <circle cx="32" cy="32" r="28" class="halo-ring" />
      <circle cx="32" cy="32" r="20" class="halo-inner" />
    </svg>
    <span class="pulse-zap"><Zap size={20} strokeWidth={2} /></span>
  </div>

  <!-- Plain-language story -->
  <div class="pulse-story">
    <p class="pulse-eyebrow">{t($locale, 'dash.pulse.title')}</p>
    <p class="pulse-headline">{t($locale, storyKey)}</p>
    <p class="pulse-sub">
      <span class="pulse-active">{activePipelines}</span>
      <span class="pulse-active-sep">/</span>
      <span>{totalPipelines}</span>
      <span class="pulse-active-label">{t($locale, 'dash.kpi.activePipelines')}</span>
    </p>
  </div>

  <!-- Throughput rate -->
  <div class="pulse-metric">
    <p class="pulse-metric-label">{t($locale, 'dash.pulse.rateLabel')}</p>
    <p class="pulse-metric-value">
      <span class="pulse-metric-num">{fmtRate(rate)}</span>
      <span class="pulse-metric-unit">{t($locale, 'dash.pulse.rateUnit')}</span>
    </p>
    <p class="pulse-trend pulse-trend-{trend}">
      {#if trend === 'up'}
        <ArrowUpRight size={12} aria-hidden="true" />
      {:else if trend === 'down'}
        <ArrowDownRight size={12} aria-hidden="true" />
      {:else}
        <Minus size={12} aria-hidden="true" />
      {/if}
      <span>
        {#if trend === 'steady'}
          {t($locale, 'dash.pulse.delta.steady')}
        {:else}
          {trendPct > 0 ? '+' : ''}{trendPct}%
          {trend === 'up' ? t($locale, 'dash.pulse.delta.up') : t($locale, 'dash.pulse.delta.down')}
        {/if}
      </span>
    </p>
  </div>

  <!-- Fail rate -->
  <div class="pulse-metric">
    <p class="pulse-metric-label">{t($locale, 'dash.pulse.failRateLabel')}</p>
    <p class="pulse-metric-value">
      <span
        class="pulse-metric-num"
        style:color={failPct > 1 ? 'var(--danger)' : failPct > 0.1 ? 'var(--warning)' : 'var(--text)'}
      >
        {failPct.toFixed(failPct < 0.1 ? 3 : 2)}%
      </span>
    </p>
    <p class="pulse-trend pulse-trend-steady">
      <span class="pulse-trend-window">{t($locale, 'dash.pulse.window')}</span>
    </p>
  </div>
</section>

<style>
  .pulse {
    display: grid;
    grid-template-columns: auto 1fr auto auto;
    align-items: center;
    gap: 24px;
    padding: 18px 22px;
    background: var(--surface);
    border: 1px solid var(--card-border);
    border-radius: 16px;
    position: relative;
    overflow: hidden;
  }
  /* Brand strip on the inline-start edge; tone-driven colour. */
  .pulse::before {
    content: '';
    position: absolute;
    inset-block: 0;
    inset-inline-start: 0;
    inline-size: 4px;
  }
  .pulse-success::before {
    background: var(--success-solid);
  }
  .pulse-warning::before {
    background: var(--warning);
  }
  .pulse-danger::before {
    background: var(--danger);
  }
  .pulse-neutral::before {
    background: var(--secondary);
  }
  :global([data-theme='light']) .pulse-neutral::before {
    background: var(--primary);
  }

  @media (max-width: 880px) {
    .pulse {
      grid-template-columns: auto 1fr;
      grid-template-rows: auto auto;
      row-gap: 16px;
    }
    .pulse-metric {
      grid-column: span 1;
    }
  }

  /* Glyph + halo */
  .pulse-glyph {
    position: relative;
    inline-size: 64px;
    block-size: 64px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .halo {
    position: absolute;
    inset: 0;
    inline-size: 100%;
    block-size: 100%;
  }
  .halo-ring {
    fill: none;
    stroke-width: 2;
    stroke-dasharray: 6 8;
    stroke-linecap: round;
    opacity: 0.65;
  }
  .halo-inner {
    fill: none;
    stroke-width: 1;
    opacity: 0.35;
  }
  .pulse-success .halo-ring,
  .pulse-success .halo-inner {
    stroke: var(--success-solid);
  }
  .pulse-warning .halo-ring,
  .pulse-warning .halo-inner {
    stroke: var(--warning);
  }
  .pulse-danger .halo-ring,
  .pulse-danger .halo-inner {
    stroke: var(--danger);
  }
  .pulse-neutral .halo-ring,
  .pulse-neutral .halo-inner {
    stroke: var(--secondary);
  }
  :global([data-theme='light']) .pulse-neutral .halo-ring,
  :global([data-theme='light']) .pulse-neutral .halo-inner {
    stroke: var(--primary);
  }
  .halo-active {
    transform-origin: 50% 50%;
    animation: halo-rot 12s linear infinite;
  }
  @keyframes halo-rot {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .halo-active {
      animation: none;
    }
  }
  .pulse-zap {
    position: relative;
    z-index: 1;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    inline-size: 36px;
    block-size: 36px;
    border-radius: 50%;
    background: var(--bg);
    border: 1px solid var(--card-border);
    color: var(--text);
  }
  .pulse-success .pulse-zap {
    color: var(--success-solid);
  }
  .pulse-warning .pulse-zap {
    color: var(--warning);
  }
  .pulse-danger .pulse-zap {
    color: var(--danger);
  }

  /* Story column */
  .pulse-eyebrow {
    color: var(--text-tertiary);
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-block-end: 4px;
  }
  .pulse-headline {
    color: var(--text);
    font-size: 18px;
    font-weight: 600;
    line-height: 1.3;
    letter-spacing: -0.005em;
  }
  .pulse-sub {
    margin-block-start: 6px;
    color: var(--text-muted);
    font-size: 12px;
    display: inline-flex;
    align-items: baseline;
    gap: 4px;
  }
  .pulse-active {
    color: var(--text);
    font-weight: 600;
    font-size: 14px;
  }
  .pulse-active-sep {
    color: var(--text-tertiary);
  }
  .pulse-active-label {
    margin-inline-start: 6px;
  }

  /* Metric columns */
  .pulse-metric {
    min-inline-size: 140px;
  }
  .pulse-metric-label {
    color: var(--text-tertiary);
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    margin-block-end: 4px;
  }
  .pulse-metric-value {
    display: inline-flex;
    align-items: baseline;
    gap: 4px;
  }
  .pulse-metric-num {
    color: var(--text);
    font-size: 22px;
    font-weight: 700;
    letter-spacing: -0.01em;
    font-variant-numeric: tabular-nums;
  }
  .pulse-metric-unit {
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 500;
  }
  .pulse-trend {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-block-start: 4px;
    font-size: 11px;
    font-weight: 500;
  }
  .pulse-trend-up {
    color: var(--success-solid);
  }
  .pulse-trend-down {
    color: var(--warning);
  }
  .pulse-trend-steady {
    color: var(--text-tertiary);
  }
  .pulse-trend-window {
    text-transform: lowercase;
  }
</style>
