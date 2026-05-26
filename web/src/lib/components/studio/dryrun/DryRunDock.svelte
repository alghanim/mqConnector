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

  // Last-run status line — a tight one-liner that summarises the
  // most recent dry-run with per-stage timings, so the operator gets
  // signal without scrolling to inspect the StageOutcomeStrip.
  // We track when the result landed (Date.now() at the moment dryRun
  // rotated) and render a coarse relative time alongside the total +
  // per-stage durations.
  let lastRunAt = 0;
  let prevDryRunRef: PreviewResponse | null = null;
  $: if (dryRun && dryRun !== prevDryRunRef) {
    lastRunAt = Date.now();
    prevDryRunRef = dryRun;
  }
  $: totalMs = (() => {
    if (!runs.length) return 0;
    let sum = 0;
    for (const r of runs) sum += Math.round((r.duration_ns ?? 0) / 1_000_000);
    return sum;
  })();
  // 1s tick so the relative-time label refreshes without a polling
  // interval per dock instance. Cleared on destroy.
  let nowTick = Date.now();
  if (typeof window !== 'undefined') {
    const tickId = setInterval(() => (nowTick = Date.now()), 1000);
    onDestroy(() => clearInterval(tickId));
  }
  $: relativeAgo = (() => {
    if (!lastRunAt) return '';
    const diff = Math.max(0, nowTick - lastRunAt);
    const sec = Math.floor(diff / 1000);
    if (sec < 60) return `${sec}s ago`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ago`;
    const hr = Math.floor(min / 60);
    return `${hr}h ago`;
  })();
  // Latency bucket for the per-stage pill tone. Matches the existing
  // thresholds elsewhere in the dock (fast/normal/slow/danger).
  function latencyTone(ms: number): 'ok' | 'warn' | 'danger' {
    if (ms < 10) return 'ok';
    if (ms < 100) return 'warn';
    return 'danger';
  }

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
      {#if hasResult && lastRunAt > 0}
        <!-- Last-run status line — one compact row above the picker so
             the operator gets a quick "what just happened" summary
             without scrolling into the outcomes strip. -->
        <div class="dock-lastrun" role="status" aria-live="polite">
          <span class="dock-lastrun-label">{t($locale, 'studio.dryrun.lastRun.label')}</span>
          <span class="dock-lastrun-time">{relativeAgo}</span>
          <span class="dock-lastrun-dot" aria-hidden="true">·</span>
          <span class="dock-lastrun-total" data-tone={latencyTone(totalMs)}>{totalMs}ms</span>
          <span class="dock-lastrun-totalcap">{t($locale, 'studio.dryrun.lastRun.totalMs')}</span>
          <span class="dock-lastrun-stages">
            {#each runs as r (r.name)}
              {@const ms = Math.round((r.duration_ns ?? 0) / 1_000_000)}
              <span class="dock-lastrun-stage" data-failed={r.failed ? 'true' : 'false'} data-tone={latencyTone(ms)}>
                <span class="dock-lastrun-stage-mark" aria-hidden="true">
                  {r.failed ? '✗' : '✓'}
                </span>
                <span class="dock-lastrun-stage-name">{r.name}</span>
                <span class="dock-lastrun-stage-ms">{ms}ms</span>
              </span>
            {/each}
          </span>
        </div>
      {/if}
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

  /* Last-run summary — single inline strip with the most recent
     dry-run's metadata. Sits above the picker so it stays visible
     while the operator picks a new fixture. Pills follow the same
     latency-bucket tones the rest of the dock uses. */
  .dock-lastrun {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    flex-wrap: wrap;
    padding: 0.375rem 0.5rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    font-size: 0.6875rem;
    color: var(--text-muted);
  }
  .dock-lastrun-label {
    color: var(--text-tertiary);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 700;
  }
  .dock-lastrun-time {
    color: var(--text);
    font-weight: 600;
  }
  .dock-lastrun-dot {
    color: var(--text-tertiary);
  }
  .dock-lastrun-total {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
  }
  .dock-lastrun-total[data-tone='ok']     { color: var(--success); }
  .dock-lastrun-total[data-tone='warn']   { color: var(--warning); }
  .dock-lastrun-total[data-tone='danger'] { color: var(--danger); }
  .dock-lastrun-totalcap {
    color: var(--text-tertiary);
  }
  .dock-lastrun-stages {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    flex-wrap: wrap;
    margin-inline-start: 0.25rem;
  }
  .dock-lastrun-stage {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding-block: 0.0625rem;
    padding-inline: 0.375rem;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 999px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.625rem;
    color: var(--text);
  }
  .dock-lastrun-stage-mark {
    color: var(--success);
    font-weight: 700;
  }
  .dock-lastrun-stage[data-failed='true'] .dock-lastrun-stage-mark {
    color: var(--danger);
  }
  .dock-lastrun-stage-name {
    text-transform: capitalize;
  }
  .dock-lastrun-stage-ms {
    color: var(--text-muted);
  }
  .dock-lastrun-stage[data-tone='warn']   .dock-lastrun-stage-ms { color: var(--warning); }
  .dock-lastrun-stage[data-tone='danger'] .dock-lastrun-stage-ms { color: var(--danger); }
</style>
