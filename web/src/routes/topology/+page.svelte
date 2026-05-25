<!--
  /topology — Live Flow Command Center.

  This is the operator's "open at 3 a.m." surface. One screen, one
  pane, one stat ribbon, one graph, one detail panel. Auto-refreshes
  every 5 s, pauses when the tab is hidden, keeps the last-good cache
  on error and surfaces a stale-data indicator instead of going blank.

  Anatomy:
    PageHeader        title + subtitle + pipeline count + stale chip
    MetricStrip       Total · Active · Errors · DLQ backlog
    Two-column body
      LHS (~70 %)     <TopologyGraph topology bind:selectedId> + Alert
                      on fetch error (the cached graph stays visible).
      RHS (~30 %)     Side detail panel — connection / pipeline / legend.

  Polling lifecycle:
    onMount       → first fetch + setInterval(5 s)
    visibilitychange:hidden → clearInterval (no point polling backgrounded)
    visibilitychange:visible → immediate refresh + restart interval
    onDestroy     → clearInterval + remove listener
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type TopologyResponse, type TopologyConnection, type TopologyPipeline } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import MetricStrip from '$lib/components/MetricStrip.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Card from '$lib/components/Card.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ConnectionTypeIcon from '$lib/components/ConnectionTypeIcon.svelte';
  import TopologyGraph from '$lib/components/charts/TopologyGraph.svelte';
  import {
    Activity,
    ArrowRight,
    Inbox,
    BarChart3,
    Edit3,
    CircuitBoard
  } from 'lucide-svelte';

  let topology: TopologyResponse | null = null;
  // Acts as the fetch error message — empty string = healthy. We keep
  // the cached `topology` even when this is set so the screen never
  // goes blank between refreshes.
  let fetchError = '';
  // Wall-clock time of the last SUCCESSFUL response. Used both for the
  // "stale" indicator and for the relative ("Last updated 12 s ago")
  // chip beside the page title.
  let lastSuccessAt: number | null = null;
  // Re-renders the "Xs ago" string once a second without re-fetching.
  let nowTick = 0;
  let agoTimer: ReturnType<typeof setInterval> | undefined;

  let selectedId = '';
  let interval: ReturnType<typeof setInterval> | undefined;

  const POLL_MS = 5_000;

  async function refresh(): Promise<void> {
    try {
      const next = await api.get<TopologyResponse>('/v1/topology');
      topology = next;
      lastSuccessAt = Date.now();
      fetchError = '';
      // Preserve selection only when the selected id still exists in
      // the new graph; otherwise drop it so the side panel reverts to
      // the legend.
      if (selectedId) {
        const stillThere =
          next.connections.some((c) => c.id === selectedId) ||
          next.pipelines.some((p) => p.id === selectedId);
        if (!stillThere) selectedId = '';
      }
    } catch (e: unknown) {
      fetchError = (e as { message?: string }).message || 'unable to load topology';
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
    // Tick the "X seconds ago" label once a second regardless of poll.
    agoTimer = setInterval(() => (nowTick = Date.now()), 1000);
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', handleVisibility);
    }
  });

  onDestroy(() => {
    stopPolling();
    if (agoTimer) clearInterval(agoTimer);
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', handleVisibility);
    }
  });

  // ── Derived ribbon stats ───────────────────────────────────────
  // All four pills are simple roll-ups across the pipelines list.

  $: pipelineCount = topology?.pipelines.length ?? 0;
  $: activeCount = topology
    ? topology.pipelines.filter(
        (p) => p.status === 'connected' && p.circuit_state !== 'open'
      ).length
    : 0;
  $: errorCount = topology
    ? topology.pipelines.filter(
        (p) => p.status === 'error' || p.circuit_state === 'open'
      ).length
    : 0;
  $: dlqBacklog = topology
    ? topology.pipelines.reduce((sum, p) => sum + (p.dlq_depth || 0), 0)
    : 0;

  // ── Stale / "X seconds ago" indicator ──────────────────────────
  // Considered stale when the most recent fetch attempt failed AND we
  // last succeeded > 10 s ago, OR when the last success itself is
  // older than the polling cadence × 3 (a missed beat).
  $: ageMs = lastSuccessAt ? Math.max(0, nowTick - lastSuccessAt) : 0;
  $: isStale = !!fetchError && lastSuccessAt !== null && ageMs > 10_000;
  $: ageLabel = lastSuccessAt
    ? ageMs < 5_000
      ? 'just now'
      : ageMs < 60_000
        ? `${Math.round(ageMs / 1000)}s ago`
        : ageMs < 3_600_000
          ? `${Math.round(ageMs / 60_000)}m ago`
          : `${Math.round(ageMs / 3_600_000)}h ago`
    : '';

  // ── Side panel resolution ──────────────────────────────────────
  $: selectedConnection = (topology && selectedId
    ? topology.connections.find((c) => c.id === selectedId) ?? null
    : null) as TopologyConnection | null;
  $: selectedPipeline = (topology && selectedId
    ? topology.pipelines.find((p) => p.id === selectedId) ?? null
    : null) as TopologyPipeline | null;

  // For a selected connection — which pipelines feed in/out of it?
  let connectionPipelines: TopologyPipeline[] = [];
  $: connectionPipelines =
    selectedConnection && topology
      ? topology.pipelines.filter(
          (p) =>
            p.source_id === selectedConnection!.id ||
            p.destination_id === selectedConnection!.id
        )
      : [];

  // For a selected pipeline — resolve src + dst connection rows so the
  // side panel can show name + broker glyph without a second lookup.
  $: pipelineSrc =
    selectedPipeline && topology
      ? topology.connections.find((c) => c.id === selectedPipeline!.source_id) ?? null
      : null;
  $: pipelineDst =
    selectedPipeline && topology
      ? topology.connections.find((c) => c.id === selectedPipeline!.destination_id) ?? null
      : null;

  function fmtCount(n: number): string {
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
    if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
    return String(n);
  }

  function fmtLatency(ms: number): string {
    if (!ms) return '0 ms';
    if (ms < 10) return `${ms.toFixed(1)} ms`;
    return `${Math.round(ms)} ms`;
  }

  function circuitTone(state: string): 'success' | 'warning' | 'danger' | 'neutral' {
    if (state === 'closed') return 'success';
    if (state === 'half-open') return 'warning';
    if (state === 'open') return 'danger';
    return 'neutral';
  }
