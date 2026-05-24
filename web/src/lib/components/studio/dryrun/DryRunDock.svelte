<!--
  DryRunDock — bottom panel of the Pipeline Studio.

  Layout when expanded (~280px tall):

      ┌─────────────────────────────────────────────────────────────┐
      │ ▼ Dry-Run            [Run]  [Clear]               ✕ collapse│
      ├─────────────────────────────────────────────────────────────┤
      │ [SamplePicker — tabs + textarea / fixture cards]            │
      │ ─────────────────────────────────────────────────────────── │
      │ [StageOutcomeStrip (horizontal)]      │ [Final output card] │
      └─────────────────────────────────────────────────────────────┘

  Collapsed (~40px tall): a one-line header with the chevron + title.
  Persisted via localStorage so the operator's preference survives a
  reload (key `mqc.studio.dryrun.collapsed`).

  Run flow:
    1. operator picks a sample (or the dock already has one from a
       prior run)
    2. operator clicks Run → studio.beginDryRun() flips chrome to
       'simulating' and the Run button pulses
    3. dock builds the /v1/preview request from the draft stages +
       sample, POSTs, and on response:
         - 2xx → studio.finishDryRun(json) → strip + canvas overlays
                 render
         - error → studio.failDryRun(message) — message lands in
                   dockError, NOT in the build-error chip

  Inspector test event:
    StudioInspector re-emits a 'test' CustomEvent (Task 10) on its
    <aside> when an editor's "Test on sample" button fires. The Studio
    shell binds this to the dock via `dryRunDockRef.runSingleStage(...)`
    so the dock can build a single-stage preview against the operator's
    current sample. We expose `runSingleStage` as a public method on
    this component for that wiring.

  Layout notes:
    - The dock sits inside the Studio.svelte <footer>. We DON'T set
      position:sticky — the parent shell uses CSS grid to keep the
      footer pinned. Sticky-positioning here would conflict with the
      grid row sizing.
