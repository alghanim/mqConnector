<!--
  ScriptEditor — structured editor for `script` stages.

  Backend config:
    { script: string, timeout_ms?: number }

  UI:
    - Monospace <textarea> with a hand-rolled line-number gutter on the
      leading edge. We avoid pulling in a code-editor library — the
      gutter recomputes from `script.split('\n').length` on each
      keystroke and renders as a sibling <pre> aligned line-for-line.
    - Timeout-ms number input (default 5000, max 30000).
    - "Test on sample" button — emits a `test` custom event; the parent
      inspector forwards to Task 11's DryRunDock (no dispatch wiring in
      Task 10).
    - Advanced (raw JSON) <details> escape hatch.

  Validation:
    - script must be non-empty.
    - timeout_ms must be 1..30000.

  The line-number gutter uses --text-tertiary so it stays subtle on
  both themes. The textarea scrolls; the gutter scrolls in lockstep
  via a manual scroll-handler.
-->
<script lang="ts">
  import { createEventDispatcher, tick } from 'svelte';
  import { locale, t } from '$lib/stores/locale';
  import Input from '$lib/components/Input.svelte';
  import Button from '$lib/components/Button.svelte';

  export let config = '{}';
  export let valid = true;

  const dispatch = createEventDispatcher<{ test: { script: string; timeout_ms: number } }>();

  let script = '';
  let timeoutMs = 5000;
  let extras: Record<string, unknown> = {};
  let lastIn = '';
  let showAdvanced = false;

  let textareaEl: HTMLTextAreaElement | null = null;
  let gutterEl: HTMLPreElement | null = null;

  $: if (config !== lastIn) {
    parse(config);
    lastIn = config;
  }

  // Line count for the gutter. We always render at least one line so
  // the gutter doesn't collapse on an empty buffer.
  $: lineCount = Math.max(1, script.split('\n').length);

  function parse(s: string) {
    script = '';
    timeoutMs = 5000;
    extras = {};
    let raw: unknown;
    try {
      raw = JSON.parse(s || '{}');
    } catch {
      return;
    }
    if (typeof raw !== 'object' || raw === null) return;
    const obj = raw as Record<string, unknown>;
    if (typeof obj.script === 'string') script = obj.script;
    if (typeof obj.timeout_ms === 'number' && Number.isFinite(obj.timeout_ms)) {
      timeoutMs = obj.timeout_ms;
    }
    for (const [k, v] of Object.entries(obj)) {
      if (k !== 'script' && k !== 'timeout_ms') extras[k] = v;
    }
  }

  function validate() {
    if (!script || script.trim() === '') {
      valid = false;
      return;
    }
    if (
      typeof timeoutMs !== 'number' ||
      !Number.isFinite(timeoutMs) ||
      timeoutMs < 1 ||
      timeoutMs > 30000
    ) {
      valid = false;
      return;
    }
    valid = true;
  }

  // Keep the gutter scroll position in sync with the textarea so the
  // line numbers don't drift when the script is taller than the
  // visible area.
  function onScroll() {
    if (textareaEl && gutterEl) gutterEl.scrollTop = textareaEl.scrollTop;
  }

  // Textarea input handler — re-aligns the gutter scroll position
  // after the reactive watcher below has had a chance to commit.
  async function onScriptInput() {
    await tick();
    onScroll();
  }

  function onTest() {
    if (!valid) return;
    dispatch('test', { script, timeout_ms: timeoutMs });
  }

  function onAdvancedInput(e: Event) {
    const v = (e.target as HTMLTextAreaElement).value;
    config = v;
    lastIn = v;
    parse(v);
    validate();
  }

  // Reactive auto-commit. <Input> doesn't forward on:input from the
  // wrapped <input>, so we watch the bound values instead. lastSerialised
  // guards against parse() → field-set → re-emit cycles.
  let lastSerialised = '';
  $: {
    const _ = `${script}|${timeoutMs}`;
    const out: Record<string, unknown> = { ...extras, script };
    if (timeoutMs !== 5000) out.timeout_ms = timeoutMs;
    const ser = JSON.stringify(out);
    if (ser !== lastSerialised && ser !== lastIn) {
      lastSerialised = ser;
      config = ser;
      lastIn = ser;
      validate();
    } else if (ser !== lastSerialised) {
      lastSerialised = ser;
    }
  }

  parse(config);
  lastIn = config;
  lastSerialised = config;
  validate();
</script>

<div class="se">
  <label class="se-label" for="se-script">{t($locale, 'studio.editor.script.body')}</label>
  <div class="se-frame">
    <pre class="se-gutter" bind:this={gutterEl} aria-hidden="true">{Array.from(
      { length: lineCount },
      (_, i) => i + 1
    ).join('\n')}</pre>
    <textarea
      id="se-script"
      class="se-text"
      bind:this={textareaEl}
      bind:value={script}
      on:input={onScriptInput}
      on:scroll={onScroll}
      placeholder={t($locale, 'studio.editor.script.placeholder')}
      rows="8"
      spellcheck="false"
    ></textarea>
  </div>

  <Input
    type="number"
    bind:value={timeoutMs}
    label={t($locale, 'studio.editor.script.timeout')}
    error={!valid && (timeoutMs < 1 || timeoutMs > 30000)
      ? t($locale, 'studio.editor.script.error.timeout')
      : ''}
  />

  <p class="se-help">{t($locale, 'studio.editor.script.help')}</p>

  <div class="se-actions">
    <Button variant="outline" on:click={onTest} disabled={!valid}>
      {t($locale, 'studio.editor.script.test')}
    </Button>
  </div>

  {#if !script || script.trim() === ''}
    <p class="se-err">{t($locale, 'studio.editor.script.error.empty')}</p>
  {/if}

  <details bind:open={showAdvanced} class="se-adv">
    <summary>{t($locale, 'studio.editor.advanced')}</summary>
    <p class="se-adv-help">{t($locale, 'studio.editor.advanced.help')}</p>
    <textarea
      class="se-adv-text"
      rows="4"
      value={config}
      on:input={onAdvancedInput}
      spellcheck="false"
    ></textarea>
  </details>
</div>

<style>
  .se {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .se-label {
    display: block;
    font-size: 12px;
    color: var(--text-muted);
  }
  .se-frame {
    display: flex;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--bg);
    overflow: hidden;
  }
  .se-gutter {
    margin: 0;
    padding: 8px 6px 8px 10px;
    background: var(--surface);
    color: var(--text-tertiary);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 1.5;
    text-align: end;
    user-select: none;
    border-inline-end: 1px solid var(--border);
    min-inline-size: 32px;
    max-block-size: 240px;
    overflow: hidden;
    white-space: pre;
  }
  .se-text {
    flex: 1;
    min-inline-size: 0;
    padding: 8px 10px;
    border: none;
    background: var(--bg);
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 1.5;
    resize: vertical;
    max-block-size: 240px;
    outline: none;
  }
  .se-help {
    margin: 0;
    color: var(--text-muted);
    font-size: 12px;
  }
  .se-err {
    margin: 0;
    color: var(--danger);
    font-size: 12px;
  }
  .se-actions {
    display: flex;
    justify-content: flex-end;
  }
  .se-adv {
    margin-block-start: 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .se-adv summary {
    cursor: pointer;
    padding-block: 4px;
  }
  .se-adv-help {
    margin: 4px 0;
    color: var(--text-tertiary);
    font-size: 11px;
  }
  .se-adv-text {
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
