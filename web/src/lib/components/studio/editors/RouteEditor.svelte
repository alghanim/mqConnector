<!--
  RouteEditor — wraps RoutingRuleListEditor inside the studio inspector.

  Same rationale as TransformEditor: routing rules are pipeline-scoped
  (one list per pipeline) and the runtime treats a `route` stage as a
  marker that says "the rules fire here". Re-authoring the rule UI
  would risk drift from /flow + /pipelines/[id].

  This wrapper adds:
    - 2-way `bind:config` for the per-stage config blob (today empty;
      future-release keys round-trip via `extras`).
    - A regex-pattern tester. RoutingRuleListEditor currently exposes a
      plain text input for `condition_value`; for `regex` rules the
      operator wants to verify their pattern matches a sample before
      saving. We render a small "Test pattern" button + popover below
      the wrapped list editor so the existing editor stays untouched
      (Wave 2 will likely fold this into the row UI itself).

  Validation: each row needs condition_path + operator (+ value unless
  operator='exists') + destination. Surfaced through `valid`.
-->
<script lang="ts">
  import type { Connection, RoutingRule } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import RoutingRuleListEditor from '$lib/components/RoutingRuleListEditor.svelte';

  export let config = '{}';
  export let valid = true;
  /** Pipeline-scoped routing rules. Two-way bound. */
  export let rules: RoutingRule[] = [];
  /** Connections for the destination dropdown. */
  export let connections: Connection[] = [];

  let extras: Record<string, unknown> = {};
  let lastIn = '';
  let showAdvanced = false;

  // Regex tester state
  let testerOpen = false;
  let pattern = '';
  let sampleVal = '';
  $: testResult = runTest(pattern, sampleVal);

  function runTest(
    p: string,
    s: string
  ): 'idle' | 'match' | 'noMatch' | 'invalid' {
    if (!p) return 'idle';
    try {
      const re = new RegExp(p);
      return re.test(s) ? 'match' : 'noMatch';
    } catch {
      return 'invalid';
    }
  }

  function openTester() {
    const first = rules.find((r) => r.condition_operator === 'regex');
    pattern = first?.condition_value || '';
    sampleVal = '';
    testerOpen = true;
  }

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

  function commit() {
    config = JSON.stringify({ ...extras });
    lastIn = config;
    validate();
  }

  function validate() {
    valid = rules.every((r) => {
      if (!r.condition_path || r.condition_path.trim() === '') return false;
      if (!r.condition_operator) return false;
      if (
        r.condition_operator !== 'exists' &&
        (!r.condition_value || r.condition_value.toString().trim() === '')
      ) {
        return false;
      }
      if (!r.destination_id) return false;
      return true;
    });
  }

  $: {
    rules;
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

<div class="re">
  <p class="re-help">{t($locale, 'studio.editor.route.help')}</p>

  <RoutingRuleListEditor bind:rules {connections} compact />

  <div class="re-tools">
    <Button variant="ghost" on:click={openTester}>
      {t($locale, 'studio.editor.route.testRegex')}
    </Button>
  </div>

  <details bind:open={showAdvanced} class="re-adv">
    <summary>{t($locale, 'studio.editor.advanced')}</summary>
    <p class="re-adv-help">{t($locale, 'studio.editor.advanced.help')}</p>
    <textarea
      class="re-adv-text"
      rows="4"
      value={config}
      on:input={onAdvancedInput}
      spellcheck="false"
    ></textarea>
  </details>
</div>

<Dialog
  open={testerOpen}
  title={t($locale, 'studio.editor.route.testRegex.title')}
  cancelLabel={t($locale, 'common.cancel')}
  confirmLabel={t($locale, 'common.cancel')}
  on:cancel={() => (testerOpen = false)}
  on:confirm={() => (testerOpen = false)}
>
  <label class="re-label" for="re-pattern">{t($locale, 'pipelines.routing.value')}</label>
  <input id="re-pattern" type="text" class="re-text" bind:value={pattern} />
  <label class="re-label" for="re-sample">
    {t($locale, 'studio.editor.route.testRegex.sample')}
  </label>
  <input id="re-sample" type="text" class="re-text" bind:value={sampleVal} />
  {#if testResult === 'match'}
    <p class="re-out re-match">{t($locale, 'studio.editor.route.testRegex.match')}</p>
  {:else if testResult === 'noMatch'}
    <p class="re-out re-noMatch">{t($locale, 'studio.editor.route.testRegex.noMatch')}</p>
  {:else if testResult === 'invalid'}
    <p class="re-out re-invalid">{t($locale, 'studio.editor.route.testRegex.invalid')}</p>
  {/if}
</Dialog>

<style>
  .re {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .re-help {
    margin: 0;
    color: var(--text-muted);
    font-size: 12px;
  }
  .re-tools {
    display: flex;
    justify-content: flex-end;
  }
  .re-adv {
    margin-block-start: 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .re-adv summary {
    cursor: pointer;
    padding-block: 4px;
  }
  .re-adv-help {
    margin: 4px 0;
    color: var(--text-tertiary);
    font-size: 11px;
  }
  .re-adv-text {
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
  .re-label {
    display: block;
    margin-block-end: 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .re-text {
    display: block;
    inline-size: 100%;
    margin-block-end: 8px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--bg);
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 13px;
  }
  .re-out {
    margin: 4px 0 0;
    font-size: 13px;
    font-weight: 600;
  }
  .re-match { color: var(--success); }
  .re-noMatch { color: var(--text-muted); }
  .re-invalid { color: var(--danger); }
</style>