</script>

<svelte:head>
  <title>Topology · mqConnector</title>
</svelte:head>

<div class="topology-page">
  <PageHeader
    title={t($locale, 'topology.title') || 'Topology'}
    subtitle={t($locale, 'topology.subtitle') ||
      'Live flow between brokers and pipelines for this tenant'}
    count={pipelineCount}
  >
    <div slot="primary" class="ph-meta-row">
      {#if lastSuccessAt !== null}
        <span class="stamp" class:stamp-stale={isStale} aria-live="polite">
          <span class="stamp-dot" aria-hidden="true"></span>
          {isStale ? 'Stale · ' : ''}Last updated {ageLabel}
        </span>
      {/if}
    </div>
  </PageHeader>

  <MetricStrip
    metrics={[
      {
        label: 'Total pipelines',
        value: pipelineCount,
        sub: pipelineCount === 1 ? 'pipeline' : 'pipelines'
      },
      {
        label: 'Active',
        value: activeCount,
        tone: 'success',
        sub: activeCount === pipelineCount ? 'all running' : 'connected & healthy'
      },
      {
        label: 'Errors',
        value: errorCount,
        tone: errorCount > 0 ? 'danger' : 'default',
        sub: errorCount > 0 ? 'attention required' : 'no failures'
      },
      {
        label: 'DLQ backlog',
        value: fmtCount(dlqBacklog),
        tone: dlqBacklog > 0 ? 'warning' : 'default',
        sub: dlqBacklog > 0 ? 'messages waiting' : 'queue empty',
        href: dlqBacklog > 0 ? '/dlq' : undefined
      }
    ]}
  />

  <div class="topology-body">
    <section class="topology-canvas" aria-label="Topology graph">
      {#if fetchError}
        <div class="topology-alert">
          <Alert variant="warning">
            <span slot="title">Topology unavailable</span>
            {fetchError}. Retrying in {POLL_MS / 1000}s.
          </Alert>
        </div>
      {/if}
      {#if topology && topology.connections.length === 0 && topology.pipelines.length === 0}
        <div class="topology-empty">
          <EmptyState
            illustration="connections"
            title={t($locale, 'topology.empty.title') || 'No brokers connected yet'}
            body={t($locale, 'topology.empty.body') ||
              'Add a connection and create a pipeline to see the flow light up here.'}
          >
            <a slot="action" class="btn btn-primary" href="/pipelines">Create a pipeline</a>
          </EmptyState>
        </div>
      {:else}
        <TopologyGraph {topology} bind:selectedId />
      {/if}
    </section>

    <aside class="topology-side" aria-label="Selection details">
      <Card padding="md">
        {#if selectedConnection}
          <header class="side-header">
            <span class="side-icon" aria-hidden="true">
              <ConnectionTypeIcon type={selectedConnection.type} size={18} />
            </span>
            <div class="side-title-block">
              <h2 class="side-title">{selectedConnection.name}</h2>
              <div class="side-pills">
                <Badge variant="neutral">{selectedConnection.type}</Badge>
                <Badge variant={selectedConnection.connected ? 'success' : 'neutral'}>
                  {selectedConnection.connected ? 'connected' : 'idle'}
                </Badge>
              </div>
            </div>
          </header>

          <dl class="side-grid">
            {#if selectedConnection.topic}
              <div class="side-row">
                <dt>Topic / queue</dt>
                <dd class="mono">{selectedConnection.topic}</dd>
              </div>
            {/if}
            <div class="side-row">
              <dt>Depth</dt>
              <dd class="mono">
                {selectedConnection.depth === null || selectedConnection.depth === undefined
                  ? '—'
                  : fmtCount(selectedConnection.depth)}
              </dd>
            </div>
          </dl>

          <hr class="side-divider" />

          <h3 class="side-subtitle">
            Pipelines · {connectionPipelines.length}
          </h3>
          {#if connectionPipelines.length === 0}
            <p class="side-muted">No pipeline currently uses this connection.</p>
          {:else}
            <ul class="side-list">
              {#each connectionPipelines as p (p.id)}
                {@const role = p.source_id === selectedConnection.id ? 'source' : 'destination'}
                <li>
                  <a class="side-link" href={`/metrics`}>
                    <span class="side-link-name">{p.name}</span>
                    <span class="side-link-role">{role}</span>
                    <ArrowRight size={12} aria-hidden="true" />
                  </a>
                </li>
              {/each}
            </ul>
          {/if}
        {:else if selectedPipeline}
          <header class="side-header">
            <span class="side-icon" aria-hidden="true">
              <Activity size={18} />
            </span>
            <div class="side-title-block">
              <h2 class="side-title">{selectedPipeline.name}</h2>
              <div class="side-pills">
                <Badge
                  variant={selectedPipeline.status === 'connected'
                    ? 'success'
                    : selectedPipeline.status === 'error'
                      ? 'danger'
                      : 'neutral'}
                >
                  {selectedPipeline.status}
                </Badge>
                <Badge variant={circuitTone(selectedPipeline.circuit_state)}>
                  circuit · {selectedPipeline.circuit_state}
                </Badge>
              </div>
            </div>
          </header>

          <div class="side-flow" aria-label="source to destination">
            <span class="flow-chip">
              {#if pipelineSrc}<ConnectionTypeIcon type={pipelineSrc.type} size={12} />{/if}
              <span class="flow-chip-name">{pipelineSrc?.name ?? 'unknown'}</span>
            </span>
            <ArrowRight size={14} aria-hidden="true" />
            <span class="flow-chip">
              {#if pipelineDst}<ConnectionTypeIcon type={pipelineDst.type} size={12} />{/if}
              <span class="flow-chip-name">{pipelineDst?.name ?? 'unknown'}</span>
            </span>
          </div>

          <dl class="side-grid">
            <div class="side-row">
              <dt>Throughput</dt>
              <dd class="mono">{fmtCount(selectedPipeline.msg_per_min)} <span class="u">msg/min</span></dd>
            </div>
            <div class="side-row">
              <dt>Processed</dt>
              <dd class="mono">{fmtCount(selectedPipeline.processed)}</dd>
            </div>
            <div class="side-row">
              <dt>Failed</dt>
              <dd class="mono" class:is-danger={selectedPipeline.failed > 0}>
                {fmtCount(selectedPipeline.failed)}
              </dd>
            </div>
            <div class="side-row">
              <dt>Avg latency</dt>
              <dd class="mono">{fmtLatency(selectedPipeline.avg_latency_ms)}</dd>
            </div>
            <div class="side-row">
              <dt>DLQ depth</dt>
              <dd class="mono" class:is-warning={selectedPipeline.dlq_depth > 0}>
                {fmtCount(selectedPipeline.dlq_depth)}
              </dd>
            </div>
          </dl>

          {#if selectedPipeline.last_error}
            <div class="side-error">
              <strong>Last error</strong>
              <code>{selectedPipeline.last_error}</code>
            </div>
          {/if}

          <hr class="side-divider" />

          <div class="side-actions">
            <a class="btn btn-primary side-btn" href={`/pipelines/${selectedPipeline.id}/studio`}>
              <Edit3 size={14} aria-hidden="true" />
              <span>Open in Studio</span>
            </a>
            <a class="btn btn-ghost side-btn" href="/metrics">
              <BarChart3 size={14} aria-hidden="true" />
              <span>Metrics</span>
            </a>
            <a class="btn btn-ghost side-btn" href={`/dlq?pipeline=${selectedPipeline.id}`}>
              <Inbox size={14} aria-hidden="true" />
              <span>DLQ</span>
            </a>
          </div>
        {:else}
          <header class="side-header">
            <span class="side-icon" aria-hidden="true">
              <CircuitBoard size={18} />
            </span>
            <div class="side-title-block">
              <h2 class="side-title">Inspect a node or edge</h2>
              <p class="side-muted">
                Click a broker or a pipeline edge to see live details and quick actions.
              </p>
            </div>
          </header>
          <hr class="side-divider" />
          <h3 class="side-subtitle">Legend</h3>
          <ul class="legend">
            <li><span class="dot dot-healthy" aria-hidden="true"></span> Healthy — closed circuit</li>
            <li><span class="dot dot-warning" aria-hidden="true"></span> Warning — half-open circuit</li>
            <li><span class="dot dot-danger" aria-hidden="true"></span> Error / open circuit</li>
            <li><span class="dot dot-dim" aria-hidden="true"></span> Disabled or shadow destination</li>
          </ul>
        {/if}
      </Card>
    </aside>
  </div>
</div>

<style>
  .topology-page {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    padding-block-end: 1rem;
  }

  .ph-meta-row {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }

  .stamp {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.25rem 0.6rem;
    border-radius: 999px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text-muted);
    font-size: 0.75rem;
    font-variant-numeric: tabular-nums;
  }
  .stamp-dot {
    inline-size: 6px;
    block-size: 6px;
    border-radius: 999px;
    background: var(--success);
  }
  .stamp-stale {
    color: var(--warning);
    border-color: color-mix(in srgb, var(--warning) 30%, transparent);
  }
  .stamp-stale .stamp-dot {
    background: var(--warning);
  }

  .topology-body {
    display: grid;
    grid-template-columns: minmax(0, 7fr) minmax(18rem, 3fr);
    gap: 1rem;
    align-items: stretch;
  }
  @media (max-width: 960px) {
    .topology-body {
      grid-template-columns: minmax(0, 1fr);
    }
  }

  .topology-canvas {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    min-block-size: 540px;
  }
  /* Make TopologyGraph fill the column. The graph component is
     internally height: 100%, but the column itself needs an explicit
     min-block-size so the SVG actually has room to draw. */
  .topology-canvas :global(.topo-wrap) {
    flex: 1;
  }

  .topology-empty {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    border: 1px dashed var(--border);
    border-radius: 16px;
    background: var(--surface);
  }

  /* Pin the warning to the top of the canvas area without forcing the
     graph below it to reflow on every render — flex gap above already
     handles the spacing. */

  .topology-side {
    min-inline-size: 18rem;
  }
  /* Stretch the Card to the column height so the side rail visually
     matches the canvas height. */
  .topology-side :global(.card) {
    block-size: 100%;
  }

  .side-header {
    display: flex;
    align-items: flex-start;
    gap: 0.625rem;
    margin-block-end: 0.875rem;
  }
  .side-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    inline-size: 32px;
    block-size: 32px;
    border-radius: 8px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text);
    flex-shrink: 0;
  }
  .side-title-block {
    min-inline-size: 0;
    flex: 1;
  }
  .side-title {
    margin: 0;
    font-size: 1rem;
    font-weight: 600;
    color: var(--text);
    overflow-wrap: anywhere;
  }
  .side-pills {
    margin-block-start: 0.375rem;
    display: inline-flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }

  .side-subtitle {
    margin: 0 0 0.5rem;
    font-size: 0.75rem;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .side-muted {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.8125rem;
    line-height: 1.5;
  }

  .side-divider {
    margin-block: 0.875rem;
    border: 0;
    border-block-start: 1px solid var(--divider);
  }

  .side-grid {
    display: grid;
    grid-template-columns: 1fr;
    gap: 0.5rem;
    margin: 0 0 0.25rem;
  }
  .side-row {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.75rem;
  }
  .side-row dt {
    color: var(--text-muted);
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .side-row dd {
    margin: 0;
    color: var(--text);
    font-weight: 600;
    font-size: 0.875rem;
  }
  .side-row dd .u {
    color: var(--text-tertiary);
    font-weight: 500;
    font-size: 0.75rem;
    margin-inline-start: 0.25rem;
  }
  .side-row dd.is-danger { color: var(--danger); }
  .side-row dd.is-warning { color: var(--warning); }

  .mono {
    font-variant-numeric: tabular-nums;
  }

  .side-flow {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    flex-wrap: wrap;
    padding: 0.5rem 0.625rem;
    border-radius: 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    margin-block-end: 0.75rem;
  }
  .flow-chip {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.2rem 0.5rem;
    border-radius: 999px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text);
    font-size: 0.75rem;
  }
  .flow-chip-name {
    overflow-wrap: anywhere;
  }

  .side-list {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .side-link {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.375rem 0.5rem;
    border-radius: 8px;
    color: var(--text);
    text-decoration: none;
    font-size: 0.8125rem;
    transition: background-color 150ms;
  }
  .side-link:hover {
    background: var(--card-hover-bg);
  }
  .side-link-name {
    flex: 1;
    overflow-wrap: anywhere;
  }
  .side-link-role {
    color: var(--text-tertiary);
    font-size: 0.6875rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .side-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem;
  }
  /* Anchor + icon row inside the side rail buttons. The .btn class
     already handles colours + 12 dp radius; we just nudge the icon +
     label to sit on one line. */
  .side-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.8125rem;
    text-decoration: none;
  }

  .side-error {
    margin-block-start: 0.75rem;
    padding: 0.5rem 0.625rem;
    border-radius: 8px;
    background: var(--danger-bg);
    border: 1px solid color-mix(in srgb, var(--danger) 25%, transparent);
    font-size: 0.75rem;
    color: var(--danger);
  }
  .side-error strong {
    display: block;
    margin-block-end: 0.25rem;
    font-size: 0.6875rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .side-error code {
    color: var(--text);
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    overflow-wrap: anywhere;
  }

  .legend {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    color: var(--text-muted);
    font-size: 0.8125rem;
  }
  .legend li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .dot {
    inline-size: 10px;
    block-size: 10px;
    border-radius: 999px;
    flex-shrink: 0;
  }
  .dot-healthy { background: var(--success); }
  .dot-warning { background: var(--warning); }
  .dot-danger  { background: var(--danger); }
  .dot-dim     { background: var(--border-strong); }
</style>
