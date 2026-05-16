<!--
  /settings — tenant-scoped configuration import / export.

  Two cards:
    • Export. Triggers GET /api/v1/config/export with format=yaml
      or format=json; the browser handles the download via the
      Content-Disposition header.
    • Import. Drag-and-drop or file-picker for a previously-exported
      bundle. POST /api/v1/config/import?dry_run=true to preview;
      on confirm, POST without dry_run to commit.

  Conflict-rejection is enforced server-side — if any connection or
  pipeline name in the bundle clashes with an existing row the
  import returns 409 and nothing is written. The UI surfaces that
  message verbatim so the operator can see exactly which name
  collided.
-->
<script lang="ts">
  import { api, type ConfigImportResult } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import { toasts } from '$lib/stores/toasts';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import { Download, FileText, Upload, X } from 'lucide-svelte';

  // ─── Export ────────────────────────────────────────────────────
  // Triggers a download by hitting the endpoint as a regular fetch
  // and creating a Blob URL. We can't use a plain <a download> link
  // because the request needs to carry the session cookie via
  // credentials: 'include' (the SvelteKit fetch interceptor does it
  // for us, but only on programmatic fetch — not link clicks).
  async function download(format: 'yaml' | 'json') {
    try {
      const res = await fetch(`/api/v1/config/export?format=${format}`, {
        credentials: 'include'
      });
      if (!res.ok) {
        throw new Error(`HTTP ${res.status}`);
      }
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `mqconnector-config.${format}`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
      toasts.success(t($locale, 'settings.export.title'), `${format.toUpperCase()}`);
    } catch (e: unknown) {
      const msg = (e as { message?: string }).message ?? 'export failed';
      toasts.error('Could not export config', msg);
    }
  }

  // ─── Import state ──────────────────────────────────────────────
  let stagedFile: File | null = null;
  let stagedText = '';
  let dryRunResult: ConfigImportResult | null = null;
  let importError = '';
  let busy = false;
  let dragOver = false;
  let fileInput: HTMLInputElement;

  function onFilePicked(e: Event) {
    const f = (e.target as HTMLInputElement).files?.[0];
    if (f) stageFile(f);
  }
  async function stageFile(f: File) {
    stagedFile = f;
    stagedText = await f.text();
    dryRunResult = null;
    importError = '';
  }
  function clearStaged() {
    stagedFile = null;
    stagedText = '';
    dryRunResult = null;
    importError = '';
    if (fileInput) fileInput.value = '';
  }

  async function postImport(dryRun: boolean): Promise<void> {
    if (!stagedFile || !stagedText) return;
    busy = true;
    importError = '';
    try {
      const url = `/api/v1/config/import${dryRun ? '?dry_run=true' : ''}`;
      const isYAML = stagedFile.name.endsWith('.yaml') || stagedFile.name.endsWith('.yml');
      const ct = isYAML ? 'application/yaml' : 'application/json';
      const res = await fetch(url, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': ct, Accept: 'application/json' },
        body: stagedText
      });
      const payload = await res.json().catch(() => ({}));
      if (!res.ok) {
        importError = (payload as { error?: string }).error ?? `HTTP ${res.status}`;
        return;
      }
      dryRunResult = payload as ConfigImportResult;
      if (dryRun) {
        toasts.success(t($locale, 'settings.import.dryRun.success'));
      } else {
        toasts.success(t($locale, 'settings.import.success'));
        // After a successful apply, clear staging so the operator
        // doesn't accidentally re-apply the same bundle.
        clearStaged();
      }
    } catch (e: unknown) {
      importError = (e as { message?: string }).message ?? 'import failed';
    } finally {
      busy = false;
    }
  }

  // ─── Drag-and-drop ─────────────────────────────────────────────
  function onDragOver(e: DragEvent) {
    e.preventDefault();
    dragOver = true;
  }
  function onDragLeave() {
    dragOver = false;
  }
  function onDrop(e: DragEvent) {
    e.preventDefault();
    dragOver = false;
    const f = e.dataTransfer?.files?.[0];
    if (f) stageFile(f);
  }
</script>

