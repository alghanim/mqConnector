<!--
  StageConfigForm — typed editor for a stage's JSON config blob.

  Replaces the "type-the-keys-from-memory" textarea with a per-type form.
  Bound via `config` (a JSON string); the form writes back on every change
  so the parent's existing PUT-and-deploy flow needs no plumbing changes.

  Anything the form doesn't recognise in the parsed object is preserved
  on commit — so a stage carrying extra keys from a future release won't
  be silently truncated by an older UI.
-->
<script lang="ts">
  import { tick } from 'svelte';
  import type { StageType, Schema } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Select from '$lib/components/Select.svelte';

  export let type: StageType;
  /** JSON-encoded config. Two-way: writes flow back to the parent. */
  export let config: string;
  /** Schemas available for the validate-stage picker. */
  export let schemas: Schema[] = [];

  // View model. Re-parsed when the parent supplies a *different* config
  // string (e.g. switching between stages); local edits commit back via
  // commit() and avoid retriggering the re-parse.
  type ViewConfig = {
    paths: string[];
    output_format: 'same' | 'json' | 'xml';
    script: string;
    schema_id: string;
    [k: string]: unknown;
  };
  // bind:value needs concrete string fields, not optionals; the parser
  // backfills any missing key with its default below.
  function defaultView(): ViewConfig {
    return { paths: [], output_format: 'same', script: '', schema_id: '' };
  }
  let view: ViewConfig = defaultView();
  let lastConfig = '';
  let pathBuf = '';
  let showAdvanced = false;

  $: if (config !== lastConfig) {
    view = parse(config);
    lastConfig = config;
  }

  function parse(s: string): ViewConfig {
    const out = defaultView();
    let raw: unknown;
    try {
      raw = JSON.parse(s || '{}');
    } catch {
      return out;
    }
    if (typeof raw !== 'object' || raw === null) return out;
    const obj = raw as Record<string, unknown>;
    if (Array.isArray(obj.paths)) out.paths = obj.paths.filter((p): p is string => typeof p === 'string');
    if (obj.output_format === 'same' || obj.output_format === 'json' || obj.output_format === 'xml') {
      out.output_format = obj.output_format;
    }
    if (typeof obj.script === 'string') out.script = obj.script;
    if (typeof obj.schema_id === 'string') out.schema_id = obj.schema_id;
    // Preserve any extra keys from a future release so the typed UI
    // doesn't silently truncate them on save.
    for (const [k, v] of Object.entries(obj)) {
      if (!(k in out)) out[k] = v;
    }
    return out;
  }

  function commit() {
    config = JSON.stringify(view);
    lastConfig = config;
  }

  // ─── filter ─────────────────────────────────────────────────────
  function addPath() {
    const p = pathBuf.trim();
    if (!p) return;
    const paths = Array.isArray(view.paths) ? view.paths.slice() : [];
    if (!paths.includes(p)) paths.push(p);
    view.paths = paths;
    pathBuf = '';
    commit();
  }
  function removePath(p: string) {
    const paths = Array.isArray(view.paths) ? view.paths.filter((x) => x !== p) : [];
    view.paths = paths;
    commit();
  }
  async function onPathKey(e: KeyboardEvent) {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault();
      addPath();
      await tick();
    }
  }

  // ─── translate ──────────────────────────────────────────────────
  $: outputFormatOptions = [
    { value: 'same', label: t($locale, 'pipelines.outputFormat.same') },
    { value: 'json', label: 'JSON' },
    { value: 'xml', label: 'XML' }
  ];

  // ─── validate ───────────────────────────────────────────────────
  $: schemaOptions = [
    { value: '', label: t($locale, 'stageConfig.schemaNone') },
    ...schemas.map((s) => ({ value: s.id || '', label: `${s.name} (${s.schema_type})` }))
  ];

  // Advanced JSON view — last-resort escape hatch. Edits there are
  // committed to `config` directly without going through `view`.
  function onAdvancedInput(e: Event) {
    const v = (e.target as HTMLTextAreaElement).value;
    config = v;
    lastConfig = v;
    view = parse(v);
  }
