<!--
  ObservabilityStatRibbon — compact 5-tile stat strip for /observability.

  Tiles:
    1. Total p99           — from latency explainer (total_p99_ms)
    2. Dominant stage      — largest p99 share, name + share %
    3. Failure rate        — from drift explainer's facts
    4. DLQ depth           — from /api/v1/topology (per-pipeline)
    5. Circuit state       — from circuit explainer's headline severity

  Tiles share a card chrome (Card with sm padding) — label-on-top,
  value-large beneath. A small "stale" amber dot appears when the
  source explanation's `as_of` is older than 60 s.
-->
<script lang="ts">
  import type { Explanation, LatencyStagesData, ExplainSeverity, CircuitState } from '$lib/api';
  import Card from '$lib/components/Card.svelte';

  export let latency: Explanation | null = null;
  export let drift: Explanation | null = null;
  export let circuit: Explanation | null = null;
  export let dlqDepth: number | null = null;
  export let circuitState: CircuitState | 'unknown' = 'unknown';

  // ── derive: total p99 ms + dominant stage ──────────────────────
  $: stagesData = (() => {
    if (!latency || !latency.sections) return null;
    const s = latency.sections.find((x) => x.kind === 'stages');
    if (!s || !s.data || typeof s.data !== 'object') return null;
    return s.data as LatencyStagesData;
  })();

  $: totalP99 = stagesData?.total_p99 ?? 0;

  $: dominantStage = (() => {
    if (!stagesData || !stagesData.stages || stagesData.stages.length === 0) {
      return { name: '—', share: 0 };
    }
    let best = stagesData.stages[0];
    for (const s of stagesData.stages) if (s.p99_ms > best.p99_ms) best = s;
    const share = stagesData.total_p99 > 0 ? best.p99_ms / stagesData.total_p99 : 0;
    return { name: best.name, share };
  })();

  // ── failure rate — read from drift facts ──────────────────────
  // Drift explainer surfaces a "Failure rate" fact pre-formatted.
  // Defensive: fall back to em-dash if we can't find it.
  $: failureRate = (() => {
    if (!drift || !drift.facts) return '—';
    const f = drift.facts.find(
      (x) => /failure|fail rate|drift/i.test(x.label)
    );
    return f?.value ?? '—';
  })();

  // ── stale-data flagging — 60 s heuristic ──────────────────────
  function ageSeconds(asOf: string | undefined): number {
    if (!asOf) return 0;
    const t = Date.parse(asOf);
    if (Number.isNaN(t)) return 0;
    return Math.max(0, Math.floor((Date.now() - t) / 1000));
  }
  $: latencyAge = ageSeconds(latency?.as_of);
  $: driftAge = ageSeconds(drift?.as_of);
  $: circuitAge = ageSeconds(circuit?.as_of);

  function fmtMs(v: number): string {
    if (v === 0) return '0 ms';
    if (v >= 1000) return `${(v / 1000).toFixed(2)} s`;
    if (v >= 100) return `${v.toFixed(0)} ms`;
    if (v >= 10) return `${v.toFixed(1)} ms`;
    return `${v.toFixed(2)} ms`;
  }

  function fmtPct(v: number): string {
    return `${(v * 100).toFixed(0)}%`;
  }

  function circuitTone(s: string): 'success' | 'warning' | 'danger' | 'neutral' {
    if (s === 'closed') return 'success';
    if (s === 'half-open') return 'warning';
    if (s === 'open') return 'danger';
    return 'neutral';
  }
  function severityTone(s: ExplainSeverity | undefined): 'success' | 'warning' | 'danger' | 'neutral' {
    if (s === 'critical') return 'danger';
    if (s === 'warning') return 'warning';
    if (s === 'info') return 'success';
    return 'neutral';
  }
</script>

