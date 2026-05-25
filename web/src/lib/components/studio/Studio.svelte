<!--
  Studio shell — the three-pane + bottom-dock layout for the Pipeline
  Studio. Mounts inside the /pipelines/{id}/studio route.

      ┌─────────────────────────────────────────────────────────────────┐
      │ StudioHeader: name · enabled · chip · Validate · Deploy        │
      ├──────────┬───────────────────────────────────┬──────────────────┤
      │ Palette  │           Canvas                  │  Inspector       │
      │  +       │ (placeholder for Task 9)          │ (placeholder     │
      │ Version  │                                   │  for Task 10)    │
      │  Rail    │                                   │                  │
      ├──────────┴───────────────────────────────────┴──────────────────┤
      │ DryRunDock — placeholder for Task 11                            │
      └─────────────────────────────────────────────────────────────────┘

  Wave 1 / Task 8 — chrome only. Every child except StudioHeader is a
  one-line Card placeholder; Tasks 9-12 will replace them. The store
  hooks (subscribe, dispatch validate/deploy) work today so the next
  implementer can wire components in without touching the shell.
-->
<script lang="ts">
  import { studio, type StudioStateData, type StudioStageType } from '$lib/stores/studio';
  import { locale, t } from '$lib/stores/locale';
  import { toasts } from '$lib/stores/toasts';
  import { api, type Stage, type Connection, type ConnectionType } from '$lib/api';
  import { metrics as liveMetrics, dlqTotal as liveDlqTotal } from '$lib/stores/live';
  import Card from '$lib/components/Card.svelte';
  import StudioHeader from './StudioHeader.svelte';
  import StudioPalette from './StudioPalette.svelte';
  import StudioCanvas from './StudioCanvas.svelte';
  import StudioInspector from './StudioInspector.svelte';
  import DryRunDock from './dryrun/DryRunDock.svelte';
  import VersionRail from './versions/VersionRail.svelte';
  import DeployDialog from './versions/DeployDialog.svelte';
  import DiffViewer, { type SnapshotDiff } from './versions/DiffViewer.svelte';

  export let pipelineId: string;

  // Subscribe to the studio store. `state` is the entire shape — we
  // destructure for template readability rather than passing the whole
  // object down to <StudioHeader>. The store guarantees referential
  // stability between unrelated mutations (Svelte writable shallow
  // diff), so this re-render path is cheap.
  let s: StudioStateData;
  const unsub = studio.subscribe((v) => (s = v));
  // Defensively unsubscribe on unmount. Without this, navigating away
  // and back would stack subscriptions and leak.
  import { onDestroy, onMount } from 'svelte';
  onDestroy(unsub);

  // The CommandPalette dispatches window-level CustomEvents so it
  // doesn't have to bind to a specific component. Wire up the
  // listeners here so the palette entries Just Work from anywhere.
  function onWindowDeployRequest() {
    onDeploy();
  }
  function onWindowCompareRequest() {
    // Expand the rail (via its exposed method) and stage the latest
    // revision for compare. The operator then taps Compare from the
    // rail toolbar to fire the diff.
    railRef?.expandForCompare();
  }
  onMount(() => {
    if (typeof window === 'undefined') return;
    window.addEventListener('studio:requestDeploy', onWindowDeployRequest);
    window.addEventListener('studio:openCompare', onWindowCompareRequest);
    return () => {
      window.removeEventListener('studio:requestDeploy', onWindowDeployRequest);
      window.removeEventListener('studio:openCompare', onWindowCompareRequest);
    };
  });

  // ─── Version rail / Deploy dialog state ────────────────────────────
  //
  // The rail handles its own diff fetch + emits `compare` with the
  // result — Studio just plugs the diff into the store so the canvas
  // can flip to read-only mode (StudioCanvas keys off
  // s.state === 'version-comparing').
  //
  // Rollback flow: rail emits `rollback {rev}`; we open the dialog
  // with kind='rollback' and let the dialog do the POST. On `done`
  // the dialog already called studio.hydrate; we just close.
  //
  // Deploy flow: rail emits `deploy {rev}` for the explicit re-deploy
  // path. The header's Deploy button is wired separately to onDeploy
  // (Wave 1 simplification — see notes below).
  let railRef: VersionRail | null = null;
  let deployDialogOpen = false;
  let deployDialogKind: 'deploy' | 'rollback' = 'deploy';
  let deployDialogTargetRev = 0;
  let deployDialogLiveRev: number | null = null;
  let deployDialogDiff: SnapshotDiff | null = null;

  function openDeployDialog(kind: 'deploy' | 'rollback', targetRev: number) {
    deployDialogKind = kind;
    deployDialogTargetRev = targetRev;
    deployDialogLiveRev = s?.deployedRev?.revision_number ?? null;
    deployDialogDiff = null; // DeployDialog will fetch on mount.
    deployDialogOpen = true;
  }

  function closeDeployDialog() {
    deployDialogOpen = false;
  }

  function onRailCompare(e: CustomEvent<{ from: number; to: number; diff: unknown }>) {
    studio.setComparison(e.detail.from, e.detail.to, e.detail.diff);
  }
  function onRailRollback(e: CustomEvent<{ rev: number }>) {
    openDeployDialog('rollback', e.detail.rev);
  }
  function onRailDeploy(e: CustomEvent<{ rev: number }>) {
    openDeployDialog('deploy', e.detail.rev);
  }
  function onExitComparison() {
    studio.clearComparison();
  }

  // The header binds enabled two-way through Switch. Pipeline enabled
  // lives on draft.pipeline; we expose a derived view for the binding
  // and forward toggles back into the draft via markDirty. Tasks 10/11
  // will add a real persistence path; for now the toggle just flips
  // the local copy and marks the draft dirty.
  $: enabled = s?.draft?.pipeline?.enabled ?? false;
  $: name = s?.draft?.pipeline?.name ?? '';
  $: latestRevNum = s?.latestRev?.revision_number ?? null;
  $: deployedRevNum = s?.deployedRev?.revision_number ?? null;
  $: comparisonFrom = s?.comparison?.from ?? null;
  $: comparisonTo = s?.comparison?.to ?? null;

  // Header summary inputs — the connection map is fetched once per
  // mount; live metrics come from the shared SSE store so we don't pay
  // for a per-pipeline poll on top of the dashboard's. Both are
  // optional — the header degrades to fewer pills if either is missing.
  let connectionMap = new Map<string, Connection>();
  void (async () => {
    try {
      const list = (await api.get<Connection[]>('/v1/connections')) ?? [];
      connectionMap = new Map(list.filter((c) => !!c.id).map((c) => [c.id as string, c]));
    } catch {
      /* non-fatal — header just shows fewer pills */
    }
  })();
  $: sourceConn = (() => {
    const id = s?.draft?.pipeline?.source_id;
    if (!id) return null;
    return connectionMap.get(id) ?? null;
  })();
  $: destConn = (() => {
    const id = s?.draft?.pipeline?.destination_id;
    if (!id) return null;
    return connectionMap.get(id) ?? null;
  })();
  $: headerSourceName = sourceConn?.name ?? null;
  $: headerSourceType = (sourceConn?.type ?? null) as ConnectionType | null;
  $: headerDestName = destConn?.name ?? null;
  $: headerDestType = (destConn?.type ?? null) as ConnectionType | null;

  // Pull this pipeline's live counters out of the shared SSE snapshot.
  // The snapshot is null until SSE delivers the first frame, which the
  // header handles by simply not rendering the pills (degrade
  // gracefully). msg/min is a synthesised value — the back-end gives
  // us cumulative `messages_processed`, but for "right now" we only
  // need a recent rate; SystemPulse does the same trick.
  let lastMsgCount = 0;
  let lastMsgAt = 0;
  let throughputPerMin: number | null = null;
  let failedTotal: number | null = null;
  $: if ($liveMetrics) {
    const pm = $liveMetrics.pipelines.find((p) => p.pipeline_id === pipelineId);
    if (pm) {
      failedTotal = pm.messages_failed;
      const now = $liveMetrics.receivedAt;
      if (lastMsgAt && now > lastMsgAt) {
        const dt = (now - lastMsgAt) / 1000;
        const dn = Math.max(0, pm.messages_processed - lastMsgCount);
        if (dt > 0) throughputPerMin = Math.round((dn / dt) * 60);
      }
      lastMsgCount = pm.messages_processed;
      lastMsgAt = now;
    }
  }
  // DLQ — global counter for now; the header treats >0 as a real
  // signal. A per-pipeline DLQ count would need a fresh endpoint that
  // doesn't exist yet, so we degrade gracefully and show the global
  // figure with the "DLQ" label.
  $: headerDlq = $liveDlqTotal > 0 ? $liveDlqTotal : null;

  function onEnableToggle(e: CustomEvent<boolean>) {
    if (!s?.draft?.pipeline) return;
    s.draft.pipeline.enabled = e.detail;
    studio.markDirty();
  }

  // Wave 1 placeholders. Tasks 11/12 wire the actual Validate / Deploy
  // flows; for now both log + flip state so the chrome animations
  // demo end-to-end.
  function onValidate() {
    // Task 11 wires the dry-run pipeline. For Task 8, just demo the
    // state transition — surrounded by clearError so a previous
    // failure doesn't stick.
    studio.clearError();
    studio.setState('validating');
    // Snap back after a tick so the chip animation is visible during
    // demo but doesn't leave the UI stuck.
    setTimeout(() => studio.setState(s.dirtyCount > 0 ? 'dirty' : 'building'), 600);
  }

  // Wave 1 simplification — see header docs of Task 12 (and Wave 1
  // plan §1.5). The Header's Deploy button has two paths:
  //
  //   - draft is dirty: open the deploy dialog against the LATEST
  //     existing revision (the legacy save-and-ship PUTs already
  //     marked everything deployed, so this is a no-op-on-snapshot,
  //     but it round-trips the operator through the same approval/
  //     summary ceremony as a re-deploy). A future "save draft as
  //     new revision" path will land in Wave 2.
  //
  //   - draft is clean: open the deploy dialog against the latest
  //     revision (re-deploy of current — useful after a hot-reload
  //     skip on the operator side, or just to verify the ceremony).
  //
  // If there is no revision history at all (a brand-new pipeline with
  // no deploys), we fall back to the old chrome-only animation so the
  // operator at least sees feedback — a Wave-2 "save draft creates
  // first revision" patch will replace this branch.
  function onDeploy() {
    studio.clearError();
    const targetRev = s?.latestRev?.revision_number ?? s?.deployedRev?.revision_number ?? null;
    if (targetRev !== null) {
      openDeployDialog('deploy', targetRev);
      return;
    }
    // Fallback: no revision history yet. Toast + chrome animation so
    // the operator sees that the action fired even though there's
    // nothing to deploy.
    toasts.warning('No revision available to deploy yet.');
    studio.setState('deploying');
    setTimeout(() => studio.setState(s.dirtyCount > 0 ? 'dirty' : 'building'), 1200);
  }

  function onRetry() {
    void studio.hydrate(pipelineId);
  }

  // Palette → canvas. A click on a palette card emits `select` with the
  // stage type; we append the stage to the chain and select it so the
  // inspector immediately shows its (Task-10) editor. Drag-drop takes
  // a different code path — StudioCanvas calls `studio.addStage`
  // directly from its drop handler so the stage lands wherever the
  // operator released the mouse (currently always "end of chain").
  function handlePaletteSelect(e: CustomEvent<StudioStageType>) {
    const newId = studio.addStage(e.detail);
    studio.selectNode(newId);
  }

  // Task 11 wiring — the inspector re-emits a 'test' CustomEvent on its
  // <aside> when a per-stage editor's "Test on sample" button fires
  // (Task 10). The event bubbles up through the DOM, but Svelte's
  // typed `on:` handlers only know about standard HTMLElement events,
  // so we attach a manual addEventListener on a wrapper <div> via
  // bind:this. Studio.svelte bridges to the DryRunDock via a
  // bind:this reference; the dock exposes runSingleStage() which
  // builds a single-stage /preview request against the current sample.
  let dockRef: DryRunDock | null = null;
  let inspectorSlotEl: HTMLElement | null = null;
  function handleInspectorTest(e: Event) {
    const detail = (e as CustomEvent<{ stage: Stage | null; payload: unknown }>).detail;
    if (!detail || !detail.stage) return;
    void dockRef?.runSingleStage(detail.stage, detail.payload);
  }
  // The aside's `on:test` listener is registered after the element is
  // bound; we re-register if the element rotates (e.g. error → hydrate
  // sequence remounts the layout). Cleaned up on destroy.
  $: if (inspectorSlotEl) {
    inspectorSlotEl.addEventListener('test', handleInspectorTest as EventListener);
  }
  onDestroy(() => {
    inspectorSlotEl?.removeEventListener('test', handleInspectorTest as EventListener);
  });
