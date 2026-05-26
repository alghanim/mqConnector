<!--
  WasmEditor — structured editor for `wasm` stages.

  Backend config: { plugin: string }

  UI:
    - Plugin dropdown populated from GET /api/v1/plugins (filtered to
      the current tenant by the backend).
    - Selected-plugin metadata card below the dropdown (size, uploaded
      timestamp). Pure read-only info — keeps the operator confident
      they're pointing at the right blob.
    - "Upload new plugin" button — opens a Dialog with a small form
      (name + file input) that POSTs multipart/form-data to
      /api/v1/plugins. On success the list refreshes and the new
      plugin auto-selects.
    - Advanced (raw JSON) <details> escape hatch.

  Validation: plugin must be a non-empty string. The backend rejects
  unknown plugin names at deploy time; surfacing that here would need
  a second fetch every keystroke, so we trust the dropdown.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Plugin } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Select from '$lib/components/Select.svelte';
  import Input from '$lib/components/Input.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Alert from '$lib/components/Alert.svelte';

  export let config = '{}';
  export let valid = false;

  let pluginName = '';
  let extras: Record<string, unknown> = {};
  let lastIn = '';
  let showAdvanced = false;

  let plugins: Plugin[] = [];

  // Upload dialog state
  let uploadOpen = false;
  let uploadName = '';
  let uploadFile: File | null = null;
  let uploadBusy = false;
  let uploadError = '';

  $: if (config !== lastIn) {
    parse(config);
    lastIn = config;
  }

  $: pluginOptions = [
    { value: '', label: t($locale, 'studio.editor.wasm.empty') },
    ...plugins.map((p) => ({ value: p.name, label: p.name }))
  ];

  $: selected = plugins.find((p) => p.name === pluginName) ?? null;

  onMount(async () => {
    await refreshPlugins();
    validate();
  });

  async function refreshPlugins() {
    try {
      plugins = (await api.get<Plugin[]>('/v1/plugins')) ?? [];
    } catch {
      plugins = [];
    }
  }

  function parse(s: string) {
    pluginName = '';
    extras = {};
    let raw: unknown;
    try {
      raw = JSON.parse(s || '{}');
    } catch {
      return;
    }
    if (typeof raw !== 'object' || raw === null) return;
    const obj = raw as Record<string, unknown>;
    if (typeof obj.plugin === 'string') pluginName = obj.plugin;
    for (const [k, v] of Object.entries(obj)) {
      if (k !== 'plugin') extras[k] = v;
    }
  }

  // commit is still called by the upload flow (after auto-selecting a
  // freshly uploaded plugin); the reactive watcher below covers the
  // bind:value-driven edits.
  function commit() {
    const out: Record<string, unknown> = { ...extras, plugin: pluginName };
    config = JSON.stringify(out);
    lastIn = config;
    validate();
  }

  function validate() {
    valid = !!pluginName && pluginName.trim() !== '';
  }

  function openUpload() {
    uploadName = '';
    uploadFile = null;
    uploadError = '';
    uploadOpen = true;
  }

  function onFileInput(e: Event) {
    const target = e.target as HTMLInputElement;
    uploadFile = target.files?.[0] ?? null;
    // Default the name field to the file's basename minus the .wasm
    // suffix so the operator usually doesn't need to type anything.
    if (uploadFile && !uploadName) {
      uploadName = uploadFile.name.replace(/\.wasm$/i, '');
    }
  }

  async function doUpload() {
    if (!uploadName || !uploadFile) {
      uploadError = t($locale, 'studio.editor.wasm.upload.error');
      return;
    }
    uploadBusy = true;
    uploadError = '';
    try {
      const fd = new FormData();
      fd.append('name', uploadName);
      fd.append('blob', uploadFile);
      // Plugin upload bypasses the JSON request helper — it needs a
      // multipart body. We post directly with the CSRF cookie echoed
      // in the standard header. The endpoint is system-admin-only on
      // the server; non-admins get a 403 here.
      const csrfCookie = (() => {
        if (typeof document === 'undefined') return '';
        for (const c of document.cookie.split(';')) {
          const trimmed = c.trim();
          if (trimmed.startsWith('mqc_csrf=')) return trimmed.slice('mqc_csrf='.length);
        }
        return '';
      })();
      const res = await fetch('/api/v1/plugins', {
        method: 'POST',
        credentials: 'include',
        headers: csrfCookie ? { 'X-CSRF-Token': csrfCookie } : undefined,
        body: fd
      });
      if (!res.ok) {
        const text = await res.text().catch(() => '');
        throw new Error(text || `HTTP ${res.status}`);
      }
      const fresh = (await res.json()) as Plugin;
      await refreshPlugins();
      pluginName = fresh.name;
      commit();
      uploadOpen = false;
    } catch (e) {
      uploadError = (e as { message?: string }).message || 'upload failed';
    } finally {
      uploadBusy = false;
    }
  }

  function formatSize(bytes?: number): string {
    if (!bytes || !Number.isFinite(bytes)) return '—';
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
    return `${(bytes / (1024 * 1024)).toFixed(2)} MiB`;
  }

  function onAdvancedInput(e: Event) {
    const v = (e.target as HTMLTextAreaElement).value;
    config = v;
    lastIn = v;
    parse(v);
    validate();
  }

  // Reactive auto-commit on plugin selection change.
  let lastSerialised = '';
  $: {
    const _ = pluginName;
    const out: Record<string, unknown> = { ...extras, plugin: pluginName };
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

<div class="we">
  <Select
    bind:value={pluginName}
    options={pluginOptions}
    label={t($locale, 'studio.editor.wasm.plugin')}
  />

  {#if selected}
    <dl class="we-meta">
      <dt>{t($locale, 'studio.editor.wasm.size')}</dt>
      <dd>{formatSize(selected.size_bytes)}</dd>
      <dt>{t($locale, 'studio.editor.wasm.uploaded')}</dt>
      <dd>{selected.uploaded_at ?? '—'}</dd>
    </dl>
  {/if}

  <p class="we-help">{t($locale, 'studio.editor.wasm.help')}</p>

  {#if !pluginName}
    <p class="we-err">{t($locale, 'studio.editor.wasm.error.plugin')}</p>
  {/if}

  <div class="we-actions">
    <Button variant="outline" on:click={openUpload}>
      {t($locale, 'studio.editor.wasm.upload')}
    </Button>
  </div>

  <details bind:open={showAdvanced} class="we-adv">
    <summary>{t($locale, 'studio.editor.advanced')}</summary>
    <p class="we-adv-help">{t($locale, 'studio.editor.advanced.help')}</p>
    <textarea
      class="we-adv-text"
      rows="4"
      value={config}
      on:input={onAdvancedInput}
      spellcheck="false"
    ></textarea>
  </details>
</div>

<Dialog
  open={uploadOpen}
  title={t($locale, 'studio.editor.wasm.upload.title')}
  cancelLabel={t($locale, 'common.cancel')}
  confirmLabel={t($locale, 'studio.editor.wasm.upload.submit')}
  busy={uploadBusy}
  on:cancel={() => (uploadOpen = false)}
  on:confirm={doUpload}
>
  <Input
    bind:value={uploadName}
    label={t($locale, 'studio.editor.wasm.upload.name')}
  />
  <label class="we-label" for="we-file">
    {t($locale, 'studio.editor.wasm.upload.file')}
  </label>
  <input id="we-file" type="file" accept=".wasm" on:change={onFileInput} />
  {#if uploadError}
    <div class="we-alert">
      <Alert variant="error">
        <span slot="title">{t($locale, 'studio.editor.wasm.upload.error')}</span>
        {uploadError}
      </Alert>
    </div>
  {/if}
</Dialog>

<style>
  .we {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .we-meta {
    margin: 0;
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 4px 12px;
    padding: 8px 12px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface);
  }
  .we-meta dt {
    color: var(--text-muted);
    font-size: 11px;
    font-weight: 600;
  }
  .we-meta dd {
    margin: 0;
    color: var(--text);
    font-size: 12px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }
  .we-help {
    margin: 0;
    color: var(--text-muted);
    font-size: 12px;
  }
  .we-err {
    margin: 0;
    color: var(--danger);
    font-size: 12px;
  }
  .we-actions {
    display: flex;
    justify-content: flex-end;
  }
  .we-label {
    display: block;
    margin-block: 8px 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .we-alert {
    margin-block-start: 8px;
  }
  .we-adv {
    margin-block-start: 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .we-adv summary {
    cursor: pointer;
    padding-block: 4px;
  }
  .we-adv-help {
    margin: 4px 0;
    color: var(--text-tertiary);
    font-size: 11px;
  }
  .we-adv-text {
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
