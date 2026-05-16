<!--
  Top-bar status pill. Polls /api/health every 15 s and surfaces the
  aggregate as a coloured dot + label. Click takes the operator to the
  Overview where the per-pipeline detail lives.
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type Health } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';

  let health: Health | null = null;
  let interval: ReturnType<typeof setInterval> | undefined;

  async function refresh() {
    try {
      health = await api.get<Health>('/health');
    } catch {
      health = null;
    }
  }
  onMount(() => {
    void refresh();
    interval = setInterval(refresh, 15_000);
  });
  onDestroy(() => {
    if (interval) clearInterval(interval);
  });

  $: status = health?.status ?? 'unknown';
  $: label =
    status === 'healthy'
      ? t($locale, 'health.healthy')
      : status === 'degraded'
        ? t($locale, 'health.degraded')
        : status === 'unhealthy'
          ? t($locale, 'health.unhealthy')
          : t($locale, 'health.unknown');
</script>

<a class="pill status-{status}" href="/" aria-label="{t($locale, 'health.label')}: {label}">
  <span class="dot" aria-hidden="true"></span>
  <span class="lbl">{label}</span>
</a>

<style>
  .pill {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding-block: 0.25rem;
    padding-inline: 0.625rem;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text);
    font-size: 0.75rem;
    font-weight: 500;
    text-decoration: none;
    transition:
      background-color 200ms,
      border-color 200ms;
  }
  .pill:hover {
    background: var(--surface);
    border-color: var(--border-strong);
  }
  .dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 999px;
    background: var(--text-muted);
    box-shadow: 0 0 0 3px transparent;
  }
  .status-healthy .dot {
    background: var(--success);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--success) 22%, transparent);
  }
  .status-degraded .dot {
    background: var(--warning);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--warning) 22%, transparent);
    animation: pulse 1.6s ease-in-out infinite;
  }
  .status-unhealthy .dot {
    background: var(--danger);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--danger) 22%, transparent);
    animation: pulse 1.6s ease-in-out infinite;
  }
  @keyframes pulse {
    0%,
    100% {
      transform: scale(1);
      opacity: 1;
    }
    50% {
      transform: scale(1.18);
      opacity: 0.85;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .dot {
      animation: none !important;
    }
  }
</style>
