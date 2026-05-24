<!--
  DeployDialog — confirms a deploy or rollback against a target revision.

  Two kinds, one component:
    kind='deploy'   → POST /api/v1/pipelines/{id}/deploy
                      body { revision_number, change_summary, approver? }
    kind='rollback' → POST /api/v1/pipelines/{id}/revisions/{rev}/rollback
                      body { change_summary }

  Layout (uses <Dialog> primitive):

      ┌──────────────────────────────────────────────────────────────┐
      │ Deploy revision #N                                           │
      ├──────────────────────────────────────────────────────────────┤
      │ X stages added, Y modified, Z removed     [view full diff ▼] │
      │                                                              │
      │ [Change summary textarea]                                    │
      │ [Approver input, only when requires_approval]                │
      │                                                              │
      │ (inline DiffViewer when "view diff" expanded)                │
      ├──────────────────────────────────────────────────────────────┤
      │                                  [Cancel]  [Deploy / Rollback]│
      └──────────────────────────────────────────────────────────────┘

  Approver gate:
    - When `requiresApproval=true`, the approver input is required.
    - Confirm button is disabled when the approver field is empty.
    - Server returns 409 (handlers_pipeline_revisions.go) if missing —
      we surface "Approver required" under the field and keep the
      dialog open.

  Success path:
    - On 2xx, calls studio.hydrate(pipelineId) so the rail's
      deployedRev + revisions list re-render with the new revision.
    - Emits `done {revision: revisionResponse}` so the parent can
      close/dismiss the dialog and trigger any follow-up.
    - Toasts "Deployed revision N" / "Rolled back to revision N".
-->
<script lang="ts">
  import { createEventDispatcher, onMount } from 'svelte';
  import { api, type ApiError } from '$lib/api';
  import { studio, type PipelineRevision } from '$lib/stores/studio';
  import { locale, t } from '$lib/stores/locale';
  import { toasts } from '$lib/stores/toasts';
  import Dialog from '$lib/components/Dialog.svelte';
  import DiffViewer, { type SnapshotDiff } from './DiffViewer.svelte';

  export let kind: 'deploy' | 'rollback';
  export let pipelineId: string;
  export let targetRev: number;
  export let liveRev: number | null = null;
  /** Pre-fetched diff (skip the inner fetch on mount). */
  export let diff: SnapshotDiff | null = null;
  export let requiresApproval = false;
  export let open = true;

  const dispatch = createEventDispatcher<{
    done: { revision: PipelineRevision };
    cancel: void;
  }>();

  let changeSummary = '';
  let approver = '';
  let showDiff = false;
  let submitting = false;
  let errorBanner: string | null = null;
  let approverError: string | null = null;
  let internalDiff: SnapshotDiff | null = null;
  let diffLoading = false;

  // On mount: if the caller didn't pass a pre-fetched diff and there's
  // a known liveRev to compare against, do the fetch ourselves so the
  // dialog can render the summary line + inline DiffViewer without
  // forcing the parent to do it. liveRev === targetRev (re-deploy of
  // current) returns an empty diff — still useful to display.
  onMount(() => {
    if (diff || liveRev === null) return;
    void loadDiff();
  });

  async function loadDiff() {
    if (liveRev === null) return;
    diffLoading = true;
    try {
      const res = await api.get<{ diff: SnapshotDiff }>(
        `/v1/pipelines/${pipelineId}/revisions/${targetRev}/diff?against=${liveRev}`
      );
      internalDiff = res.diff;
    } catch {
      // Don't block the dialog — the summary line just won't render.
      internalDiff = null;
    } finally {
      diffLoading = false;
    }
  }

  $: effectiveDiff = diff ?? internalDiff;

  // Summary line — counts come from the merged diff. We fall back to
  // an empty descriptor when no diff is available.
  $: summary = (() => {
    if (!effectiveDiff) return null;
    const added =
      effectiveDiff.stages.added.length +
      effectiveDiff.transforms.added.length +
      effectiveDiff.routing_rules.added.length;
    const modified =
      effectiveDiff.stages.modified.length +
      effectiveDiff.transforms.modified.length +
      effectiveDiff.routing_rules.modified.length;
    const removed =
      effectiveDiff.stages.removed.length +
      effectiveDiff.transforms.removed.length +
      effectiveDiff.routing_rules.removed.length;
    return { added, modified, removed };
  })();

  $: title =
    kind === 'deploy'
      ? `${t($locale, 'studio.deploy.title.deploy')} #${targetRev}`
      : `${t($locale, 'studio.deploy.title.rollback')} #${targetRev}`;
  $: confirmLabel =
    kind === 'deploy'
      ? t($locale, 'studio.deploy.confirm.deploy')
      : t($locale, 'studio.deploy.confirm.rollback');
  $: summaryPlaceholder =
    kind === 'deploy'
      ? t($locale, 'studio.deploy.summary.placeholder.deploy')
      : t($locale, 'studio.deploy.summary.placeholder.rollback');

  // Confirm is disabled until the approver is filled (when required)
  // and while a request is in flight.
  $: confirmDisabled =
    submitting || (kind === 'deploy' && requiresApproval && approver.trim() === '');

  async function onConfirm() {
    submitting = true;
    errorBanner = null;
    approverError = null;
    try {
      const summaryBody = changeSummary.trim();
      let res: PipelineRevision;
      if (kind === 'deploy') {
        res = await api.post<PipelineRevision>(`/v1/pipelines/${pipelineId}/deploy`, {
          revision_number: targetRev,
          change_summary: summaryBody,
          ...(requiresApproval && approver.trim() ? { approver: approver.trim() } : {})
        });
      } else {
        res = await api.post<PipelineRevision>(
          `/v1/pipelines/${pipelineId}/revisions/${targetRev}/rollback`,
          { change_summary: summaryBody }
        );
      }
      const msg =
        kind === 'deploy'
          ? `${t($locale, 'studio.deploy.toast.deployed')} #${res.revision_number ?? targetRev}`
          : `${t($locale, 'studio.deploy.toast.rolledBack')} #${res.revision_number ?? targetRev}`;
      toasts.success(msg);
      // Re-hydrate the studio so the version rail picks up the new
      // deployed revision + appended history row.
      await studio.hydrate(pipelineId);
      dispatch('done', { revision: res });
    } catch (err) {
      const e = err as ApiError;
      if (e?.status === 409) {
        approverError = e.message || t($locale, 'studio.deploy.approver.required');
      } else {
        errorBanner = e?.message || t($locale, 'studio.deploy.error.generic');
      }
    } finally {
      submitting = false;
    }
  }

  function onCancel() {
    if (submitting) return;
    dispatch('cancel');
  }
