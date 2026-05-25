<!--
  StudioPalette — the drag source for the seven stage types the studio
  can place onto the canvas.

  Two interactions, parity per spec:
    1. Drag a card onto the canvas. The card sets the
       `application/x-mqc-stage-type` mime payload so the canvas can read
       it back in its `on:drop` handler.
    2. Click a card. Emits `select` with the stage type so the parent
       (Studio.svelte) can append it via `studio.addStage(...)`.

  Icons are inline SVG authored here — no third-party icon library. Each
  icon is a small geometric shape that hints at the stage's behaviour
  (cone for filter, fork for route, etc.). The icon size + stroke read
  off brand tokens so dark + light themes look identical.

  Task 9 / Wave 1.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { locale, t } from '$lib/stores/locale';
  import type { StudioStageType } from '$lib/stores/studio';

  const dispatch = createEventDispatcher<{ select: StudioStageType }>();

  // The full set of stage types the palette exposes. Order is the
  // recommended insertion order for a typical pipeline (filter early,
  // route late). Keep this list in sync with `StudioStageType` in
  // studio.ts — every type must have a palette card.
  const STAGE_TYPES: StudioStageType[] = [
    'filter',
    'transform',
    'translate',
    'validate',
    'route',
    'script',
    'wasm'
  ];

  // mimeForDrag is the canvas's drag-and-drop contract. Anything that
  // wants to programmatically place a stage on the canvas needs to set
  // this exact key. Keep in sync with StudioCanvas.svelte.
  export const STAGE_DRAG_MIME = 'application/x-mqc-stage-type';

  function onDragStart(e: DragEvent, stageType: StudioStageType) {
    if (!e.dataTransfer) return;
    e.dataTransfer.setData(STAGE_DRAG_MIME, stageType);
    e.dataTransfer.effectAllowed = 'copy';
  }

  function onClick(stageType: StudioStageType) {
    dispatch('select', stageType);
  }

  function onKeyDown(e: KeyboardEvent, stageType: StudioStageType) {
    // Space + Enter activate — same affordance the rest of the app uses
    // for button-shaped role=button divs (matches our Switch + Button
    // pattern). The user can tab through and pick a stage without ever
    // touching a mouse.
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onClick(stageType);
    }
  }
</script>