</script>

{#if s?.state === 'error' && !s.draft}
  <!-- Hard error during hydrate. The route also surfaces an Alert; this
       inline retry stays mounted so the operator can recover without
       leaving the page. -->
  <div class="studio-error">
    <Card>
      <p class="studio-error-title">{t($locale, 'studio.error.title')}</p>
      <p class="studio-error-body">{s.error}</p>
      <button type="button" class="studio-error-retry" on:click={onRetry}>
        {t($locale, 'studio.error.retry')}
      </button>
    </Card>
  </div>
{:else if !s?.draft}
  <!-- Pre-hydration / mid-hydrate. The route renders the actual
       Skeleton; this fallback keeps the component standalone-testable. -->
  <div class="studio-loading" aria-busy="true">
    <p>{t($locale, 'studio.loading')}</p>
  </div>
{:else}
  <div class="studio-shell" data-state={s.state}>
    <StudioHeader
      {pipelineId}
      {name}
      {enabled}
      dirtyCount={s.dirtyCount}
      state={s.state}
      latestRev={latestRevNum}
      deployedRev={deployedRevNum}
      {comparisonFrom}
      {comparisonTo}
      sourceName={headerSourceName}
      sourceType={headerSourceType}
      destName={headerDestName}
      destType={headerDestType}
      {throughputPerMin}
      {failedTotal}
      dlqTotal={headerDlq}
      on:validate={onValidate}
      on:deploy={onDeploy}
      on:toggle-enabled={onEnableToggle}
    />

    <div class="studio-body">
      <aside class="studio-left" aria-label="Studio palette and version rail">
        <StudioPalette on:select={handlePaletteSelect} />
        <VersionRail
          bind:this={railRef}
          on:compare={onRailCompare}
          on:rollback={onRailRollback}
          on:deploy={onRailDeploy}
        />
      </aside>

      <main class="studio-canvas" aria-label="Studio canvas">
        {#if s.state === 'version-comparing' && s.comparison}
          <div class="studio-compare-overlay">
            <header class="studio-compare-head">
              <span class="studio-compare-title">
                Comparing #{s.comparison.from} → #{s.comparison.to}
              </span>
              <button type="button" class="studio-compare-close" on:click={onExitComparison}>
                Exit comparison
              </button>
            </header>
            <div class="studio-compare-body">
              <DiffViewer
                revA={s.comparison.from}
                revB={s.comparison.to}
                diff={(s.comparison.diff as SnapshotDiff) ?? {
                  pipeline_fields: [],
                  stages: { added: [], removed: [], modified: [] },
                  transforms: { added: [], removed: [], modified: [] },
                  routing_rules: { added: [], removed: [], modified: [] }
                }}
                onRollback={(rev) => openDeployDialog('rollback', rev)}
              />
            </div>
          </div>
        {:else}
          <StudioCanvas />
        {/if}
      </main>

      <aside class="studio-right" aria-label="Studio inspector" bind:this={inspectorSlotEl}>
        <StudioInspector />
      </aside>
    </div>

    {#if deployDialogOpen}
      <DeployDialog
        kind={deployDialogKind}
        pipelineId={pipelineId}
        targetRev={deployDialogTargetRev}
        liveRev={deployDialogLiveRev}
        diff={deployDialogDiff}
        requiresApproval={s.draft?.pipeline?.requires_approval ?? false}
        on:done={closeDeployDialog}
        on:cancel={closeDeployDialog}
      />
    {/if}

    <footer class="studio-dock" aria-label="Studio dry-run dock">
      <DryRunDock bind:this={dockRef} />
    </footer>
  </div>
{/if}

<style>
  /*
   * Three-pane + dock layout. Tailwind isn't ideal for grid-template
   * with sidebar collapse, so the bones live in a scoped block-style
   * <style>. Tokens drive every colour + dimension that isn't a raw
   * inline (no hex, no magic colors).
   *
   * Narrow screens (≤ 900px): the side panels stack ABOVE the canvas
   * instead of beside it. The dock stays at the bottom.
   */
  .studio-shell {
    display: grid;
    grid-template-rows: auto 1fr auto;
    block-size: calc(100dvh - 4rem);
    /* The app shell's header adds ~4rem of vertical chrome above us.
       100dvh - 4rem keeps the studio inside the viewport on mobile
       browsers that toggle their URL bar (dvh handles that case). */
    min-block-size: 32rem;
    background: var(--bg, var(--surface));
  }
  .studio-body {
    display: grid;
    grid-template-columns: minmax(14rem, 18rem) 1fr minmax(16rem, 22rem);
    gap: 0.75rem;
    padding: 0.75rem;
    overflow: hidden;
    min-block-size: 0;
  }
  .studio-left,
  .studio-right {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    overflow-y: auto;
    min-block-size: 0;
  }
  .studio-canvas {
    overflow: hidden;
    min-block-size: 0;
    display: flex;
  }
  .studio-canvas > :global(*) {
    flex: 1;
  }
  .studio-dock {
    border-block-start: 1px solid var(--border);
    padding: 0.75rem;
    background: var(--surface);
    min-block-size: 4rem;
  }

  /* Comparison overlay replaces the canvas while
     state === 'version-comparing'. Kept inside studio-canvas so the
     grid sizing stays consistent. */
  .studio-compare-overlay {
    display: flex;
    flex-direction: column;
    flex: 1;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    overflow: hidden;
    min-block-size: 0;
  }
  .studio-compare-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.5rem 0.75rem;
    background: var(--surface);
    border-block-end: 1px solid var(--border);
  }
  .studio-compare-title {
    font-size: 0.75rem;
    font-weight: 600;
    color: var(--text);
  }
  .studio-compare-close {
    background: transparent;
    border: 1px solid var(--border);
    color: var(--text);
    border-radius: 8px;
    padding: 0.25rem 0.5rem;
    font-size: 0.6875rem;
    cursor: pointer;
  }
  .studio-compare-close:hover {
    border-color: var(--accent);
  }
  .studio-compare-body {
    padding: 0.75rem;
    overflow: auto;
    min-block-size: 0;
  }

  /* Narrow viewports — collapse the side panels above the canvas so
     the editor is still usable on a tablet. */
  @media (max-inline-size: 900px) {
    .studio-body {
      grid-template-columns: 1fr;
      grid-template-rows: auto 1fr auto;
    }
    .studio-left,
    .studio-right {
      max-block-size: 12rem;
    }
  }

  .studio-loading {
    padding: 2rem;
    text-align: center;
    color: var(--text-muted);
  }

  .studio-error {
    padding: 1rem;
  }
  .studio-error-title {
    font-weight: 600;
    color: var(--danger);
    margin-block-end: 0.5rem;
  }
  .studio-error-body {
    color: var(--text);
    margin-block-end: 0.75rem;
  }
  .studio-error-retry {
    display: inline-flex;
    align-items: center;
    padding-block: 0.375rem;
    padding-inline: 0.875rem;
    border-radius: 12px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text);
    font-size: 0.8125rem;
    cursor: pointer;
    transition: border-color 120ms, background-color 120ms;
  }
  .studio-error-retry:hover {
    border-color: var(--accent);
    background: var(--surface-2);
  }
</style>