</script>

<Dialog
  {open}
  {title}
  {confirmLabel}
  cancelLabel={t($locale, 'studio.deploy.cancel')}
  busy={submitting}
  confirmDisabled={confirmDisabled && !submitting}
  on:cancel={onCancel}
  on:confirm={onConfirm}
>
  <div class="deploy-body">
    {#if errorBanner}
      <p class="deploy-banner" role="alert">{errorBanner}</p>
    {/if}

    {#if summary}
      <div class="deploy-summary">
        <span class="deploy-summary-counts">
          <strong>{summary.added}</strong> {t($locale, 'studio.deploy.summary.stages.added')},
          <strong>{summary.modified}</strong> {t($locale, 'studio.deploy.summary.stages.modified')},
          <strong>{summary.removed}</strong> {t($locale, 'studio.deploy.summary.stages.removed')}
        </span>
        {#if effectiveDiff}
          <button
            type="button"
            class="deploy-summary-toggle"
            on:click={() => (showDiff = !showDiff)}
          >
            {showDiff
              ? t($locale, 'studio.deploy.hideDiff')
              : t($locale, 'studio.deploy.viewDiff')}
          </button>
        {/if}
      </div>
    {:else if diffLoading}
      <p class="deploy-loading">…</p>
    {/if}

    <label class="deploy-field">
      <span class="deploy-label">{t($locale, 'studio.deploy.summaryLabel')}</span>
      <textarea
        class="deploy-textarea"
        bind:value={changeSummary}
        placeholder={summaryPlaceholder}
        rows="3"
        disabled={submitting}
      ></textarea>
    </label>

    {#if kind === 'deploy' && requiresApproval}
      <label class="deploy-field">
        <span class="deploy-label">{t($locale, 'studio.deploy.approverLabel')}</span>
        <input
          class="deploy-input"
          type="text"
          bind:value={approver}
          disabled={submitting}
          aria-invalid={approverError !== null}
          aria-describedby={approverError ? 'approver-err' : undefined}
        />
        {#if approverError}
          <span id="approver-err" class="deploy-field-err">{approverError}</span>
        {/if}
      </label>
    {/if}

    {#if showDiff && effectiveDiff && liveRev !== null}
      <div class="deploy-diff">
        <DiffViewer revA={targetRev} revB={liveRev} diff={effectiveDiff} />
      </div>
    {/if}
  </div>
</Dialog>

<style>
  .deploy-body {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .deploy-banner {
    margin: 0;
    padding: 0.5rem 0.625rem;
    background: var(--danger-bg);
    color: var(--danger);
    border: 1px solid var(--danger);
    border-radius: 8px;
    font-size: 0.75rem;
  }
  .deploy-loading {
    margin: 0;
    color: var(--text-tertiary);
    font-size: 0.75rem;
    text-align: center;
  }
  .deploy-summary {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 0.5rem;
    padding: 0.5rem 0.625rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    font-size: 0.75rem;
    color: var(--text-muted);
    flex-wrap: wrap;
  }
  .deploy-summary-counts strong {
    color: var(--text);
  }
  .deploy-summary-toggle {
    background: transparent;
    border: 0;
    color: var(--accent);
    cursor: pointer;
    font-size: 0.75rem;
    font-weight: 600;
    padding: 0;
  }
  .deploy-summary-toggle:hover {
    text-decoration: underline;
  }
  .deploy-field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .deploy-label {
    font-size: 0.6875rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-tertiary);
    font-weight: 600;
  }
  .deploy-textarea,
  .deploy-input {
    inline-size: 100%;
    box-sizing: border-box;
    padding: 0.5rem 0.625rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    font: inherit;
    font-size: 0.8125rem;
    resize: vertical;
  }
  .deploy-textarea:focus,
  .deploy-input:focus {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
    border-color: var(--accent);
  }
  .deploy-input[aria-invalid='true'] {
    border-color: var(--danger);
  }
  .deploy-field-err {
    color: var(--danger);
    font-size: 0.6875rem;
  }
  .deploy-diff {
    max-block-size: 24rem;
    overflow: auto;
  }
</style>