<div class="space-y-6 max-w-4xl">
  <PageHeader title={t($locale, 'settings.title')} subtitle={t($locale, 'settings.subtitle')} />

  <!-- ─── Export ──────────────────────────────────────────────── -->
  <Card>
    <h3 class="section-title">{t($locale, 'settings.export.title')}</h3>
    <p class="section-body">{t($locale, 'settings.export.body')}</p>
    <div class="flex gap-2 mt-4">
      <Button on:click={() => download('yaml')}>
        <Download size={14} strokeWidth={1.75} />
        <span>{t($locale, 'settings.export.yaml')}</span>
      </Button>
      <Button variant="outline" on:click={() => download('json')}>
        <Download size={14} strokeWidth={1.75} />
        <span>{t($locale, 'settings.export.json')}</span>
      </Button>
    </div>
  </Card>

  <!-- ─── Import ──────────────────────────────────────────────── -->
  <Card>
    <h3 class="section-title">{t($locale, 'settings.import.title')}</h3>
    <p class="section-body">{t($locale, 'settings.import.body')}</p>

    {#if !stagedFile}
      <!-- Drop-zone — also functions as a click-to-pick label.
           svelte-a11y wants a keyboard handler on click-bindings, but
           drag-drop and file-input share the same affordance; we add a
           Tab-focusable button inside instead. -->
      <!-- svelte-ignore a11y-no-static-element-interactions -->
      <div
        class="dropzone"
        class:over={dragOver}
        on:dragover={onDragOver}
        on:dragleave={onDragLeave}
        on:drop={onDrop}
      >
        <FileText size={32} strokeWidth={1.5} class="dropzone-icon" />
        <p class="dropzone-hint">{t($locale, 'settings.import.dropHint')}</p>
        <input
          bind:this={fileInput}
          type="file"
          accept=".yaml,.yml,.json,application/yaml,application/json"
          class="dropzone-input"
          on:change={onFilePicked}
        />
      </div>
    {:else}
      <div class="staged">
        <div class="staged-row">
          <FileText size={16} strokeWidth={1.75} />
          <span class="staged-name">{stagedFile.name}</span>
          <span class="staged-size">{(stagedFile.size / 1024).toFixed(1)} KB</span>
          <button type="button" class="staged-remove" on:click={clearStaged} aria-label="Remove">
            <X size={14} strokeWidth={2} />
          </button>
        </div>

        {#if importError}
          <div class="import-error">{importError}</div>
        {/if}

        {#if dryRunResult}
          <div class="import-summary">
            <Badge variant="success">{dryRunResult.status}</Badge>
            <span class="summary-num">{dryRunResult.connections}</span>
            <span class="summary-label">{t($locale, 'settings.import.summary.connections')}</span>
            <span class="summary-sep">·</span>
            <span class="summary-num">{dryRunResult.pipelines}</span>
            <span class="summary-label">{t($locale, 'settings.import.summary.pipelines')}</span>
            {#if dryRunResult.dry_run}
              <Badge variant="neutral">dry-run</Badge>
            {/if}
          </div>
        {/if}

        <div class="flex gap-2 mt-3">
          <Button variant="outline" on:click={() => postImport(true)} loading={busy}>
            {t($locale, 'settings.import.dryRun')}
          </Button>
          <Button
            on:click={() => postImport(false)}
            loading={busy}
            disabled={!dryRunResult || !!dryRunResult.dry_run === false || !!importError}
          >
            <Upload size={14} strokeWidth={1.75} />
            <span>{t($locale, 'settings.import.apply')}</span>
          </Button>
        </div>
      </div>
    {/if}
  </Card>
</div>

<style>
  .section-title {
    margin: 0 0 6px;
    color: var(--text);
    font-size: 14px;
    font-weight: 600;
    letter-spacing: -0.005em;
  }
  .section-body {
    color: var(--text-muted);
    font-size: 13px;
    line-height: 1.55;
  }

  .dropzone {
    position: relative;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    margin-block-start: 16px;
    padding: 36px 16px;
    border: 1px dashed var(--border-strong);
    border-radius: 12px;
    background: var(--surface-2);
    color: var(--text-muted);
    transition:
      border-color 150ms,
      background-color 150ms;
  }
  .dropzone.over {
    border-color: var(--secondary);
    background: color-mix(in srgb, var(--secondary) 10%, var(--surface-2));
  }
  :global([data-theme='light']) .dropzone.over {
    border-color: var(--primary);
    background: color-mix(in srgb, var(--primary) 8%, var(--surface-2));
  }
  .dropzone :global(.dropzone-icon) {
    color: var(--text-tertiary);
  }
  .dropzone-hint {
    font-size: 13px;
    color: var(--text-muted);
  }
  /* Stretches over the parent so a click anywhere on the dropzone
     opens the file picker. The native input is visually hidden but
     accessible to assistive tech and keyboard users. */
  .dropzone-input {
    position: absolute;
    inset: 0;
    opacity: 0;
    cursor: pointer;
  }

  .staged {
    margin-block-start: 16px;
  }
  .staged-row {
    /* Interactive container (clear button lives inside) → 12dp per
       §7 rule 10. */
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    border: 1px solid var(--card-border);
    border-radius: 12px;
    background: var(--surface-2);
    color: var(--text);
  }
  .staged-name {
    flex: 1;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
  }
  .staged-size {
    color: var(--text-tertiary);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }
  .staged-remove {
    /* Interactive icon button → 12dp per §7 rule 10. */
    inline-size: 24px;
    block-size: 24px;
    border-radius: 12px;
    background: transparent;
    border: 1px solid var(--divider);
    color: var(--text-muted);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .staged-remove:hover {
    background: var(--danger);
    color: var(--danger-on);
    border-color: var(--danger);
  }

  .import-error {
    /* Alert per §5.10 → 12dp corner radius. */
    margin-block-start: 12px;
    padding: 10px 12px;
    background: var(--danger-bg);
    border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent);
    border-radius: 12px;
    color: var(--danger);
    font-size: 13px;
    line-height: 1.45;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }
  .import-summary {
    /* Labeled chip per §5.5 / §7 rule 10 → 12dp. */
    margin-block-start: 12px;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    background: var(--surface-2);
    border: 1px solid var(--divider);
    border-radius: 12px;
  }
  .summary-num {
    color: var(--text);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  .summary-label {
    color: var(--text-muted);
    font-size: 12px;
  }
  .summary-sep {
    color: var(--text-tertiary);
  }
</style>
