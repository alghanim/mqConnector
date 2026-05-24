<!--
  DiffViewer — renders the structured SnapshotDiff returned by
  GET /api/v1/pipelines/{id}/revisions/{rev}/diff?against={other}.

  Layout:

      ┌──────────────────────────────────────────────────────────────┐
      │ Rev #A → Rev #B               [Rollback to rev #A]           │
      ├──────────────────────────────────────────────────────────────┤
      │ Pipeline fields                                              │
      │   path        before        after                            │
      ├──────────────────────────────────────────────────────────────┤
      │ Stages                                                       │
      │   Added      [card] [card]                                   │
      │   Removed    [card] (greyed)                                 │
      │   Modified   before-cfg │ after-cfg                          │
      ├──────────────────────────────────────────────────────────────┤
      │ Transforms — same shape                                      │
      │ Routing rules — same shape                                   │
      │ Schema version — only if changed                             │
      └──────────────────────────────────────────────────────────────┘

  Wire shape (matches snapshot_diff.go):
    type SnapshotDiff = {
      pipeline_fields: FieldChange[];
      stages: ChildDiff;
      transforms: ChildDiff;
      routing_rules: ChildDiff;
      schema_version?: FieldChange;
    }
    type FieldChange = { path: string; before?: unknown; after?: unknown };
    type ChildDiff = {
      added: ChildEntry[];
      removed: ChildEntry[];
      modified: ChildChange[];
    };
    type ChildEntry = { id: string; order: number; value: unknown };
    type ChildChange = {
      id: string; before_id?: string; order: number;
      before: unknown; after: unknown;
    };

  Rollback gate: the "Rollback to rev #A" CTA is only enabled when
  revA < revB — you can't roll *forward* to a future revision.

  Empty diff: when every section is empty AND schema_version is null,
  render "No differences between rev #A and rev #B" centered.
-->
<script context="module" lang="ts">
  // ─── Public wire shapes ───────────────────────────────────────────
  // Mirrored from internal/server/snapshot_diff.go. Exposed from a
  // module-context block so they're importable as named types from
  // sibling components (DeployDialog, Studio.svelte, VersionRail);
  // a Svelte instance <script> only allows `export let` prop bindings,
  // not type re-exports.
  export interface FieldChange {
    path: string;
    before?: unknown;
    after?: unknown;
  }
  export interface ChildEntry {
    id: string;
    order: number;
    value: unknown;
  }
  export interface ChildChange {
    id: string;
    before_id?: string;
    order: number;
    before: unknown;
    after: unknown;
  }
  export interface ChildDiff {
    added: ChildEntry[];
    removed: ChildEntry[];
    modified: ChildChange[];
  }
  export interface SnapshotDiff {
    pipeline_fields: FieldChange[];
    stages: ChildDiff;
    transforms: ChildDiff;
    routing_rules: ChildDiff;
    schema_version?: FieldChange | null;
  }
</script>

<script lang="ts">
  import { locale, t } from '$lib/stores/locale';

  export let revA: number;
  export let revB: number;
  export let diff: SnapshotDiff;
  /** Optional callback for the "Rollback to rev #A" CTA. */
  export let onRollback: ((toRev: number) => void) | undefined = undefined;

  // Pretty-print a JSON-ish value. The wire field is `unknown` because
  // Go's json.RawMessage is decoded into whatever the JSON parser
  // produces on the wire — for our diff that's usually a primitive
  // (string/number/bool) for pipeline_fields and an object for child
  // values. JSON.stringify handles all of them; the fallback returns
  // the raw value coerced to a string so a malformed payload never
  // crashes the viewer.
  function fmt(v: unknown): string {
    if (v === undefined || v === null) return '∅';
    try {
      return typeof v === 'string' ? v : JSON.stringify(v, null, 2);
    } catch {
      return String(v);
    }
  }

  // Compact one-line summary for an added/removed child. Pulls the
  // `stage_type` / `transform_type` / `condition_operator` field when
  // present so the card communicates intent without forcing the
  // operator to expand it.
  function summarise(value: unknown): string {
    if (!value || typeof value !== 'object') return fmt(value);
    const obj = value as Record<string, unknown>;
    if (typeof obj.stage_type === 'string') return obj.stage_type;
    if (typeof obj.transform_type === 'string') return obj.transform_type;
    if (typeof obj.condition_operator === 'string') return String(obj.condition_operator);
    return JSON.stringify(obj).slice(0, 80);
  }

  // Derived shape. `diff` is non-nullable by prop type, but defend
  // against undefined sub-fields in case a backend pre-dating §1.4
  // returns a partial shape — the viewer should still render rather
  // than crash.
  $: pipelineFields = diff?.pipeline_fields ?? [];
  $: stages = diff?.stages ?? { added: [], removed: [], modified: [] };
  $: transforms = diff?.transforms ?? { added: [], removed: [], modified: [] };
  $: routes = diff?.routing_rules ?? { added: [], removed: [], modified: [] };
  $: schemaVer = diff?.schema_version ?? null;

  $: isEmpty =
    pipelineFields.length === 0 &&
    stages.added.length === 0 &&
    stages.removed.length === 0 &&
    stages.modified.length === 0 &&
    transforms.added.length === 0 &&
    transforms.removed.length === 0 &&
    transforms.modified.length === 0 &&
    routes.added.length === 0 &&
    routes.removed.length === 0 &&
    routes.modified.length === 0 &&
    !schemaVer;

  // Rollback CTA gate — only sane to roll back to a *prior* revision.
  // We allow equal numbers for completeness (the parent should never
  // pass them), but disable forward rolls.
  $: canRollback = onRollback !== undefined && revA < revB;
