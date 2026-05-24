<!--
  FilterEditor — structured editor for `filter` stages.

  Backend config: { paths: string[] }.

  UI:
    - Chip list of currently filtered paths, X-to-remove on each.
    - Inline text input + Add button to append a new path.
    - PathPicker popover for selecting from a sample (Task 11 wires the
      sample bytes through; for Task 10 the picker renders against any
      paths the parent inspector chooses to pass in via `samplePaths`).
    - Advanced (raw JSON) <details> escape hatch that preserves any
      forward-compat keys the typed form doesn't model (legacy
      StageConfigForm.svelte:66 + 181-185 patterns).

  The editor is two-way bound:
    - `bind:config` — JSON string. Reads it on every external change,
      writes back via JSON.stringify after each structured edit. Unknown
      keys round-trip via the `extras` bag.
    - `bind:valid` — true when every path is a non-empty string.

  No store calls in here: the parent inspector owns the patchStage call.
-->
<script lang="ts">
  import { tick } from 'svelte';
  import { locale, t } from '$lib/stores/locale';
  import Button from '$lib/components/Button.svelte';
  import PathPicker from './PathPicker.svelte';

  export let config = '{}';
  export let valid = true;
  /** Optional pre-extracted JSONPath strings for the PathPicker. */
  export let samplePaths: string[] = [];

  // View model. `extras` is the bag of keys we don't know about — kept
  // intact so a future-release `filter` config (e.g. `mode: "strict"`)
  // round-trips losslessly. Mirrors StageConfigForm.svelte:67-69.
  let paths: string[] = [];
  let extras: Record<string, unknown> = {};
  let pathBuf = '';
  let lastIn = '';
  let showAdvanced = false;

  // Parse on first mount + any time the parent supplies a different
  // config string (stage switch). Local edits commit back via commit()
  // which sets lastIn = config so this branch doesn't re-trigger.
  $: if (config !== lastIn) {
    parse(config);
    lastIn = config;
  }

  function parse(s: string) {
    paths = [];
    extras = {};
    let raw: unknown;
    try {
      raw = JSON.parse(s || '{}');
    } catch {
      // Malformed JSON — leave the view at defaults. The Advanced
      // escape hatch lets the operator fix it.
      return;
    }
    if (typeof raw !== 'object' || raw === null) return;
    const obj = raw as Record<string, unknown>;
    if (Array.isArray(obj.paths)) {
      paths = obj.paths.filter((p): p is string => typeof p === 'string');
    }
    for (const [k, v] of Object.entries(obj)) {
      if (k !== 'paths') extras[k] = v;
    }
  }

  function commit() {
    const out: Record<string, unknown> = { ...extras, paths };
    config = JSON.stringify(out);
    lastIn = config;
    validate();
  }

  function validate() {
    // Every path must be a non-empty string. Empty list is fine — it
    // means "let everything through" (the runtime treats that as a
    // no-op stage). Matches the backend contract.
    valid = paths.every((p) => typeof p === 'string' && p.trim().length > 0);
  }

  function addPath() {
    const p = pathBuf.trim();
    if (!p) return;
    if (!paths.includes(p)) paths = [...paths, p];
    pathBuf = '';
    commit();
  }

  function removePath(p: string) {
    paths = paths.filter((x) => x !== p);
    commit();
  }

  async function onPathKey(e: KeyboardEvent) {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault();
      addPath();
      await tick();
    }
  }

  function onPickPath(e: CustomEvent<string>) {
    const p = e.detail;
    if (!p || paths.includes(p)) return;
    paths = [...paths, p];
    commit();
  }

  // Advanced JSON escape hatch — edits flow straight to `config` without
  // going through the structured view, but we still re-parse so a return
  // visit to the structured pane reflects the manual change.
  function onAdvancedInput(e: Event) {
    const v = (e.target as HTMLTextAreaElement).value;
    config = v;
    lastIn = v;
    parse(v);
    validate();
  }

  // Initial validate so the parent sees the right `valid` on first
  // render even before any edits.
  parse(config);
  lastIn = config;
  validate();
</script>

<div class="fe">
  <p class="fe-help">{t($locale, 'studio.editor.filter.help')}</p>

  {#if paths.length === 0}
    <p class="fe-empty">{t($locale, 'studio.editor.filter.empty')}</p>
  {:else}
    <ul class="fe-chips" aria-label={t($locale, 'studio.editor.filter.heading')}>
      {#each paths as p (p)}
        <li class="fe-chip">
          <span class="fe-chip-text">{p}</span>
          <button
            type="button"
            class="fe-chip-x"
            aria-label={t($locale, 'studio.editor.filter.remove')}
            on:click={() => removePath(p)}
          >×</button>
        </li>
      {/each}
    </ul>
  {/if}

  <div class="fe-row">
    <input
      type="text"
      class="fe-input"
      placeholder={t($locale, 'studio.editor.filter.placeholder')}
      bind:value={pathBuf}
      on:keydown={onPathKey}
      aria-label={t($locale, 'studio.editor.filter.add')}
    />
    <Button variant="primary" on:click={addPath}>
      {t($locale, 'studio.editor.filter.add')}
    </Button>
    <PathPicker
      paths={samplePaths}
      value=""
      label={t($locale, 'studio.editor.filter.pick')}
      on:pick={onPickPath}
    />
  </div>

  <details bind:open={showAdvanced} class="fe-adv">
    <summary>{t($locale, 'studio.editor.advanced')}</summary>
    <p class="fe-adv-help">{t($locale, 'studio.editor.advanced.help')}</p>
    <textarea
      class="fe-adv-text"
      rows="4"
      value={config}
      on:input={onAdvancedInput}
      spellcheck="false"
    ></textarea>
  </details>
</div>

<style>
  .fe {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .fe-help {
    margin: 0;
    color: var(--text-muted);
    font-size: 12px;
  }
  .fe-empty {
    margin: 0;
    color: var(--text-tertiary);
    font-size: 12px;
    font-style: italic;
  }
  .fe-chips {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .fe-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding-block: 2px;
    padding-inline-start: 10px;
    padding-inline-end: 4px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface);
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
  }
  .fe-chip-text { word-break: break-all; }
  .fe-chip-x {
    background: transparent;
    border: none;
    cursor: pointer;
    color: var(--text-muted);
    inline-size: 20px;
    block-size: 20px;
    border-radius: 50%;
    line-height: 1;
  }
  .fe-chip-x:hover {
    background: var(--danger);
    color: var(--danger-on);
  }
  .fe-row {
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .fe-input {
    flex: 1;
    min-inline-size: 0;
    background: var(--bg);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 8px 10px;
    font-size: 13px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }
  .fe-adv {
    margin-block-start: 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .fe-adv summary {
    cursor: pointer;
    padding-block: 4px;
  }
  .fe-adv-help {
    margin: 4px 0;
    color: var(--text-tertiary);
    font-size: 11px;
  }
  .fe-adv-text {
    inline-size: 100%;
    background: var(--bg);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 8px 10px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    resize: vertical;
  }
</style>