-->
<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { studio, type StudioStateData } from '$lib/stores/studio';
  import { locale, t } from '$lib/stores/locale';
  import { api, type Stage } from '$lib/api';
  import Button from '$lib/components/Button.svelte';
  import SamplePicker from './SamplePicker.svelte';
  import StageOutcomeStrip from './StageOutcomeStrip.svelte';
  import type { PreviewResponse } from './types';

  const STORAGE_KEY = 'mqc.studio.dryrun.collapsed';

  let s: StudioStateData;
  const unsub = studio.subscribe((v) => (s = v));
  onDestroy(unsub);

  // Collapsed state. Default to expanded so a first-time operator sees
  // the dock and understands the affordance; once they collapse it the
  // preference sticks via localStorage. The localStorage read is gated
  // on `typeof localStorage !== 'undefined'` for SSR safety even though
  // SvelteKit static adapter is browser-only for this route.
  let collapsed = false;
  onMount(() => {
    try {
      const v = localStorage.getItem(STORAGE_KEY);
      if (v === '1') collapsed = true;
    } catch {
      /* localStorage blocked — fall back to default */
    }
  });

  function setCollapsed(next: boolean) {
    collapsed = next;
    try {
      localStorage.setItem(STORAGE_KEY, next ? '1' : '0');
    } catch {
      /* no-op */
    }
  }

  function toggleCollapsed() {
    setCollapsed(!collapsed);
  }

  // ─── Sample state ──────────────────────────────────────────────────
  // We hold the picked sample locally rather than in the store — the
  // dry-run input isn't pipeline state, it's transient UX. A future
  // Wave-2 "tenant saved samples" feature can promote this to the
  // store; for Wave 1 a local string suffices.
  let sample = '';
  let running = false;

  function onSampleChange(e: CustomEvent<string>) {
    sample = e.detail;
  }

  // Build the /v1/preview request body. Two modes:
  //   - inline draft (when we have stages)
  //   - saved pipeline (rare on the studio — only if there are zero
  //     stages, which means the user is previewing the deployed copy)
  // In either case the operator's current sample wins.
  type PreviewReq = {
    pipeline_id?: string;
    stages?: Stage[];
    sample: string;
    output_format?: string;
  };
  function buildPreviewRequest(): PreviewReq {
    const draft = s?.draft;
    if (!draft) return { sample };
    // We hand the back-end the draft stages verbatim. They carry the
    // editor's stage_config JSON strings; pipeline.Build parses them.
    return {
      stages: draft.stages,
      output_format: draft.pipeline?.output_format,
      sample
    };
  }

  // ─── Run / Clear ───────────────────────────────────────────────────
  async function onRun() {
    if (!sample || sample.trim() === '') {
      studio.failDryRun(t($locale, 'studio.dryrun.error.noSample'));
      return;
    }
    running = true;
    studio.beginDryRun();
    try {
      const body = buildPreviewRequest();
      const res = await api.post<unknown>('/v1/preview', body);
      studio.finishDryRun(res);
    } catch (err) {
      const msg = (err as { message?: string })?.message ?? 'preview failed';
      studio.failDryRun(msg);
    } finally {
      running = false;
    }
  }

  function onClear() {
    studio.clearDryRun();
  }

  // ─── Inspector test event ──────────────────────────────────────────
  // Exposed as a public method so Studio.svelte can call it via
  // `bind:this`. We expand the dock automatically so the operator sees
  // the result land — collapsed dock + button click would otherwise
  // look like a no-op.
  export async function runSingleStage(stage: Stage, _payload?: unknown): Promise<void> {
    if (!sample || sample.trim() === '') {
      studio.failDryRun(t($locale, 'studio.dryrun.error.noSample'));
      if (collapsed) setCollapsed(false);
      return;
    }
    if (collapsed) setCollapsed(false);
    running = true;
    studio.beginDryRun();
    try {
      const body: PreviewReq = {
        stages: [stage],
        sample,
        output_format: s?.draft?.pipeline?.output_format
      };
      const res = await api.post<unknown>('/v1/preview', body);
      studio.finishDryRun(res);
    } catch (err) {
      const msg = (err as { message?: string })?.message ?? 'preview failed';
      studio.failDryRun(msg);
    } finally {
      running = false;
    }
  }

  // ─── Render-side derivations ───────────────────────────────────────
  // The dryRun shape from /v1/preview. We narrow defensively — anything
  // unrecognised falls back to safe defaults so a backend that adds a
  // field doesn't crash the dock.
  $: dryRun = (s?.dryRun as PreviewResponse | null) ?? null;
  $: runs = dryRun?.stage_runs ?? [];
  $: stagesForOverlay = s?.draft?.stages ?? [];
  $: hasResult = dryRun !== null;
  $: dockError = s?.dockError ?? null;
  $: outputFormat = dryRun?.format ?? '';
  $: outputBody = dryRun?.output ?? '';
  $: routes = dryRun?.routes ?? [];

  // Overlay forwarding to the canvas. The strip emits an `overlays`
  // event with the zipped {stageId,failed,durationMs}[] — Studio.svelte
  // could re-publish to the canvas via prop, but a window-level event is
  // simpler given the canvas already falls back to reading studio.dryRun
  // directly. We DON'T publish here — the canvas reads from the store.
  // (The strip's overlays event is the path Studio.svelte can use to
  // forward to a future <Canvas overlays={}>, but for Wave 1 the canvas
  // already covers this via the store.)
</script>

<section
  class="dock"
  class:is-collapsed={collapsed}
  data-state={s?.state ?? 'empty'}
  aria-label={t($locale, 'studio.dryrun.heading')}