</script>

<section class="diff" aria-label={`diff revision ${revA} to ${revB}`}>
  <header class="diff-head">
    <h3 class="diff-title">
      <span class="diff-rev">#{revA}</span>
      <span class="diff-arrow" aria-hidden="true">→</span>
      <span class="diff-rev">#{revB}</span>
    </h3>
    {#if canRollback}
      <button
        type="button"
        class="diff-rollback"
        on:click={() => onRollback?.(revA)}
      >
        {t($locale, 'studio.diff.rollbackCta')} #{revA}
      </button>
    {/if}
  </header>

  {#if isEmpty}
    <p class="diff-none">
      {t($locale, 'studio.diff.none')} #{revA} / #{revB}
    </p>
  {:else}
    {#if pipelineFields.length > 0}
      <section class="diff-section">
        <h4 class="diff-h">{t($locale, 'studio.diff.heading.pipelineFields')}</h4>
        <table class="diff-table">
          <thead>
            <tr>
              <th scope="col">{t($locale, 'studio.diff.path')}</th>
              <th scope="col">{t($locale, 'studio.diff.before')}</th>
              <th scope="col">{t($locale, 'studio.diff.after')}</th>
            </tr>
          </thead>
          <tbody>
            {#each pipelineFields as fc (fc.path)}
              <tr>
                <td class="diff-path">{fc.path}</td>
                <td class="diff-val diff-val-before"><pre>{fmt(fc.before)}</pre></td>
                <td class="diff-val diff-val-after"><pre>{fmt(fc.after)}</pre></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </section>
    {/if}

    {#each [
      { label: t($locale, 'studio.diff.heading.stages'), block: stages, kind: 'stages' },
      { label: t($locale, 'studio.diff.heading.transforms'), block: transforms, kind: 'transforms' },
      { label: t($locale, 'studio.diff.heading.routingRules'), block: routes, kind: 'routes' }
    ] as bucket (bucket.kind)}
      {#if bucket.block.added.length > 0 || bucket.block.removed.length > 0 || bucket.block.modified.length > 0}
        <section class="diff-section" data-kind={bucket.kind}>
          <h4 class="diff-h">{bucket.label}</h4>

          {#if bucket.block.added.length > 0}
            <div class="diff-sub" data-tone="added">
              <h5 class="diff-sub-h">{t($locale, 'studio.diff.added')}</h5>
              <ul class="diff-cards">
                {#each bucket.block.added as e (e.id || e.order)}
                  <li class="diff-card diff-card-added">
                    <div class="diff-card-top">
                      <span class="diff-card-order">{t($locale, 'studio.diff.order')} {e.order}</span>
                      <span class="diff-card-id">{e.id || '—'}</span>
                    </div>
                    <p class="diff-card-summary">{summarise(e.value)}</p>
                    <details class="diff-card-details">
                      <summary>JSON</summary>
                      <pre>{fmt(e.value)}</pre>
                    </details>
                  </li>
                {/each}
              </ul>
            </div>
          {/if}

          {#if bucket.block.removed.length > 0}
            <div class="diff-sub" data-tone="removed">
              <h5 class="diff-sub-h">{t($locale, 'studio.diff.removed')}</h5>
              <ul class="diff-cards">
                {#each bucket.block.removed as e (e.id || e.order)}
                  <li class="diff-card diff-card-removed">
                    <div class="diff-card-top">
                      <span class="diff-card-order">{t($locale, 'studio.diff.order')} {e.order}</span>
                      <span class="diff-card-id">{e.id || '—'}</span>
                    </div>
                    <p class="diff-card-summary">{summarise(e.value)}</p>
                    <details class="diff-card-details">
                      <summary>JSON</summary>
                      <pre>{fmt(e.value)}</pre>
                    </details>
                  </li>
                {/each}
              </ul>
            </div>
          {/if}

          {#if bucket.block.modified.length > 0}
            <div class="diff-sub" data-tone="modified">
              <h5 class="diff-sub-h">{t($locale, 'studio.diff.modified')}</h5>
              <ul class="diff-cards diff-cards-modified">
                {#each bucket.block.modified as c (c.id || c.order)}
                  <li class="diff-card diff-card-modified">
                    <div class="diff-card-top">
                      <span class="diff-card-order">{t($locale, 'studio.diff.order')} {c.order}</span>
                      <span class="diff-card-id">{c.id || '—'}</span>
                    </div>
                    <div class="diff-modified-grid">
                      <div class="diff-val diff-val-before">
                        <p class="diff-val-label">{t($locale, 'studio.diff.before')}</p>
                        <pre>{fmt(c.before)}</pre>
                      </div>
                      <div class="diff-val diff-val-after">
                        <p class="diff-val-label">{t($locale, 'studio.diff.after')}</p>
                        <pre>{fmt(c.after)}</pre>
                      </div>
                    </div>
                  </li>
                {/each}
              </ul>
            </div>
          {/if}
        </section>
      {/if}
    {/each}

    {#if schemaVer}
      <section class="diff-section">
        <h4 class="diff-h">{t($locale, 'studio.diff.heading.schemaVersion')}</h4>
        <p class="diff-schema-chip">
          v{fmt(schemaVer.before)} <span aria-hidden="true">→</span> v{fmt(schemaVer.after)}
        </p>
      </section>
    {/if}
  {/if}
</section>

<style>
  .diff {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    padding: 0.75rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 12px;
    color: var(--text);
  }
  .diff-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .diff-title {
    margin: 0;
    font-size: 0.875rem;
    font-weight: 600;
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .diff-rev {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    color: var(--text);
  }
  .diff-arrow {
    color: var(--text-tertiary);
  }
  .diff-rollback {
    display: inline-flex;
    align-items: center;
    padding-block: 0.375rem;
    padding-inline: 0.625rem;
    border-radius: 8px;
    background: var(--accent);
    color: var(--accent-on);
    border: 1px solid var(--accent);
    font-size: 0.75rem;
    font-weight: 600;
    cursor: pointer;
  }
  .diff-rollback:hover {
    background: var(--accent-hover);
  }
  .diff-none {
    margin: 0;
    padding: 2rem;
    text-align: center;
    color: var(--text-muted);
    font-style: italic;
  }

  .diff-section {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.625rem;
  }
  .diff-h {
    margin: 0;
    font-size: 0.6875rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-tertiary);
    font-weight: 700;
  }

  .diff-table {
    inline-size: 100%;
    border-collapse: collapse;
    font-size: 0.75rem;
  }
  .diff-table th,
  .diff-table td {
    text-align: start;
    vertical-align: top;
    padding: 0.375rem 0.5rem;
    border-block-end: 1px solid var(--border);
  }
  .diff-table th {
    color: var(--text-tertiary);
    font-weight: 600;
    text-transform: uppercase;
    font-size: 0.625rem;
    letter-spacing: 0.06em;
  }
  .diff-path {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    color: var(--text);
  }
  .diff-val pre {
    margin: 0;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.6875rem;
    line-height: 1.4;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .diff-val-before pre {
    color: var(--danger);
  }
  .diff-val-after pre {
    color: var(--success);
  }

  .diff-sub {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .diff-sub-h {
    margin: 0;
    font-size: 0.625rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-tertiary);
    font-weight: 700;
  }
  .diff-cards {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }
  .diff-card {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    padding: 0.5rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
  }
  .diff-card-added {
    border-inline-start: 3px solid var(--success);
  }
  .diff-card-removed {
    border-inline-start: 3px solid var(--danger);
    opacity: 0.75;
  }
  .diff-card-modified {
    border-inline-start: 3px solid var(--info);
  }
  .diff-card-top {
    display: flex;
    gap: 0.5rem;
    align-items: baseline;
  }
  .diff-card-order {
    font-size: 0.6875rem;
    font-weight: 600;
    color: var(--text);
  }
  .diff-card-id {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.625rem;
    color: var(--text-tertiary);
  }
  .diff-card-summary {
    margin: 0;
    font-size: 0.75rem;
    color: var(--text-muted);
  }
  .diff-card-details {
    margin: 0;
    font-size: 0.6875rem;
  }
  .diff-card-details summary {
    cursor: pointer;
    color: var(--text-tertiary);
    font-size: 0.625rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .diff-card-details pre {
    margin-block-start: 0.25rem;
    background: var(--bg);
    border-radius: 4px;
    padding: 0.375rem 0.5rem;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.6875rem;
    line-height: 1.4;
    white-space: pre-wrap;
    word-break: break-word;
    color: var(--text);
    max-block-size: 16rem;
    overflow: auto;
  }
  .diff-modified-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.375rem;
  }
  .diff-modified-grid .diff-val {
    background: var(--bg);
    border-radius: 4px;
    padding: 0.375rem 0.5rem;
  }
  .diff-val-label {
    margin: 0 0 0.125rem;
    font-size: 0.5625rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-tertiary);
    font-weight: 700;
  }

  .diff-schema-chip {
    margin: 0;
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding-block: 0.25rem;
    padding-inline: 0.5rem;
    background: var(--info-bg);
    color: var(--info);
    border: 1px solid var(--info);
    border-radius: 999px;
    font-size: 0.75rem;
    font-weight: 600;
    align-self: flex-start;
  }

  @media (max-inline-size: 720px) {
    .diff-modified-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
