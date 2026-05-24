<!--
  TransformEditor — wraps TransformListEditor inside the studio
  inspector for the per-stage UX.

  Why a wrapper instead of re-authoring: the transform list is
  pipeline-scoped (one list per pipeline, regardless of how many
  `transform` stages exist). The studio's notion of a `transform` stage
  is just a marker in the chain that says "the pipeline transform list
  fires here". Re-authoring the row UI risks drift between the visual
  editor (/flow) and the form editor (/pipelines/[id]) which already
  share TransformListEditor. So this wrapper just:

    - Hosts a 2-way `bind:config` so the inspector's per-stage contract
      matches the other six editors. The per-stage config carries no
      meaningful fields today; we preserve any future-release keys via
      the same `extras` bag every editor uses.
    - Surfaces the help string explaining that the rules below edit the
      pipeline-wide list (so an operator who selects the stage doesn't
      see an empty pane and wonder where the rules went).
    - Delegates row UX (type dropdown, source/target paths, drag-handle
      reorder, delete) to TransformListEditor via bind:transforms.

  Validation: each row already requires a non-empty source_path inside
  TransformListEditor; we surface a single `valid` flag here that's
  true when every row's required fields per its transform_type are
  populated. Empty list is valid.
-->
<script lang="ts">
  import type { Transform } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import TransformListEditor from '$lib/components/TransformListEditor.svelte';

  export let config = '{}';
  export let valid = true;
  /** Pipeline-scoped transform list. Two-way bound to the parent. */
  export let transforms: Transform[] = [];

  // Per-stage config bag. The per-stage config for transform stages
  // carries no meaningful fields today, so the only data we preserve
  // is the extras bag (forward-compat).
  let extras: Record<string, unknown> = {};
  let lastIn = '';
  let showAdvanced = false;

  $: if (config !== lastIn) {
    parse(config);
    lastIn = config;
  }

  function parse(s: string) {
    extras = {};
    let raw: unknown;
    try {
      raw = JSON.parse(s || '{}');
    } catch {
      return;
    }
    if (typeof raw !== 'object' || raw === null) return;
    for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
      extras[k] = v;
    }
  }

  // Commit not needed for fields we don't model — but we still re-emit
  // `config` whenever extras changes so the bound parent doesn't miss
  // edits from the advanced pane.
  function commit() {
    config = JSON.stringify({ ...extras });
    lastIn = config;
    validate();
  }

  // Re-validate the entire transform list. Each row must have a
  // non-empty source_path; row types with target_path (rename/move)
  // need a non-empty target; mask needs pattern; set needs a value.
  function validate() {
    valid = transforms.every((tr) => {
      if (!tr.source_path || tr.source_path.trim() === '') return false;
      if (tr.transform_type === 'rename' || tr.transform_type === 'move') {
        return !!tr.target_path && tr.target_path.trim() !== '';
      }
      if (tr.transform_type === 'mask') {
        return !!tr.mask_pattern && tr.mask_pattern.trim() !== '';
      }
      if (tr.transform_type === 'set') {
        // Set's empty string is technically allowed (set field to "")
        // — we treat that as valid because the wire format permits it.
        return true;
      }
      return true;
    });
  }

  // Re-validate whenever the list mutates.
  $: {
    transforms;
    validate();
  }

  function onAdvancedInput(e: Event) {
    const v = (e.target as HTMLTextAreaElement).value;
    config = v;
    lastIn = v;
    parse(v);
    validate();
  }

  parse(config);
  lastIn = config;
  validate();
</script>

<div class="te">
  <p class="te-help">{t($locale, 'studio.editor.transform.help')}</p>
  <TransformListEditor bind:transforms compact />

  <details bind:open={showAdvanced} class="te-adv">
    <summary>{t($locale, 'studio.editor.advanced')}</summary>
    <p class="te-adv-help">{t($locale, 'studio.editor.advanced.help')}</p>
    <textarea
      class="te-adv-text"
      rows="4"
      value={config}
      on:input={onAdvancedInput}
      spellcheck="false"
    ></textarea>
  </details>
</div>

<style>
  .te {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .te-help {
    margin: 0;
    color: var(--text-muted);
    font-size: 12px;
  }
  .te-adv {
    margin-block-start: 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .te-adv summary {
    cursor: pointer;
    padding-block: 4px;
  }
  .te-adv-help {
    margin: 4px 0;
    color: var(--text-tertiary);
    font-size: 11px;
  }
  .te-adv-text {
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
