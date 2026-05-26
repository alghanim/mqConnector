<!--
  SamplePicker — the top row of the DryRunDock. Lets the operator pick
  a sample message to feed into the preview endpoint.

  Two tabs:

    Saved  — horizontal chip strip with one entry per SAMPLE_FIXTURES
             item. Clicking a chip selects the fixture and updates the
             dock's value. A collapsible "Preview" block lets the
             operator inspect the selected sample without burning ~200
             px of vertical space on every fixture by default.
    Paste  — raw textarea + optional file upload (max 1 MiB) so an
             operator can drop in a production-shaped sample.

  Bound prop:
    value: string — current sample text. Two-way bind so the dock can
                    read it for the preview request body.

  Events:
    change → { detail: string } — fired whenever the value rotates
              (Saved chip click, Paste textarea input, file upload).
              Keeps the dock decoupled from the picker's internals.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { SAMPLE_FIXTURES } from '$lib/sample-fixtures';
  import { locale, t } from '$lib/stores/locale';
  import { ChevronDown, ChevronRight } from 'lucide-svelte';

  export let value = '';

  type Tab = 'saved' | 'paste';
  let tab: Tab = 'saved';

  const MAX_FILE_BYTES = 1024 * 1024;

  let fileError: string | null = null;

  // Which fixture is currently selected (by label). Persisted across
  // re-renders so the chip + preview pair stays in sync. Defaults to
  // null — the operator picks explicitly.
  let selectedFixture: string | null = null;
  // Whether the preview body for the currently selected fixture is
  // expanded. Collapsed by default so the dock stays compact; the
  // operator opts in to see the sample's contents.
  let previewOpen = false;

  const dispatch = createEventDispatcher<{ change: string }>();

  function selectFixture(label: string, body: string) {
    selectedFixture = label;
    value = body;
    fileError = null;
    dispatch('change', value);
  }

  function onPasteInput(e: Event) {
    const next = (e.target as HTMLTextAreaElement).value;
    value = next;
    fileError = null;
    dispatch('change', value);
  }

  async function onFileChange(e: Event) {
    const input = e.target as HTMLInputElement;
    const f = input.files?.[0];
    if (!f) return;
    if (f.size > MAX_FILE_BYTES) {
      fileError = t($locale, 'studio.dryrun.sample.fileTooLarge');
      input.value = '';
      return;
    }
    try {
      const txt = await f.text();
      value = txt;
      fileError = null;
      dispatch('change', value);
    } catch {
      fileError = t($locale, 'studio.dryrun.sample.fileReadFailed');
    } finally {
      input.value = '';
    }
  }

  // Format hint for a fixture name — the extension drives the badge.
  function fmt(label: string): string {
    const dot = label.lastIndexOf('.');
    if (dot < 0) return '';
    return label.slice(dot + 1).toLowerCase();
  }

  // Byte size of a string, approximated using TextEncoder where it's
  // available (every browser the app targets). Falls back to .length —
  // accurate for ASCII, slightly under-counts for multibyte text, but
  // good enough for a "how big is this sample" hint.
  function byteSize(s: string): number {
    if (typeof TextEncoder !== 'undefined') {
      return new TextEncoder().encode(s).length;
    }
    return s.length;
  }

  // The currently picked fixture body (for the preview disclosure).
  // We re-resolve from the fixture catalogue so a later edit to the
  // catalogue propagates without the picker holding a stale snapshot.
  $: pickedFixture = SAMPLE_FIXTURES.find((f) => f.label === selectedFixture) ?? null;
  $: pickedBytes = pickedFixture ? byteSize(pickedFixture.body) : 0;
</script>

