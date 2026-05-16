<!--
  StatChip — a single line of "label: value" with optional delta/trend.
  Use in a row at the top of a page (under the title) to give the
  operator a quick at-a-glance read of the most important numbers.
-->
<script lang="ts">
  export let label: string;
  export let value: string | number;
  export let trend: 'up' | 'down' | 'flat' | null = null;
  export let tone: 'default' | 'success' | 'warning' | 'danger' = 'default';
</script>

<div class="chip" data-tone={tone}>
  <span class="chip-label">{label}</span>
  <span class="chip-value">{value}</span>
  {#if trend === 'up'}
    <span class="chip-trend chip-trend-up" aria-hidden="true">▲</span>
  {:else if trend === 'down'}
    <span class="chip-trend chip-trend-down" aria-hidden="true">▼</span>
  {:else if trend === 'flat'}
    <span class="chip-trend" aria-hidden="true">⏤</span>
  {/if}
</div>

<style>
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.375rem 0.75rem;
    border-radius: 8px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    font-size: 0.8125rem;
    color: var(--text);
  }
  .chip-label {
    color: var(--text-muted);
    font-size: 0.75rem;
    letter-spacing: 0.01em;
  }
  .chip-value {
    font-weight: 600;
    color: var(--text);
  }
  .chip[data-tone='success'] .chip-value {
    color: var(--success);
  }
  .chip[data-tone='warning'] .chip-value {
    color: var(--warning);
  }
  .chip[data-tone='danger'] .chip-value {
    color: var(--danger);
  }
  .chip-trend {
    color: var(--text-muted);
    font-size: 0.6875rem;
  }
  .chip-trend-up {
    color: var(--success);
  }
  .chip-trend-down {
    color: var(--danger);
  }
</style>
