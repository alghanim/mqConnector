<!--
  AlertRibbon — sticky band that surfaces currently-firing SLO alerts.

  Polls GET /api/v1/alerts/active every 30 s. Pauses while the tab is
  hidden so a backgrounded admin page doesn't burn API calls. Renders
  nothing when:
    • the evaluator is disabled in the binary (evaluator_enabled=false)
    • there are zero firing alerts
    • the user dismissed the ribbon this session

  When at least one alert is firing, the ribbon docks BELOW the topnav,
  ABOVE the page content (sticky; z-index between topnav and page).
  Background tint shifts from warning → danger when any critical alert
  is in the set. A "View all" link deep-links into
  /observability/alerts for the full list with annotations.

  Dismissal is session-only: closing the ribbon hides it for this tab
  until a full page reload. The decision is intentional — we don't
  want to permanently mute alerts on the operator's behalf, but a
  banner that reappears on every poll while they're triaging an
  incident is hostile.
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { AlertTriangle, AlertOctagon, X } from 'lucide-svelte';
  import { api, type AlertsResponse, type FiringAlert } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';

  // Internal state.
  let alerts: FiringAlert[] = [];
  let evaluatorEnabled = false;
  let dismissed = false;
  let interval: ReturnType<typeof setInterval> | undefined;

  // Refresh every 30 s — matches the SLO evaluator's own default
  // interval, so we never miss a state change by more than two
  // ticks worst-case.
  const POLL_MS = 30_000;

  async function refresh(): Promise<void> {
    try {
      const res = await api.get<AlertsResponse>('/v1/alerts/active');
      alerts = res.alerts ?? [];
      evaluatorEnabled = res.evaluator_enabled;
    } catch {
      // Best-effort: a transient fetch error leaves the prior
      // state in place. The ribbon doesn't surface fetch errors
      // — its job is alert visibility, not API liveness.
    }
  }

  function startPolling(): void {
    if (interval) return;
    interval = setInterval(refresh, POLL_MS);
  }

  function stopPolling(): void {
    if (interval) {
      clearInterval(interval);
      interval = undefined;
    }
  }

  function handleVisibility(): void {
    if (typeof document === 'undefined') return;
    if (document.visibilityState === 'hidden') {
      stopPolling();
    } else {
      void refresh();
      startPolling();
    }
  }

  onMount(() => {
    void refresh();
    startPolling();
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', handleVisibility);
    }
  });

  onDestroy(() => {
    stopPolling();
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', handleVisibility);
    }
  });

  // Severity-driven styling. ANY critical alert tips the whole ribbon
  // into danger colours — the most-severe-wins rule mirrors how a
  // human operator would prioritise the band on a glance.
  $: hasCritical = alerts.some((a) => a.severity === 'critical');
  $: visible = !dismissed && evaluatorEnabled && alerts.length > 0;
  $: variant = hasCritical ? 'danger' : 'warning';
  // The "headline" alert — pick the first after the API's
  // severity-DESC, started_at-DESC sort. Its summary annotation
  // becomes the inline detail line.
  $: headline = alerts[0];
  $: headlineSummary = headline?.annotations?.summary ?? headline?.name ?? '';

  function onDismiss(): void {
    dismissed = true;
  }
</script>

{#if visible}
  <div
    class="ribbon ribbon-{variant}"
    role="status"
    aria-live="polite"
    data-testid="alert-ribbon"
  >
    <span class="ribbon-icon" aria-hidden="true">
      {#if hasCritical}
        <AlertOctagon size={18} />
      {:else}
        <AlertTriangle size={18} />
      {/if}
    </span>
    <div class="ribbon-body">
      <span class="ribbon-count">
        {alerts.length}
        {alerts.length === 1
          ? t($locale, 'alerts.singular')
          : t($locale, 'alerts.plural')}
      </span>
      {#if headlineSummary}
        <span class="ribbon-sep" aria-hidden="true">·</span>
        <span class="ribbon-summary">{headlineSummary}</span>
      {/if}
    </div>
    <a class="ribbon-link" href="/observability/alerts">
      {t($locale, 'alerts.viewAll')}
    </a>
    <button
      type="button"
      class="ribbon-dismiss"
      aria-label={t($locale, 'alerts.dismiss')}
      on:click={onDismiss}
    >
      <X size={14} aria-hidden="true" />
    </button>
  </div>
{/if}

<style>
  /*
   * The ribbon docks BELOW the topnav and ABOVE the page. The topnav
   * is position:sticky; top:0; z-index:30 — the ribbon sits at the
   * same `top:0` but uses an isolation context so it scrolls into
   * place underneath. z-index 25 keeps it above page content but
   * below popovers / command-palette overlays (which sit at 50+).
   *
   * Layout is logical-property-only so RTL flips the icon + dismiss
   * positions without manual mirroring.
   */
  .ribbon {
    position: sticky;
    top: 0;
    z-index: 25;
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding-inline: 1rem;
    padding-block: 0.5rem;
    border-block-end: 1px solid transparent;
    font-size: 0.8125rem;
    line-height: 1.4;
  }
  .ribbon-warning {
    background: var(--warning-bg);
    color: var(--warning);
    border-block-end-color: color-mix(in srgb, var(--warning) 30%, transparent);
  }
  .ribbon-danger {
    background: var(--danger-bg);
    color: var(--danger);
    border-block-end-color: color-mix(in srgb, var(--danger) 30%, transparent);
  }
  .ribbon-icon {
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
  }
  .ribbon-body {
    flex: 1 1 auto;
    min-inline-size: 0;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ribbon-count {
    font-weight: 600;
    flex-shrink: 0;
  }
  .ribbon-sep {
    opacity: 0.6;
    flex-shrink: 0;
  }
  .ribbon-summary {
    flex: 1 1 auto;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .ribbon-link {
    flex-shrink: 0;
    color: inherit;
    text-decoration: underline;
    text-underline-offset: 2px;
    font-weight: 500;
  }
  .ribbon-link:hover {
    text-decoration-thickness: 2px;
  }
  .ribbon-dismiss {
    flex-shrink: 0;
    inline-size: 22px;
    block-size: 22px;
    border-radius: 12px;
    background: transparent;
    color: inherit;
    border: none;
    cursor: pointer;
    opacity: 0.75;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    transition:
      opacity 200ms,
      background-color 200ms;
  }
  .ribbon-dismiss:hover {
    opacity: 1;
    background: color-mix(in srgb, currentColor 12%, transparent);
  }
</style>
