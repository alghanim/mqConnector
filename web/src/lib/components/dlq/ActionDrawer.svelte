<!--
  ActionDrawer — right-panel actions for the selected DLQ entry in the
  /dlq Cluster console.

  Three discrete capabilities:
    1. Replay simulation — POST /api/v1/dlq/{id}/replay-sim. Renders a
       per-stage outcome strip + a retry-confidence pill.
    2. Compare to another entry — dropdown of other recent_ids from the
       same cluster; on pick, GET /api/v1/dlq/{id}/diff?against=<other>
       and render with the charts/PayloadDiff primitive.
    3. Retry now / Delete — POST /api/v1/dlq/{id}/retry + DELETE
       /api/v1/dlq/{id}. Same endpoints the legacy drawer uses; we
       just expose the buttons here.

  The parent owns the network calls — we dispatch events and consume
  props. This keeps the drawer testable in isolation (mock the parent's
  handlers) and centralises error/toast handling in the page.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import Button from '$lib/components/Button.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Select from '$lib/components/Select.svelte';
  import PayloadDiff from '$lib/components/charts/PayloadDiff.svelte';
  import StageOutcomeStrip from './StageOutcomeStrip.svelte';
  import type {
    DLQEntry,
    DLQReplaySimResponse,
    DLQDiffResponse
  } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';

  /** Currently focused entry. Null = empty state. */
  export let entry: DLQEntry | null = null;
  /** Other recent_ids in the same cluster — used to populate the compare dropdown. */
  export let otherRecentIds: string[] = [];
  /** Whatever the parent has resolved so far for the replay-sim. */
  export let replaySim: DLQReplaySimResponse | null = null;
  /** Whatever the parent has resolved so far for the compare diff. */
  export let diff: DLQDiffResponse | null = null;
  /** True while the parent has an in-flight replay-sim or diff call. */
  export let busySim = false;
  export let busyDiff = false;
  /** Id currently picked in the compare dropdown — controlled from here. */
  export let compareId = '';

  const dispatch = createEventDispatcher<{
    runReplaySim: { id: string };
    pickCompare: { againstId: string };
    retry: { id: string };
    askDelete: { id: string };
  }>();

  // Compare dropdown options. Empty value = "pick one" placeholder.
  $: compareOptions = [
    { value: '', label: t($locale, 'dlq.clusters.drawer.compare.pick') },
    ...otherRecentIds.map((id) => ({ value: id, label: id.slice(0, 8) }))
  ];

  // Reactive bridge: when the parent-controlled `compareId` is set to
  // a non-empty value, dispatch upward so the parent can fetch the
  // diff. We guard on `entry` to avoid firing during the
  // entry-changed reset cycle.
  let lastCompareId = '';
  $: if (entry && compareId && compareId !== lastCompareId) {
    lastCompareId = compareId;
    dispatch('pickCompare', { againstId: compareId });
  }
  $: if (entry && !compareId) {
    lastCompareId = '';
  }

  function onRunSim() {
    if (!entry) return;
    dispatch('runReplaySim', { id: entry.id });
  }
  function onRetry() {
    if (!entry) return;
    dispatch('retry', { id: entry.id });
  }
  function onAskDelete() {
    if (!entry) return;
    dispatch('askDelete', { id: entry.id });
  }

  // Confidence pill copy + tone. `would_succeed` is the headline; we
  // also pull `failing_stage` into the unsafe-case copy so the
  // operator immediately knows where the retry would die.
  $: confidence = (() => {
    if (!replaySim) return null;
    if (replaySim.would_succeed) {
      return {
        tone: 'success' as const,
        label: t($locale, 'dlq.clusters.drawer.replaySim.safe')
      };
    }
    const atStage = replaySim.failing_stage || replaySim.error || '?';
    return {
      tone: 'danger' as const,
      label:
        t($locale, 'dlq.clusters.drawer.replaySim.unsafe') +
        ' · ' +
        t($locale, 'dlq.clusters.drawer.replaySim.atStage').replace('{name}', atStage)
    };
  })();
</script>

