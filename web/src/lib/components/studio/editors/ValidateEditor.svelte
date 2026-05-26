<!--
  ValidateEditor — structured editor for `validate` stages.

  Backend config:
    {
      schema_id?: string,
      schema_type?: string,    // mirrors the chosen schema for runtime
      content?: string,        // legacy inline-schema; hidden under Advanced
      proto_message?: string   // required when the picked schema is protobuf
    }

  UI:
    - SchemaSelector (no type filter — every schema is selectable). The
      selected schema's type controls whether we render the protobuf
      message-name field.
    - "Test schema against sample" Button — emits a `test` custom event
      the parent inspector can forward to Task 11's DryRunDock.
    - Advanced (raw JSON) <details> escape hatch.

  Validation:
    - schema_id must be set (a schema must be picked).
    - When the picked schema_type='protobuf', proto_message must also
      be non-empty.
-->
<script lang="ts">
  import { createEventDispatcher, onMount } from 'svelte';
  import { api, type Schema } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Input from '$lib/components/Input.svelte';
  import Button from '$lib/components/Button.svelte';
  import SchemaSelector from './SchemaSelector.svelte';

  export let config = '{}';
  export let valid = false;
  /** Pre-fetched schemas. If null, fetched on mount. */
  export let schemas: Schema[] | null = null;

  const dispatch = createEventDispatcher<{
    test: { schema_id: string; proto_message: string };
  }>();

  let schemaId = '';
  let protoMessage = '';
  let extras: Record<string, unknown> = {};
  let lastIn = '';
  let showAdvanced = false;
  let internalSchemas: Schema[] = schemas ?? [];

  $: if (schemas !== null) internalSchemas = schemas;

  onMount(async () => {
    if (schemas === null) {
      try {
        internalSchemas = (await api.get<Schema[]>('/v1/schemas')) ?? [];
      } catch {
        internalSchemas = [];
      }
      validate();
    }
  });

  // Pick the picked schema's type so the protobuf branch can decide
  // whether to render the proto_message input.
  $: pickedType = (internalSchemas.find((s) => s.id === schemaId)?.schema_type ?? '') as
    | ''
    | 'json_schema'
    | 'xsd'
    | 'protobuf';

  $: if (config !== lastIn) {
    parse(config);
    lastIn = config;
  }

  function parse(s: string) {
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
    if (typeof obj.schema_id === 'string') schemaId = obj.schema_id;
    if (typeof obj.proto_message === 'string') protoMessage = obj.proto_message;
    for (const [k, v] of Object.entries(obj)) {
      if (k !== 'schema_id' && k !== 'proto_message') extras[k] = v;
    }
  }

  function validate() {
    if (!schemaId) {
      valid = false;
      return;
    }
    if (pickedType === 'protobuf' && (!protoMessage || protoMessage.trim() === '')) {
      valid = false;
      return;
    }
    valid = true;
  }

  function onTest() {
    if (!valid) return;
    dispatch('test', { schema_id: schemaId, proto_message: protoMessage });
  }

  function onAdvancedInput(e: Event) {
    const v = (e.target as HTMLTextAreaElement).value;
    config = v;
    lastIn = v;
    parse(v);
    validate();
  }

  // Reactive auto-commit. <Select> + <Input> don't forward DOM events,
  // so we react to bind:value changes instead. lastSerialised guards
  // against the parse() → field-set → re-emit cycle.
  let lastSerialised = '';
  $: {
    const _ = `${schemaId}|${protoMessage}|${pickedType}`;
    const out: Record<string, unknown> = { ...extras };
    if (schemaId) out.schema_id = schemaId;
    if (pickedType === 'protobuf' && protoMessage) out.proto_message = protoMessage;
    if (pickedType) out.schema_type = pickedType;
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

<div class="ve">
  <SchemaSelector
    schemas={internalSchemas}
    bind:value={schemaId}
    label={t($locale, 'studio.editor.validate.schema')}
  />

  {#if pickedType === 'protobuf'}
    <Input
      bind:value={protoMessage}
      label={t($locale, 'studio.editor.validate.protoMessage')}
    />
  {/if}

  <p class="ve-help">{t($locale, 'studio.editor.validate.help')}</p>

  {#if !schemaId}
    <p class="ve-err">{t($locale, 'studio.editor.validate.error.schema')}</p>
  {:else if pickedType === 'protobuf' && (!protoMessage || protoMessage.trim() === '')}
    <p class="ve-err">{t($locale, 'studio.editor.validate.error.proto')}</p>
  {/if}

  <div class="ve-actions">
    <Button variant="outline" on:click={onTest} disabled={!valid}>
      {t($locale, 'studio.editor.validate.test')}
    </Button>
  </div>

  <details bind:open={showAdvanced} class="ve-adv">
    <summary>{t($locale, 'studio.editor.advanced')}</summary>
    <p class="ve-adv-help">{t($locale, 'studio.editor.advanced.help')}</p>
    <textarea
      class="ve-adv-text"
      rows="4"
      value={config}
      on:input={onAdvancedInput}
      spellcheck="false"
    ></textarea>
  </details>
</div>

<style>
  .ve {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .ve-help {
    margin: 0;
    color: var(--text-muted);
    font-size: 12px;
  }
  .ve-err {
    margin: 0;
    color: var(--danger);
    font-size: 12px;
  }
  .ve-actions {
    display: flex;
    justify-content: flex-end;
  }
  .ve-adv {
    margin-block-start: 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .ve-adv summary {
    cursor: pointer;
    padding-block: 4px;
  }
  .ve-adv-help {
    margin: 4px 0;
    color: var(--text-tertiary);
    font-size: 11px;
  }
  .ve-adv-text {
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
