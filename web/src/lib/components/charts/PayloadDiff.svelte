<!--
  PayloadDiff — inline side-by-side line-diff viewer that consumes a
  server-computed `[{op, text}]` operation stream.

  This is the lightweight cousin of studio/dryrun/PayloadDiffView.svelte:
  that one is a modal that runs an in-browser LCS over two raw strings;
  this one renders a sequence of ops that the backend already produced
  (GET /api/v1/dlq/{id}/diff). Different shape, different use case —
  keeping both is cheaper than retrofitting one into the other.

  Layout
  ──────
  Two-column grid:
    * Left  = `eq` + `del` lines. `del` rows tinted with `--danger-bg`.
    * Right = `eq` + `add` lines. `add` rows tinted with `--success-bg`.
    * `eq`  appears on BOTH sides (so unchanged context aligns).
    * `add` shows a blank row on the left; `del` blanks on the right.

  Line numbers are gutter-rendered on the side that actually owns the
  line — blank on the synthetic-opposite row. Numbering restarts per
  side (left counts del+eq; right counts add+eq) so the result reads
  like a unified-diff side-by-side.

  Empty operations → friendly empty-state copy. Server returning an
  all-`eq` diff is NOT empty — that's "no payload differences".

  Constraints applied:
    - Tailwind + brand tokens only; no raw hex.
    - Logical CSS for any directional spacing.
    - No third-party dep.
-->
<script lang="ts">
  export let operations: { op: 'eq' | 'add' | 'del'; text: string }[] = [];
  /** Optional headline above the left column. */
  export let leftLabel = '';
  /** Optional headline above the right column. */
  export let rightLabel = '';

  // Build the rendered row list. Each row carries the text + line
  // numbers for both sides; either side may be blank. We walk through
  // the operation stream once, advancing the appropriate side counters.
  type Row = {
    op: 'eq' | 'add' | 'del';
    left: string;
    right: string;
    leftNo?: number;
    rightNo?: number;
  };

  $: rows = (() => {
    const out: Row[] = [];
    let leftNo = 1;
    let rightNo = 1;
    for (const op of operations) {
      if (op.op === 'eq') {
        out.push({ op: 'eq', left: op.text, right: op.text, leftNo, rightNo });
        leftNo++;
        rightNo++;
      } else if (op.op === 'del') {
        out.push({ op: 'del', left: op.text, right: '', leftNo });
        leftNo++;
      } else if (op.op === 'add') {
        out.push({ op: 'add', left: '', right: op.text, rightNo });
        rightNo++;
      }
    }
    return out;
  })();

  // hasDiff = the stream contains at least one non-`eq` op. Used to
  // print a small "identical" hint above the grid when both sides
  // collapsed to the same content.
  $: hasDiff = operations.some((o) => o.op !== 'eq');
</script>

<div class="pdiff" data-testid="payload-diff">
  {#if operations.length === 0}
    <p class="pdiff-empty" role="status">No payload differences.</p>
  {:else}
    <div class="pdiff-meta">
      {#if !hasDiff}
        <span class="pdiff-equal-hint">Payloads are identical.</span>
      {/if}
    </div>
    <div
      class="pdiff-grid"
      role="table"
      aria-label="Payload diff"
    >
      <div class="pdiff-head" role="row">
        <span class="pdiff-head-cell" role="columnheader">{leftLabel || 'Before'}</span>
        <span class="pdiff-head-cell" role="columnheader">{rightLabel || 'After'}</span>
      </div>
      <div class="pdiff-body" role="rowgroup">
        {#each rows as row, idx (idx)}
          <div class="pdiff-row" role="row" data-op={row.op}>
            <div
              class="pdiff-cell pdiff-cell-left"
              class:is-del={row.op === 'del'}
              role="cell"
            >
              <span class="pdiff-no">{row.leftNo ?? ''}</span>
              <span class="pdiff-text">{row.left}</span>
            </div>
            <div
              class="pdiff-cell pdiff-cell-right"
              class:is-add={row.op === 'add'}
              role="cell"
            >
              <span class="pdiff-no">{row.rightNo ?? ''}</span>
              <span class="pdiff-text">{row.right}</span>
            </div>
          </div>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .pdiff {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    inline-size: 100%;
  }
  .pdiff-empty {
    margin: 0;
    padding: 0.75rem;
    text-align: center;
    color: var(--text-muted);
    font-size: 0.8125rem;
    font-style: italic;
    background: var(--surface-2);
    border: 1px dashed var(--border);
    border-radius: 8px;
  }
  .pdiff-meta {
    display: flex;
    justify-content: flex-end;
    min-block-size: 1rem;
    font-size: 0.75rem;
  }
  .pdiff-equal-hint {
    color: var(--success);
  }
  .pdiff-grid {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
    /* Vertical scroll lives inside .pdiff-body — see below. */
  }
  .pdiff-head {
    display: grid;
    grid-template-columns: 1fr 1fr;
    background: var(--surface-high);
    border-block-end: 1px solid var(--border);
  }
  .pdiff-head-cell {
    padding: 0.375rem 0.625rem;
    font-size: 0.6875rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-tertiary);
    font-weight: 600;
  }
  .pdiff-head-cell + .pdiff-head-cell {
    border-inline-start: 1px solid var(--border);
  }
  .pdiff-body {
    overflow: auto;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.75rem;
    line-height: 1.45;
    /* Hard ceiling so a large diff never blows out the drawer. */
    max-block-size: 24rem;
  }
  .pdiff-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
  }
  .pdiff-cell {
    display: grid;
    grid-template-columns: 2.25rem 1fr;
    align-items: start;
    padding-block: 0.125rem;
    padding-inline: 0.375rem 0.5rem;
    color: var(--text);
    border-block-end: 1px solid var(--border);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .pdiff-cell + .pdiff-cell {
    border-inline-start: 1px solid var(--border);
  }
  .pdiff-cell.is-del {
    background: var(--danger-bg);
  }
  .pdiff-cell.is-add {
    background: var(--success-bg);
  }
  .pdiff-no {
    color: var(--text-tertiary);
    user-select: none;
    text-align: end;
    padding-inline-end: 0.5rem;
  }
  .pdiff-text {
    /* Force a min-height so a blank "synthetic" cell still claims a
       row's worth of vertical space and stays aligned with the other
       side's content. */
    min-block-size: 1em;
  }
</style>
