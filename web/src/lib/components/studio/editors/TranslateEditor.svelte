<!--
  TranslateEditor — structured editor for `translate` stages.

  Backend config:
    {
      output_format: 'same' | 'json' | 'xml' | 'protobuf',
      schema_id?: string,        // required when output_format='protobuf'
      proto_message?: string     // required when output_format='protobuf'
    }

  UI:
    - Output-format dropdown (Select).
    - Conditional Protobuf fields: when 'protobuf' is selected we render
      SchemaSelector filtered to protobuf schemas + a proto_message text
      input.
    - Advanced (raw JSON) <details> escape hatch that preserves any
      unknown keys (same pattern as the other editors).

  Validation:
    - Non-protobuf: always valid.
    - Protobuf: requires both schema_id and proto_message.
-->
<script lang="ts">
  import { locale, t } from '$lib/stores/locale';
  import Select from '$lib/components/Select.svelte';
  import Input from '$lib/components/Input.svelte';
  import SchemaSelector from './SchemaSelector.svelte';

  export let config = '{}';
  export let valid = true;

  type OutputFormat = 'same' | 'json' | 'xml' | 'protobuf';

  let outputFormat: OutputFormat = 'same';
  let schemaId = '';
  let protoMessage = '';
  let extras: Record<string, unknown> = {};
  let lastIn = '';
  let showAdvanced = false;

  $: if (config !== lastIn) {
    parse(config);
    lastIn = config;
  }

  $: outputFormatOptions = [
    { value: 'same', label: t($locale, 'pipelines.outputFormat.same') },
    { value: 'json', label: 'JSON' },
    { value: 'xml', label: 'XML' },
    { value: 'protobuf', label: 'Protobuf' }
  ];

  function parse(s: string) {
    outputFormat = 'same';
    schemaId = '';
    protoMessage = '';
    extras = {};
    let raw: unknown;
    try {
      raw = JSON.parse(s || '{}');
    } catch {
      return;
    }
    if (typeof raw !== 'object' || raw === null) return;
    const obj = raw as Record<string, unknown>;
    if (
      obj.output_format === 'same' ||
      obj.output_format === 'json' ||
      obj.output_format === 'xml' ||
      obj.output_format === 'protobuf'
    ) {
      outputFormat = obj.output_format;
    }
    if (typeof obj.schema_id === 'string') schemaId = obj.schema_id;
    if (typeof obj.proto_message === 'string') protoMessage = obj.proto_message;
    for (const [k, v] of Object.entries(obj)) {
      if (k !== 'output_format' && k !== 'schema_id' && k !== 'proto_message') {
        extras[k] = v;
      }
    }
  }

  function validate() {
    if (outputFormat === 'protobuf') {
      valid = !!schemaId && !!protoMessage.trim();
    } else {
      valid = true;
    }
  }

  function onAdvancedInput(e: Event) {
    const v = (e.target as HTMLTextAreaElement).value;
    config = v;
    lastIn = v;
    parse(v);
    validate();
  }

  // Auto-commit when any structured field changes. We can't rely on
  // <Select on:change> because the Select component doesn't forward
  // DOM events; bind:value is the reliable signal. Track the last
  // serialised state so this reactive block only fires on real edits
  // (not on the round-trip from parse(config) above).
  let lastSerialised = '';
  $: {
    // Reading every field so Svelte tracks dependencies.
    const _ = `${outputFormat}|${schemaId}|${protoMessage}`;
    const out: Record<string, unknown> = { ...extras, output_format: outputFormat };
    if (outputFormat === 'protobuf') {
      out.schema_id = schemaId;
      out.proto_message = protoMessage;
    }
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

<div class="te">
  <Select
    bind:value={outputFormat}
    options={outputFormatOptions}
    label={t($locale, 'studio.editor.translate.format')}
  />
  <p class="te-help">{t($locale, 'studio.editor.translate.help')}</p>

  {#if outputFormat === 'protobuf'}
    <SchemaSelector
      bind:value={schemaId}
      filter="protobuf"
      label={t($locale, 'studio.editor.translate.schema')}
    />
    <Input
      bind:value={protoMessage}
      label={t($locale, 'studio.editor.translate.protoMessage')}
      placeholder={t($locale, 'studio.editor.translate.protoMessage.placeholder')}
    />
  {/if}

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
