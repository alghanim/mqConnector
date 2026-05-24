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
  import Card from '$lib/components/Card.svelte';
  import StudioHeader from './StudioHeader.svelte';
  import StudioPalette from './StudioPalette.svelte';
  import StudioCanvas from './StudioCanvas.svelte';
  import StudioInspector from './StudioInspector.svelte';

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
    // Task 12 will own the actual VersionRail open + diff fetch. For
    // Wave 1 the placeholder just flips state so the chrome animation
    // demos end-to-end.
    if (s?.latestRev) {
      studio.setComparison(s.latestRev.revision_number, s.deployedRev?.revision_number ?? 0, null);
      setTimeout(() => studio.clearComparison(), 1200);
    }
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

  function onDeploy() {
    studio.clearError();
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
      on:validate={onValidate}
      on:deploy={onDeploy}
      on:toggle-enabled={onEnableToggle}
    />

    <div class="studio-body">
      <aside class="studio-left" aria-label="Studio palette and version rail">
        <StudioPalette on:select={handlePaletteSelect} />
        <Card padding="sm">
          <p class="studio-stub-label">{t($locale, 'studio.placeholder.versionRail')}</p>
        </Card>
      </aside>

      <main class="studio-canvas" aria-label="Studio canvas">
        <StudioCanvas />
      </main>

      <aside class="studio-right" aria-label="Studio inspector">
        <StudioInspector />
      </aside>
    </div>

    <footer class="studio-dock" aria-label="Studio dry-run dock">
      <Card padding="sm">
        <p class="studio-stub-label">{t($locale, 'studio.placeholder.dock')}</p>
      </Card>
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

  .studio-stub-label {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.8125rem;
    font-style: italic;
    text-align: center;
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