>
  <header class="dock-head">
    <button
      type="button"
      class="dock-toggle"
      aria-expanded={!collapsed}
      aria-controls="dock-body"
      on:click={toggleCollapsed}
    >
      <span class="dock-toggle-chevron" aria-hidden="true">{collapsed ? '▶' : '▼'}</span>
      <span class="dock-title">{t($locale, 'studio.dryrun.heading')}</span>
    </button>
    <div class="dock-actions">
      <Button on:click={onRun} disabled={running} loading={running}>
        {t($locale, 'studio.dryrun.run')}
      </Button>
      <Button variant="outline" on:click={onClear} disabled={!hasResult && !dockError}>
        {t($locale, 'studio.dryrun.clear')}
      </Button>
    </div>
  </header>

  {#if !collapsed}
    <div id="dock-body" class="dock-body">
      <div class="dock-top">
        <SamplePicker bind:value={sample} on:change={onSampleChange} />
      </div>
      <div class="dock-divider" aria-hidden="true"></div>
      <div class="dock-bottom">
        <div class="dock-strip">
          {#if dockError}
            <div class="dock-error" role="alert">
              <strong class="dock-error-label">{t($locale, 'studio.dryrun.error.heading')}</strong>
              <span class="dock-error-msg">{dockError}</span>
            </div>
          {/if}
          {#if hasResult}
            <StageOutcomeStrip runs={runs} stages={stagesForOverlay} />
          {:else if !dockError}
            <p class="dock-hint">{t($locale, 'studio.dryrun.hint')}</p>
          {/if}
        </div>
        <aside class="dock-final" aria-label={t($locale, 'studio.dryrun.final.heading')}>
          <header class="dock-final-head">
            <span class="dock-final-h">{t($locale, 'studio.dryrun.final.heading')}</span>
            {#if outputFormat}
              <span class="dock-final-format">{outputFormat}</span>
            {/if}
          </header>
          {#if hasResult}
            <pre class="dock-final-body">{outputBody || t($locale, 'studio.dryrun.final.empty')}</pre>
            {#if routes.length > 0}
              <div class="dock-final-routes" aria-label={t($locale, 'studio.dryrun.final.routes')}>
                {#each routes as r (r)}
                  <span class="dock-final-route">{r}</span>
                {/each}
              </div>
            {/if}
          {:else}
            <p class="dock-final-empty">{t($locale, 'studio.dryrun.final.placeholder')}</p>
          {/if}
        </aside>
      </div>
    </div>
  {/if}
</section>

<style>
  .dock {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    background: var(--surface);
    border-block-start: 1px solid var(--border);
    padding: 0.5rem 0.75rem;
    min-block-size: 2.5rem;
    max-block-size: 18rem;
    transition: max-block-size 160ms ease;
  }
  .dock.is-collapsed {
    max-block-size: 2.5rem;
    padding-block: 0.375rem;
  }
  .dock-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .dock-toggle {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    background: transparent;
    border: none;
    color: var(--text);
    font-size: 0.8125rem;
    font-weight: 600;
    cursor: pointer;
    padding-block: 0.25rem;
    padding-inline: 0.25rem;
  }
  .dock-toggle:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
    border-radius: 4px;
  }
  .dock-toggle-chevron {
    color: var(--text-tertiary);
    font-size: 0.75rem;
  }
  .dock-title {
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-size: 0.6875rem;
  }
  .dock-actions {
    display: flex;
    gap: 0.5rem;
  }
  .dock-body {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    min-block-size: 0;
    overflow: hidden;
  }
  .dock-top {
    flex-shrink: 0;
  }
  .dock-divider {
    block-size: 1px;
    background: var(--border);
  }
  .dock-bottom {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(12rem, 18rem);
    gap: 0.5rem;
    min-block-size: 0;
    overflow: hidden;
  }
  .dock-strip {
    min-inline-size: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }
  .dock-hint {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.75rem;
    font-style: italic;
  }
  .dock-error {
    display: flex;
    gap: 0.5rem;
    align-items: baseline;
    padding: 0.375rem 0.5rem;
    background: var(--danger-bg);
    color: var(--danger);
    border: 1px solid var(--danger);
    border-radius: 8px;
    font-size: 0.75rem;
    flex-wrap: wrap;
  }
  .dock-error-label {
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 700;
  }
  .dock-error-msg {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    word-break: break-word;
  }
  .dock-final {
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    min-block-size: 0;
    overflow: hidden;
  }
  .dock-final-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .dock-final-h {
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-size: 0.6875rem;
    color: var(--text-tertiary);
    font-weight: 600;
  }
  .dock-final-format {
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-size: 0.625rem;
    font-weight: 600;
    background: var(--surface-high);
    color: var(--text);
    padding-block: 0.0625rem;
    padding-inline: 0.375rem;
    border-radius: 999px;
  }
  .dock-final-body {
    margin: 0;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 0.375rem 0.5rem;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.6875rem;
    line-height: 1.4;
    color: var(--text);
    max-block-size: 8rem;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .dock-final-routes {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }
  .dock-final-route {
    font-size: 0.625rem;
    font-weight: 600;
    background: var(--info-bg);
    color: var(--info);
    border-radius: 999px;
    padding-block: 0.0625rem;
    padding-inline: 0.375rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }
  .dock-final-empty {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.75rem;
    font-style: italic;
  }

  @media (max-inline-size: 720px) {
    .dock-bottom {
      grid-template-columns: 1fr;
    }
  }
</style>