<div class="ribbon" role="group" aria-label="Observability stats">
  <Card padding="sm">
    <div class="tile" data-testid="stat-total-p99">
      <span class="tile-label">Total p99</span>
      <span class="tile-value">{fmtMs(totalP99)}</span>
      {#if latencyAge > 60}
        <span class="tile-stale" title="data is {latencyAge}s old"></span>
      {/if}
    </div>
  </Card>
  <Card padding="sm">
    <div class="tile" data-testid="stat-dominant-stage">
      <span class="tile-label">Dominant stage</span>
      <span class="tile-value tile-value-name">{dominantStage.name}</span>
      <span class="tile-sub">{fmtPct(dominantStage.share)} of p99</span>
    </div>
  </Card>
  <Card padding="sm">
    <div class="tile" data-testid="stat-failure-rate">
      <span class="tile-label">Failure rate</span>
      <span class="tile-value">{failureRate}</span>
      {#if driftAge > 60}
        <span class="tile-stale" title="data is {driftAge}s old"></span>
      {/if}
    </div>
  </Card>
  <Card padding="sm">
    <div class="tile" data-testid="stat-dlq-depth">
      <span class="tile-label">DLQ depth</span>
      <span class="tile-value">{dlqDepth === null ? '—' : dlqDepth}</span>
    </div>
  </Card>
  <Card padding="sm">
    <div class="tile" data-testid="stat-circuit">
      <span class="tile-label">Circuit</span>
      <span class="tile-pill" data-tone={circuit ? severityTone(circuit.severity) : circuitTone(circuitState)}>
        <span class="tile-pill-dot" aria-hidden="true"></span>
        {circuitState}
      </span>
      {#if circuitAge > 60}
        <span class="tile-stale" title="data is {circuitAge}s old"></span>
      {/if}
    </div>
  </Card>
</div>

<style>
  .ribbon {
    display: grid;
    grid-template-columns: repeat(5, minmax(0, 1fr));
    gap: 10px;
    margin-block-end: 14px;
  }
  @media (max-width: 1100px) {
    .ribbon {
      grid-template-columns: repeat(3, minmax(0, 1fr));
    }
  }
  @media (max-width: 700px) {
    .ribbon {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  .tile {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-block-size: 56px;
  }
  .tile-label {
    color: var(--text-muted);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 600;
  }
  .tile-value {
    color: var(--text);
    font-size: 18px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    line-height: 1.15;
  }
  .tile-value-name {
    text-transform: none;
    letter-spacing: 0;
    font-family: 'SFMono-Regular', Menlo, monospace;
    font-size: 15px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tile-sub {
    color: var(--text-tertiary);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .tile-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding-block: 3px;
    padding-inline: 8px;
    border-radius: 6px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text);
    font-size: 12px;
    font-weight: 600;
    text-transform: capitalize;
    align-self: flex-start;
  }
  .tile-pill-dot {
    inline-size: 7px;
    block-size: 7px;
    border-radius: 999px;
    background: var(--text-tertiary);
  }
  .tile-pill[data-tone='success'] .tile-pill-dot {
    background: var(--success-solid);
  }
  .tile-pill[data-tone='warning'] .tile-pill-dot {
    background: var(--warning);
  }
  .tile-pill[data-tone='danger'] .tile-pill-dot {
    background: var(--danger);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--danger) 22%, transparent);
  }
  .tile-pill[data-tone='success'] {
    border-color: color-mix(in srgb, var(--success) 40%, var(--border));
  }
  .tile-pill[data-tone='warning'] {
    border-color: color-mix(in srgb, var(--warning) 40%, var(--border));
  }
  .tile-pill[data-tone='danger'] {
    border-color: color-mix(in srgb, var(--danger) 40%, var(--border));
  }

  .tile-stale {
    position: absolute;
    top: 0;
    inset-inline-end: 0;
    inline-size: 6px;
    block-size: 6px;
    border-radius: 999px;
    background: var(--warning);
    box-shadow: 0 0 0 2px var(--surface-2);
  }
</style>
