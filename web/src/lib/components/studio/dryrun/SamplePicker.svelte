<!--
  SamplePicker — the top row of the DryRunDock. Lets the operator pick
  a sample message to feed into the preview endpoint.

  Two tabs:

    Saved  — list of SAMPLE_FIXTURES (order.json, payment.xml) shipped
             with the front-end. Clicking "Use this" copies the body
             into the picker's value + emits 'change' so the dock can
             trigger a fresh dry-run if desired.
    Paste  — raw textarea + optional file upload (max 1 MiB) so an
             operator can drop in a production-shaped sample.

  Wave 2 will add a "Recent" tab (live-tail ring buffer) — deferred per
  plan §1.9. The tab strip uses an extensible {tabs[]} array so adding
  it later doesn't churn this file.

  Bound prop:
    value: string — current sample text. Two-way bind so the dock can
                    read it for the preview request body.

  Events:
    change → { detail: string } — fired whenever the value rotates
              (Saved card click, Paste textarea input, file upload).
              Keeps the dock decoupled from the picker's internals.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { SAMPLE_FIXTURES } from '$lib/sample-fixtures';
  import { locale, t } from '$lib/stores/locale';

  export let value = '';

  type Tab = 'saved' | 'paste';
  let tab: Tab = 'saved';

  // 1 MiB file upload cap — same threshold the back-end /preview applies
  // to its request body. Larger samples typically mean someone dropped
  // a database dump in by mistake; we reject early with an inline
  // notice so the operator knows.
  const MAX_FILE_BYTES = 1024 * 1024;

  let fileError: string | null = null;

  const dispatch = createEventDispatcher<{ change: string }>();

  function selectFixture(body: string) {
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
      // Don't keep the picked file on the input — re-selecting the same
      // file would otherwise not re-fire change.
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
      // Reset the input so the same file can be picked twice in a row.
      input.value = '';
    }
  }

  // Short preview for the saved-fixture cards. Strip leading whitespace
  // and clip to ~120 chars so the card stays compact.
  function previewSnippet(body: string): string {
    const clean = body.trim();
    return clean.length > 140 ? clean.slice(0, 140) + '…' : clean;
  }
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
      {#each SAMPLE_FIXTURES as fixture (fixture.label)}
        <div class="sample-picker-card">
          <div class="sample-picker-card-head">
            <span class="sample-picker-card-name">{fixture.label}</span>
            <button
              type="button"
              class="sample-picker-card-use"
              on:click={() => selectFixture(fixture.body)}
            >
              {t($locale, 'studio.dryrun.sample.useThis')}
            </button>
          </div>
          <pre class="sample-picker-card-preview">{previewSnippet(fixture.body)}</pre>
        </div>
      {/each}
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
    gap: 0.5rem;
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
  .sample-picker-saved {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(13rem, 1fr));
    gap: 0.5rem;
  }
  .sample-picker-card {
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    min-inline-size: 0;
  }
  .sample-picker-card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .sample-picker-card-name {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.75rem;
    color: var(--text);
    font-weight: 600;
  }
  .sample-picker-card-use {
    appearance: none;
    background: var(--surface-high);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding-block: 0.125rem;
    padding-inline: 0.5rem;
    font-size: 0.6875rem;
    font-weight: 600;
    cursor: pointer;
    transition: border-color 120ms, background 120ms;
  }
  .sample-picker-card-use:hover,
  .sample-picker-card-use:focus-visible {
    border-color: var(--accent);
    background: var(--surface-bright);
  }
  .sample-picker-card-preview {
    margin: 0;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.6875rem;
    color: var(--text-muted);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 0.375rem 0.5rem;
    max-block-size: 4.5rem;
    overflow: hidden;
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
    /* Visually hide the native control; the label is the affordance.
       Keep it focusable via tab so keyboard users reach it. */
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
