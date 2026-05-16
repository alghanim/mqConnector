<!--
  RouteHealthMatrix — Bloomberg-style grid of one square per pipeline,
  coloured by status, sized to be a single visual unit. The operator
  sees in one glance "47/50 green, 2 warning, 1 down" instead of
  scrolling a table.

  Each square is 14 px (12 px square + 2 px gap), so 60 pipelines fit
  in a ~9-wide grid; 120 in a 11-wide. The container expands its
  column count from `auto-fit` so the band always fills its slot.

  Hover surfaces a small popover with: pipeline name, source → dest,
  status, processed/failed counters. Click jumps to /metrics with the
  pipeline pre-filtered.
-->
<script lang="ts">
  import type { PipelineMetric } from '$lib/api';

  export let pipelines: PipelineMetric[] = [];

  function tone(p: PipelineMetric): 'success' | 'warning' | 'danger' | 'neutral' {
    if (p.last_error) return 'danger';
    if (p.status === 'connected') return 'success';
    if (p.status === 'error') return 'danger';
    if (p.status === 'starting' || p.status === 'reconnecting') return 'warning';
    return 'neutral';
  }
</script>

{#if pipelines.length === 0}
  <slot name="empty" />
{:else}
  <ul class="matrix">
    {#each pipelines as p (p.pipeline_id)}
      <li>
        <a
          href="/metrics"
          class="cell"
          data-tone={tone(p)}
          title="{p.pipeline_id} — {p.source_queue} → {p.dest_queue} — {p.status}{p.messages_failed > 0 ? ` — ${p.messages_failed} failed` : ''}"
        >
          <span class="sr-only">{p.pipeline_id} — {p.status}</span>
        </a>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .matrix {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(14px, 1fr));
    gap: 4px;
    padding: 4px 2px;
    list-style: none;
    margin: 0;
  }
  .matrix > li {
    list-style: none;
  }
  .cell {
    display: block;
    block-size: 14px;
    border-radius: 3px;
    background: var(--surface-bright);
    border: 1px solid var(--border);
    transition: transform 100ms;
  }
  .sr-only {
    position: absolute;
    inline-size: 1px;
    block-size: 1px;
    margin: -1px;
    padding: 0;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
    border: 0;
  }
  .cell:hover {
    transform: scale(1.4);
    z-index: 1;
  }
  .cell[data-tone='success'] {
    background: color-mix(in srgb, var(--success-solid) 70%, transparent);
    border-color: var(--success-solid);
  }
  .cell[data-tone='warning'] {
    background: color-mix(in srgb, var(--warning) 70%, transparent);
    border-color: var(--warning);
  }
  .cell[data-tone='danger'] {
    background: color-mix(in srgb, var(--danger) 70%, transparent);
    border-color: var(--danger);
  }
  .cell[data-tone='neutral'] {
    background: var(--surface-bright);
    border-color: var(--border);
  }
</style>