<aside class="action-drawer" data-testid="action-drawer" aria-label={t($locale, 'dlq.clusters.drawer.title')}>
  <header class="action-drawer-head">
    <h2 class="action-drawer-title">{t($locale, 'dlq.clusters.drawer.title')}</h2>
    {#if entry}
      <p class="action-drawer-subtitle" title={entry.id}>
        <code>{entry.id.slice(0, 8)}</code>
        <span aria-hidden="true">·</span>
        <time datetime={entry.created_at}>{entry.created_at}</time>
      </p>
    {/if}
  </header>

  {#if !entry}
    <div class="action-drawer-empty">
      <p class="action-drawer-empty-title">
        {t($locale, 'dlq.clusters.drawer.empty.title')}
      </p>
      <p class="action-drawer-empty-body">
        {t($locale, 'dlq.clusters.drawer.empty.body')}
      </p>
    </div>
  {:else}
    <section class="action-drawer-section" aria-label={t($locale, 'dlq.clusters.drawer.replaySim')}>
      <h3 class="action-drawer-section-title">
        {t($locale, 'dlq.clusters.drawer.replaySim')}
      </h3>
      <div class="action-drawer-section-controls">
        <Button on:click={onRunSim} loading={busySim} variant="outline">
          {busySim
            ? t($locale, 'dlq.clusters.drawer.replaySim.running')
            : t($locale, 'dlq.clusters.drawer.replaySim.run')}
        </Button>
        {#if confidence}
          <span class="confidence-pill" data-tone={confidence.tone}>
            <Badge variant={confidence.tone}>{confidence.label}</Badge>
          </span>
        {/if}
      </div>
      {#if replaySim}
        <div class="action-drawer-strip">
          <StageOutcomeStrip runs={replaySim.stage_runs ?? []} />
        </div>
      {/if}
    </section>

    <section class="action-drawer-section" aria-label={t($locale, 'dlq.clusters.drawer.compare')}>
      <h3 class="action-drawer-section-title">{t($locale, 'dlq.clusters.drawer.compare')}</h3>
      {#if otherRecentIds.length === 0}
        <p class="action-drawer-muted">{t($locale, 'dlq.clusters.drawer.compare.none')}</p>
      {:else}
        <Select bind:value={compareId} options={compareOptions} />
        {#if busyDiff}
          <p class="action-drawer-muted">…</p>
        {:else if diff}
          <div class="action-drawer-diff">
            <PayloadDiff
              operations={diff.diff}
              leftLabel={diff.from.id.slice(0, 8)}
              rightLabel={diff.to.id.slice(0, 8)}
            />
          </div>
        {/if}
      {/if}
    </section>

    <section class="action-drawer-section action-drawer-actions">
      <Button variant="outline" on:click={onAskDelete}>
        {t($locale, 'dlq.clusters.drawer.deleteEntry')}
      </Button>
      <Button on:click={onRetry}>{t($locale, 'dlq.clusters.drawer.retryNow')}</Button>
    </section>
  {/if}
</aside>

<style>
  .action-drawer {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    padding: 0.875rem 1rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 12px;
    min-block-size: 100%;
  }
  .action-drawer-head {
    display: flex;
    flex-direction: column;
    gap: 0.125rem;
    padding-block-end: 0.5rem;
    border-block-end: 1px solid var(--border);
  }
  .action-drawer-title {
    margin: 0;
    color: var(--text);
    font-size: 0.8125rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .action-drawer-subtitle {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.6875rem;
    display: flex;
    align-items: center;
    gap: 0.375rem;
  }
  .action-drawer-subtitle code {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    color: var(--text-tertiary);
  }
  .action-drawer-empty {
    color: var(--text-muted);
    padding-block: 1.5rem;
    text-align: center;
  }
  .action-drawer-empty-title {
    margin: 0;
    color: var(--text);
    font-size: 0.875rem;
    font-weight: 600;
  }
  .action-drawer-empty-body {
    margin: 0.25rem 0 0;
    color: var(--text-muted);
    font-size: 0.75rem;
    line-height: 1.5;
  }
  .action-drawer-section {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .action-drawer-section-title {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.6875rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .action-drawer-section-controls {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .action-drawer-strip {
    margin-block-start: 0.375rem;
  }
  .action-drawer-muted {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.75rem;
  }
  .action-drawer-diff {
    margin-block-start: 0.5rem;
  }
  .action-drawer-actions {
    flex-direction: row;
    gap: 0.5rem;
    justify-content: flex-end;
    padding-block-start: 0.5rem;
    border-block-start: 1px solid var(--border);
  }
  .confidence-pill {
    display: inline-flex;
    align-items: center;
  }
</style>