<section class="studio-palette" aria-label={t($locale, 'studio.palette.heading')}>
  <header class="studio-palette-head">
    <h2 class="studio-palette-title">{t($locale, 'studio.palette.heading')}</h2>
    <p class="studio-palette-help">{t($locale, 'studio.palette.help')}</p>
  </header>

  <ul class="studio-palette-list" role="list">
    {#each STAGE_TYPES as stageType (stageType)}
      <li class="studio-palette-li">
        <div
          class="studio-palette-card"
          data-tone={stageType}
          role="button"
          tabindex="0"
          draggable="true"
          data-stage-type={stageType}
          aria-label={t($locale, `studio.palette.stage.${stageType}.label`)}
          title={t($locale, `studio.palette.stage.${stageType}.help`)}
          on:dragstart={(e) => onDragStart(e, stageType)}
          on:click={() => onClick(stageType)}
          on:keydown={(e) => onKeyDown(e, stageType)}
        >
          <span class="studio-palette-accent" aria-hidden="true"></span>
          <span class="studio-palette-icon" aria-hidden="true">
            <!--
              Inline SVG glyph per stage type. 16×16 viewBox, currentColor
              stroke so theme switching is automatic. Each shape is
              hand-rolled (no icon lib per spec).
            -->
            {#if stageType === 'filter'}
              <svg viewBox="0 0 16 16" width="16" height="16" fill="none">
                <path
                  d="M2 3h12l-4.5 5.5V13l-3 1V8.5L2 3z"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linejoin="round"
                />
              </svg>
            {:else if stageType === 'transform'}
              <svg viewBox="0 0 16 16" width="16" height="16" fill="none">
                <path
                  d="M3 5l3-3 3 3M6 2v8M13 11l-3 3-3-3M10 14V6"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                />
              </svg>
            {:else if stageType === 'translate'}
              <svg viewBox="0 0 16 16" width="16" height="16" fill="none">
                <path
                  d="M2 4h6M5 2v2M3 4c0 3 2 5 4 5M7 9c-1 1-3 2-5 2"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linecap="round"
                />
                <path
                  d="M9 14l3-7 3 7M10 12h4"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                />
              </svg>
            {:else if stageType === 'validate'}
              <svg viewBox="0 0 16 16" width="16" height="16" fill="none">
                <path
                  d="M8 1l5 2v4c0 4-3 7-5 8-2-1-5-4-5-8V3l5-2z"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linejoin="round"
                />
                <path d="M5.5 8l2 2 3-4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
            {:else if stageType === 'route'}
              <svg viewBox="0 0 16 16" width="16" height="16" fill="none">
                <path
                  d="M3 14V8a3 3 0 013-3h4a3 3 0 013 3v6"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linecap="round"
                />
                <circle cx="8" cy="3" r="1.5" stroke="currentColor" stroke-width="1.4" />
                <circle cx="3" cy="14" r="1.2" stroke="currentColor" stroke-width="1.4" />
                <circle cx="13" cy="14" r="1.2" stroke="currentColor" stroke-width="1.4" />
              </svg>
            {:else if stageType === 'script'}
              <svg viewBox="0 0 16 16" width="16" height="16" fill="none">
                <path
                  d="M5 4L2 8l3 4M11 4l3 4-3 4M9 3l-2 10"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                />
              </svg>
            {:else if stageType === 'wasm'}
              <svg viewBox="0 0 16 16" width="16" height="16" fill="none">
                <rect
                  x="2"
                  y="3"
                  width="12"
                  height="10"
                  rx="2"
                  stroke="currentColor"
                  stroke-width="1.4"
                />
                <path
                  d="M4.5 9l1 2 1-3 1 3 1-2M9.5 9l1 2 1-3"
                  stroke="currentColor"
                  stroke-width="1.2"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                />
              </svg>
            {/if}
          </span>
          <div class="studio-palette-text">
            <span class="studio-palette-row">
              <span class="studio-palette-label">
                {t($locale, `studio.palette.stage.${stageType}.label`)}
              </span>
              <span class="studio-palette-slug" aria-hidden="true">{stageType}</span>
            </span>
            <span class="studio-palette-desc">
              {t($locale, `studio.palette.stage.${stageType}.help`)}
            </span>
          </div>
        </div>
      </li>
    {/each}
  </ul>
</section>

<style>
  /*
   * The palette is a left-rail vertical list of seven cards. Cards are
   * draggable; the cursor goes to `grab` on hover and `grabbing` while
   * the drag is active. Both light + dark themes read entirely from
   * brand tokens — no raw hex.
   */
  .studio-palette {
    display: flex;
    flex-direction: column;
    gap: 0.625rem;
    block-size: 100%;
    overflow-y: auto;
    padding-inline: 0.125rem;
    padding-block: 0.125rem;
  }
  .studio-palette-head {
    padding-block-end: 0.25rem;
    border-block-end: 1px solid var(--border);
  }
  .studio-palette-title {
    margin: 0;
    font-size: 0.6875rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-tertiary);
  }
  .studio-palette-help {
    margin: 0;
    margin-block-start: 0.25rem;
    color: var(--text-muted);
    font-size: 0.6875rem;
    line-height: 1.4;
  }
  .studio-palette-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }
  /* Per-stage accent colour. Each stage type gets a left-edge strip
     in a brand-token colour so the palette reads as a typed legend
     rather than a generic list. The accent tones are CSS variables
     so the icon tile + the strip can share one source. The maroon
     accent on `wasm` is the documented exception per spec — wasm is a
     primary/notable affordance and pulling --accent here flags that.

     Tokens used (single source of truth in brand-tokens.css):
       filter    → --info     (cyan — predicate/check vibe)
       transform → --primary  (gold — workhorse stage)
       translate → --info     (cyan — protocol/format conversion)
       route     → --secondary (copper / deeper gold — branching/edge)
       script    → --warning  (amber — caution: arbitrary code)
       validate  → --success  (green — passes/fails)
       wasm      → --accent   (maroon — primary plugin extension) */
  .studio-palette-card {
    position: relative;
    display: flex;
    align-items: flex-start;
    gap: 0.5rem;
    padding: 0.5rem 0.625rem 0.5rem 0.75rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 10px;
    color: var(--text);
    cursor: grab;
    user-select: none;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.04);
    transition:
      border-color 150ms ease,
      background-color 150ms ease,
      box-shadow 150ms ease,
      transform 150ms ease;
    --stage-tone: var(--primary);
  }
  .studio-palette-card[data-tone='filter']    { --stage-tone: var(--info); }
  .studio-palette-card[data-tone='transform'] { --stage-tone: var(--primary); }
  .studio-palette-card[data-tone='translate'] { --stage-tone: var(--info); }
  .studio-palette-card[data-tone='route']     { --stage-tone: var(--secondary); }
  .studio-palette-card[data-tone='script']    { --stage-tone: var(--warning); }
  .studio-palette-card[data-tone='validate']  { --stage-tone: var(--success); }
  .studio-palette-card[data-tone='wasm']      { --stage-tone: var(--accent); }

  /* Left-edge accent strip — 3px default, widens to 4px on hover for
     a tactile "this card is alive" cue. */
  .studio-palette-accent {
    position: absolute;
    inset-block: 4px;
    inset-inline-start: 0;
    inline-size: 3px;
    border-radius: 4px;
    background: var(--stage-tone);
    transition: inline-size 150ms ease, opacity 150ms ease;
    opacity: 0.85;
  }

  @media (prefers-reduced-motion: no-preference) {
    .studio-palette-card:hover,
    .studio-palette-card:focus-visible {
      transform: translateY(-1px);
    }
  }
  .studio-palette-card:hover,
  .studio-palette-card:focus-visible {
    border-color: var(--stage-tone);
    background: var(--surface-high);
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
    outline: none;
  }
  .studio-palette-card:hover .studio-palette-accent,
  .studio-palette-card:focus-visible .studio-palette-accent {
    inline-size: 4px;
    opacity: 1;
  }
  .studio-palette-card:active {
    cursor: grabbing;
    transform: translateY(0);
  }

  .studio-palette-icon {
    inline-size: 26px;
    block-size: 26px;
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: 6px;
    background: var(--surface-high);
    color: var(--stage-tone);
  }
  .studio-palette-text {
    min-inline-size: 0;
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 0.125rem;
  }
  .studio-palette-row {
    display: inline-flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.375rem;
    min-inline-size: 0;
  }
  .studio-palette-label {
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--text);
    line-height: 1.15;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /* Mono slug — short, lowercased, in tertiary text. Reads as a
     developer-facing tag without competing with the human label. */
  .studio-palette-slug {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.625rem;
    color: var(--text-tertiary);
    text-transform: lowercase;
    letter-spacing: 0.02em;
    flex: 0 0 auto;
  }
  .studio-palette-desc {
    font-size: 0.6875rem;
    color: var(--text-muted);
    line-height: 1.3;
    overflow: hidden;
    text-overflow: ellipsis;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
  }
</style>