</script>

<div class="stage-config">
  {#if type === 'filter'}
    <p class="hint">{t($locale, 'stageConfig.filter.help')}</p>
    <div class="paths-row">
      {#each view.paths ?? [] as p}
        <span class="path-chip">
          {p}
          <button class="chip-x" type="button" on:click={() => removePath(p)}>×</button>
        </span>
      {/each}
    </div>
    <div class="path-input-row">
      <input
        type="text"
        class="path-input"
        placeholder={t($locale, 'stageConfig.filter.placeholder')}
        bind:value={pathBuf}
        on:keydown={onPathKey}
      />
      <button class="add-btn" type="button" on:click={addPath}>
        {t($locale, 'stageConfig.filter.add')}
      </button>
    </div>
  {:else if type === 'translate'}
    <Select
      bind:value={view.output_format}
      options={outputFormatOptions}
      label={t($locale, 'stageConfig.translate.target')}
      on:change={commit}
    />
    <p class="hint">{t($locale, 'stageConfig.translate.help')}</p>
  {:else if type === 'script'}
    <label class="form-label" for="script-body">{t($locale, 'stageConfig.script.body')}</label>
    <textarea
      id="script-body"
      class="code-input"
      rows="6"
      bind:value={view.script}
      on:input={commit}
      placeholder={'msg.processed = true;\nmsg;'}
    ></textarea>
    <p class="hint">{t($locale, 'stageConfig.script.help')}</p>
  {:else if type === 'validate'}
    <Select
      bind:value={view.schema_id}
      options={schemaOptions}
      label={t($locale, 'stageConfig.validate.schema')}
      on:change={commit}
    />
    <p class="hint">{t($locale, 'stageConfig.validate.help')}</p>
  {:else if type === 'route'}
    <p class="hint">{t($locale, 'stageConfig.route.help')}</p>
  {:else if type === 'transform'}
    <p class="hint">{t($locale, 'stageConfig.transform.help')}</p>
  {/if}

  <!-- Advanced escape hatch — preserve forward-compatibility for any
       fields the typed form doesn't know about. -->
  <details bind:open={showAdvanced}>
    <summary>{t($locale, 'stageConfig.advanced')}</summary>
    <textarea class="code-input" rows="4" value={config} on:input={onAdvancedInput}></textarea>
  </details>
</div>

<style>
  .stage-config {
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 12px;
    background: var(--bg);
  }
  .hint {
    font-size: 12px;
    color: var(--text-muted);
    margin-top: 6px;
    margin-bottom: 8px;
  }
  .form-label {
    display: block;
    margin: 6px 0 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .paths-row {
    display: flex; flex-wrap: wrap; gap: 6px;
    margin-bottom: 8px;
    min-height: 26px;
  }
  .path-chip {
    display: inline-flex; align-items: center; gap: 6px;
    padding: 2px 4px 2px 10px;
    border: 1px solid var(--border);
    border-radius: 12px; /* labeled chip — Brand Guide §5 / Rule 9 (pill is count-badge only) */
    background: var(--surface);
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
  }
  .chip-x {
    background: transparent; border: none; cursor: pointer;
    color: var(--text-muted);
    width: 20px; height: 20px;
    border-radius: 50%;
    line-height: 1;
  }
  .chip-x:hover { background: var(--danger); color: var(--danger-on); }
  .path-input-row {
    display: flex; gap: 8px;
  }
  .path-input, .code-input {
    flex: 1;
    background: var(--bg); color: var(--text);
    border: 1px solid var(--border); border-radius: 12px;
    padding: 8px 10px; font-size: 13px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }
  .code-input { width: 100%; resize: vertical; }
  .add-btn {
    padding: 8px 14px;
    border-radius: 12px;
    background: var(--accent); color: var(--accent-on);
    border: none; cursor: pointer; font-size: 13px;
  }
  details {
    margin-top: 10px; font-size: 12px; color: var(--text-muted);
  }
  details summary { cursor: pointer; padding: 4px 0; }
  details textarea { margin-top: 6px; }
</style>