<div class="sample-picker" aria-label={t($locale, 'studio.dryrun.sample.heading')}>
  <div class="sample-picker-tabs" role="tablist">
    <button
      type="button"
      role="tab"
      class="sample-picker-tab"
      class:is-active={tab === 'saved'}
      aria-selected={tab === 'saved'}
      on:click={() => (tab = 'saved')}
    >
      {t($locale, 'studio.dryrun.sample.tab.saved')}
    </button>
    <button
      type="button"
      role="tab"
      class="sample-picker-tab"
      class:is-active={tab === 'paste'}
      aria-selected={tab === 'paste'}
      on:click={() => (tab = 'paste')}
    >
      {t($locale, 'studio.dryrun.sample.tab.paste')}
    </button>
  </div>

  {#if tab === 'saved'}
    <div class="sample-picker-saved" role="tabpanel" aria-label={t($locale, 'studio.dryrun.sample.tab.saved')}>
      <!-- Compact chip strip — one entry per saved fixture. Selecting
           a chip loads the sample and reveals the meta + preview row
           below. Keyboard parity via native button semantics. -->
      <div class="sample-picker-chipstrip" role="group" aria-label="Saved fixtures">
        {#each SAMPLE_FIXTURES as fixture (fixture.label)}
          {@const isSelected = selectedFixture === fixture.label}
          <button
            type="button"
            class="sample-picker-chip"
            class:is-selected={isSelected}
            aria-pressed={isSelected}
            on:click={() => selectFixture(fixture.label, fixture.body)}
          >
            <span class="sample-picker-chip-name">{fixture.label}</span>
            {#if fmt(fixture.label)}
              <span class="sample-picker-chip-fmt">{fmt(fixture.label)}</span>
            {/if}
          </button>
        {/each}
      </div>

      {#if pickedFixture}
        <div class="sample-picker-meta">
          <span class="sample-picker-meta-name">{pickedFixture.label}</span>
          <span class="sample-picker-meta-dot" aria-hidden="true">·</span>
          <span class="sample-picker-meta-size">
            {pickedBytes} {t($locale, 'studio.dryrun.sample.bytes')}
          </span>
          <button
            type="button"
            class="sample-picker-disclose"
            aria-expanded={previewOpen}
            on:click={() => (previewOpen = !previewOpen)}
          >
            {#if previewOpen}
              <ChevronDown size={12} aria-hidden="true" />
            {:else}
              <ChevronRight size={12} aria-hidden="true" />
            {/if}
            {t($locale, 'studio.dryrun.sample.preview')}
          </button>
        </div>
        {#if previewOpen}
          <pre class="sample-picker-preview">{pickedFixture.body}</pre>
        {/if}
      {/if}
    </div>
  {:else}
    <div class="sample-picker-paste" role="tabpanel" aria-label={t($locale, 'studio.dryrun.sample.tab.paste')}>
      <textarea
        class="sample-picker-textarea"
        aria-label={t($locale, 'studio.dryrun.sample.pastePlaceholder')}
        placeholder={t($locale, 'studio.dryrun.sample.pastePlaceholder')}
        spellcheck="false"
        rows="6"
        value={value}
        on:input={onPasteInput}
      ></textarea>
      <div class="sample-picker-paste-actions">
        <label class="sample-picker-file">
          <input
            type="file"
            accept=".json,.xml,.txt,application/json,text/xml,text/plain"
            on:change={onFileChange}
          />
          <span>{t($locale, 'studio.dryrun.sample.upload')}</span>
        </label>
        {#if fileError}
          <span class="sample-picker-error" role="alert">{fileError}</span>
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .sample-picker {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    min-inline-size: 0;
  }
  .sample-picker-tabs {
    display: flex;
    gap: 0.25rem;
  }
  .sample-picker-tab {
    appearance: none;
    background: transparent;
    border: 1px solid transparent;
    color: var(--text-muted);
    padding-block: 0.25rem;
    padding-inline: 0.625rem;
    border-radius: 8px 8px 0 0;
    font-size: 0.75rem;
    font-weight: 600;
    cursor: pointer;
    transition: color 120ms, background 120ms, border-color 120ms;
  }
  .sample-picker-tab:hover {
    color: var(--text);
    background: var(--surface-high);
  }
  .sample-picker-tab.is-active {
    color: var(--text);
    background: var(--surface-2);
    border-color: var(--border);
    border-block-end-color: var(--surface-2);
  }

  /* Saved tab — compact horizontal chip strip. */
  .sample-picker-saved {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }
  .sample-picker-chipstrip {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }
  .sample-picker-chip {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding-block: 0.25rem;
    padding-inline: 0.5rem 0.375rem;
    background: transparent;
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 999px;
    font-size: 0.75rem;
    font-weight: 600;
    cursor: pointer;
    transition: background 120ms, border-color 120ms, color 120ms;
  }
  .sample-picker-chip:hover,
  .sample-picker-chip:focus-visible {
    border-color: var(--primary);
    color: var(--primary);
    outline: none;
  }
  .sample-picker-chip.is-selected {
    background: var(--primary-container);
    border-color: var(--primary);
    color: var(--on-primary-container);
  }
  .sample-picker-chip-name {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }
  .sample-picker-chip-fmt {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.625rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    padding-block: 0.0625rem;
    padding-inline: 0.25rem;
    background: var(--surface-high);
    color: var(--text-muted);
    border-radius: 4px;
  }
  .sample-picker-chip.is-selected .sample-picker-chip-fmt {
    background: var(--primary);
    color: var(--primary-on);
  }

  /* Selection meta line + preview disclosure. */
  .sample-picker-meta {
    display: inline-flex;
    align-items: baseline;
    gap: 0.375rem;
    font-size: 0.6875rem;
    color: var(--text-muted);
  }
  .sample-picker-meta-name {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    color: var(--text);
    font-weight: 600;
  }
  .sample-picker-meta-dot {
    color: var(--text-tertiary);
  }
  .sample-picker-disclose {
    margin-inline-start: auto;
    appearance: none;
    background: transparent;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.6875rem;
    font-weight: 600;
    padding-block: 0.125rem;
    padding-inline: 0.25rem;
  }
  .sample-picker-disclose:hover,
  .sample-picker-disclose:focus-visible {
    color: var(--text);
    outline: none;
  }
  .sample-picker-preview {
    margin: 0;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.6875rem;
    color: var(--text-muted);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 0.375rem 0.5rem;
    max-block-size: 8rem;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
  }

  .sample-picker-paste {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }
  .sample-picker-textarea {
    inline-size: 100%;
    min-block-size: 6rem;
    max-block-size: 12rem;
    padding: 0.5rem 0.625rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg);
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.75rem;
    line-height: 1.45;
    resize: vertical;
  }
  .sample-picker-textarea:focus {
    outline: none;
    border-color: var(--accent);
  }
  .sample-picker-paste-actions {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
  }
  .sample-picker-file {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding-block: 0.25rem;
    padding-inline: 0.5rem;
    background: var(--surface-high);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 6px;
    font-size: 0.6875rem;
    font-weight: 600;
    cursor: pointer;
  }
  .sample-picker-file input[type='file'] {
    position: absolute;
    inline-size: 1px;
    block-size: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
  .sample-picker-file:hover,
  .sample-picker-file:focus-within {
    border-color: var(--accent);
  }
  .sample-picker-error {
    color: var(--danger);
    font-size: 0.75rem;
  }
</style>
