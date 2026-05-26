<!--
  /observability/alerts — full listing of currently-firing SLO alerts.

  Deep-link target for the AlertRibbon's "View all" link. Shows every
  alert with its full annotations, severity badge, label set, started-
  at timestamp, and the raw expression so the operator can pivot to
  Prometheus / Grafana with one copy. Polls every 30 s; pauses while
  the tab is hidden.

  Layout: PageHeader + Card list. The page intentionally does not
  embed a TopologyGraph-style canvas — the firing-alerts surface is a
  triage view, not a navigation view; the operator clicks through to
  /pipelines/<id> for actions.
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type AlertsResponse, type FiringAlert } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Card from '$lib/components/Card.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';

  let resp: AlertsResponse | null = null;
  let fetchError = '';
  let interval: ReturnType<typeof setInterval> | undefined;

  const POLL_MS = 30_000;

  async function refresh(): Promise<void> {
    try {
      resp = await api.get<AlertsResponse>('/v1/alerts/active');
      fetchError = '';
    } catch (e: unknown) {
      fetchError = (e as { message?: string }).message || 'unable to load alerts';
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

  function severityVariant(s: string): 'danger' | 'warning' | 'info' | 'neutral' {
    switch (s) {
      case 'critical':
        return 'danger';
      case 'warning':
        return 'warning';
      case 'info':
        return 'info';
      default:
        return 'neutral';
    }
  }

  function formatStartedAt(iso: string): string {
    try {
      return new Date(iso).toLocaleString($locale === 'ar' ? 'ar' : undefined);
    } catch {
      return iso;
    }
  }

  function alertLabelEntries(a: FiringAlert): Array<[string, string]> {
    const entries = Object.entries(a.labels ?? {});
    // Stable order — labels first, then sorted alphabetically.
    entries.sort(([a], [b]) => a.localeCompare(b));
    return entries;
  }
</script>

<svelte:head>
  <title>{t($locale, 'alerts.title')} · mqConnector</title>
</svelte:head>

<PageHeader
  title={t($locale, 'alerts.title')}
  subtitle={t($locale, 'alerts.subtitle')}
  count={resp?.total}
/>

{#if fetchError}
  <div class="page-row">
    <Alert variant="error">{fetchError}</Alert>
  </div>
{/if}

{#if resp && !resp.evaluator_enabled}
  <div class="page-row">
    <Alert variant="info">{t($locale, 'alerts.evaluatorDisabled')}</Alert>
  </div>
{:else if resp && resp.alerts.length === 0}
  <EmptyState illustration="metrics" title={t($locale, 'alerts.empty')} body="" />
{:else if resp}
  <div class="alerts-grid">
    {#each resp.alerts as a (a.name + JSON.stringify(a.labels))}
      <Card>
        <div class="alert-card-head">
          <div class="alert-card-title">
            <Badge variant={severityVariant(a.severity)}>{a.severity}</Badge>
            <span class="alert-name">{a.name}</span>
          </div>
          <div class="alert-card-when">
            {t($locale, 'alerts.startedAt')}: {formatStartedAt(a.started_at)}
          </div>
        </div>

        {#if a.annotations?.summary}
          <p class="alert-summary">{a.annotations.summary}</p>
        {/if}
        {#if a.annotations?.description}
          <p class="alert-description">{a.annotations.description}</p>
        {/if}

        <dl class="alert-meta">
          <div class="alert-meta-row">
            <dt>{t($locale, 'alerts.value')}</dt>
            <dd>{a.value}{a.threshold ? ' (' + a.threshold + ')' : ''}</dd>
          </div>
          {#if a.expr}
            <div class="alert-meta-row">
              <dt>{t($locale, 'alerts.expr')}</dt>
              <dd><code>{a.expr}</code></dd>
            </div>
          {/if}
          {#if alertLabelEntries(a).length > 0}
            <div class="alert-meta-row">
              <dt>{t($locale, 'alerts.severity')}</dt>
              <dd class="alert-labels">
                {#each alertLabelEntries(a) as [k, v]}
                  <span class="alert-label-chip">{k}={v}</span>
                {/each}
              </dd>
            </div>
          {/if}
          {#if a.annotations?.runbook_url}
            <div class="alert-meta-row">
              <dt>runbook</dt>
              <dd><a href={a.annotations.runbook_url} target="_blank" rel="noopener noreferrer">{a.annotations.runbook_url}</a></dd>
            </div>
          {/if}
        </dl>
      </Card>
    {/each}
  </div>
{/if}

<style>
  .page-row {
    margin-block-end: 1rem;
  }
  .alerts-grid {
    display: grid;
    gap: 1rem;
    grid-template-columns: 1fr;
  }
  .alert-card-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 1rem;
    flex-wrap: wrap;
    margin-block-end: 0.5rem;
  }
  .alert-card-title {
    display: inline-flex;
    align-items: center;
    gap: 0.625rem;
  }
  .alert-name {
    font-weight: 600;
    font-size: 1rem;
    color: var(--text);
  }
  .alert-card-when {
    font-size: 0.8125rem;
    color: var(--text-muted);
  }
  .alert-summary {
    margin: 0 0 0.25rem 0;
    color: var(--text);
    font-weight: 500;
  }
  .alert-description {
    margin: 0 0 0.75rem 0;
    color: var(--text-muted);
    white-space: pre-line;
  }
  .alert-meta {
    display: grid;
    gap: 0.5rem;
    margin: 0;
    font-size: 0.8125rem;
  }
  .alert-meta-row {
    display: grid;
    grid-template-columns: 6rem 1fr;
    gap: 0.5rem;
  }
  .alert-meta-row dt {
    color: var(--text-muted);
    font-weight: 500;
  }
  .alert-meta-row dd {
    margin: 0;
    color: var(--text);
    min-inline-size: 0;
    overflow-wrap: anywhere;
  }
  .alert-meta-row code {
    background: var(--surface-2);
    padding: 0.125rem 0.375rem;
    border-radius: 8px;
    font-size: 0.8125rem;
  }
  .alert-labels {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem;
  }
  .alert-label-chip {
    background: var(--surface-2);
    border: 1px solid var(--border);
    padding: 0.125rem 0.5rem;
    border-radius: 12px;
    font-size: 0.75rem;
    color: var(--text);
  }
</style>
