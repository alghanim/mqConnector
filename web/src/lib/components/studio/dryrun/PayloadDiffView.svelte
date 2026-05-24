<!--
  PayloadDiffView — modal that shows a side-by-side line diff between
  two stage bodies (the one a user clicked vs the previous stage's
  output). Wired into <StageOutcomeStrip> via the studio dry-run dock.

  Diff algorithm: hand-rolled Longest-Common-Subsequence. We deliberately
  do NOT pull in a third-party diff library — the studio is air-gapped
  and the runtime budget is tight. The LCS implementation is the
  classic two-pass dynamic program:

    1. Split both strings on '\n' into line arrays L (before) and R
       (after).
    2. Build a (|L|+1) × (|R|+1) integer table where dp[i][j] is the
       length of the LCS of L[i:] and R[j:].
    3. Walk back from (0,0) emitting one step per call:
         - equal lines      → {op:'eq', text:L[i]}      (advance both)
         - line only in L   → {op:'del', text:L[i]}     (advance i)
         - line only in R   → {op:'add', text:R[j]}     (advance j)
    4. Render two columns: left shows L lines (with 'del' tinted red),
       right shows R lines (with 'add' tinted green). Unchanged lines
       align by emitting a placeholder in the opposite column.

  Complexity: O(|L| × |R|) time + memory. Stage bodies are typically a
  few hundred lines max — within budget. For very large payloads we
  cap at 10k lines per side and render a notice; this keeps the dialog
  from hanging the UI on a 5 MB payload someone pasted into the
  sample picker.

  Token + a11y choices:
    - Diff tints use `--success-bg` / `--danger-bg` (theme-aware tokens)
      so light + dark themes both read correctly.
    - The dialog is the existing <Dialog> primitive — it handles ESC,
      focus trap, scrim, and restore-focus for us.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import { locale, t } from '$lib/stores/locale';

  export let open = false;
  export let before = '';
  export let after = '';
  export let format = '';
  export let beforeLabel = '';
  export let afterLabel = '';

  const dispatch = createEventDispatcher<{ close: void }>();

  // Hard cap on lines per side. 10k matches the back-end's typical
  // stage payload budget; anything larger is a sample-size mistake on
  // the operator's side, not a diff to look at line-by-line.
  const MAX_LINES = 10_000;

  type DiffOp = 'eq' | 'add' | 'del';
  type DiffStep = { op: DiffOp; left: string; right: string; leftNo?: number; rightNo?: number };

  function splitLines(s: string): string[] {
    if (!s) return [];
    // Keep the original line endings out of the comparison — we render
    // one row per line and the trailing '\n' would otherwise show as
    // empty rows at the end.
    return s.split('\n');
  }

  function lcsTable(l: string[], r: string[]): number[][] {
    // dp[i][j] = LCS length of l[i..] and r[j..]. Allocate one row at a
    // time to keep small allocations cheap.
    const m = l.length;
    const n = r.length;
    const dp: number[][] = Array.from({ length: m + 1 }, () => new Array<number>(n + 1).fill(0));
    for (let i = m - 1; i >= 0; i--) {
      for (let j = n - 1; j >= 0; j--) {
        if (l[i] === r[j]) {
          dp[i][j] = dp[i + 1][j + 1] + 1;
        } else {
          dp[i][j] = dp[i + 1][j] >= dp[i][j + 1] ? dp[i + 1][j] : dp[i][j + 1];
        }
      }
    }
    return dp;
  }

  // walk emits the diff as a flat sequence of rendered rows, each row
  // carrying the text for the left column, the right column, or both
  // (when the line is unchanged). Line numbers are populated only for
  // the side(s) the row actually occupies — the opposite column's
  // line number stays undefined so we render a blank gutter cell.
  function diff(beforeText: string, afterText: string): DiffStep[] {
    const l = splitLines(beforeText).slice(0, MAX_LINES);
    const r = splitLines(afterText).slice(0, MAX_LINES);
    const dp = lcsTable(l, r);
    const out: DiffStep[] = [];
    let i = 0;
    let j = 0;
    let leftNo = 1;
    let rightNo = 1;
    while (i < l.length && j < r.length) {
      if (l[i] === r[j]) {
        out.push({ op: 'eq', left: l[i], right: r[j], leftNo, rightNo });
        i++;
        j++;
        leftNo++;
        rightNo++;
      } else if (dp[i + 1][j] >= dp[i][j + 1]) {
        out.push({ op: 'del', left: l[i], right: '', leftNo });
        i++;
        leftNo++;
      } else {
        out.push({ op: 'add', left: '', right: r[j], rightNo });
        j++;
        rightNo++;
      }
    }
    while (i < l.length) {
      out.push({ op: 'del', left: l[i], right: '', leftNo });
      i++;
      leftNo++;
    }
    while (j < r.length) {
      out.push({ op: 'add', left: '', right: r[j], rightNo });
      j++;
      rightNo++;
    }
    return out;
  }

  // Cap-aware truncation flag — surfaced as a banner above the diff when
  // either side blew the line limit so the operator knows the rendered
  // diff isn't the full story.
  $: truncated = (() => {
    const lc = splitLines(before).length;
    const rc = splitLines(after).length;
    return lc > MAX_LINES || rc > MAX_LINES;
  })();

  $: steps = open ? diff(before, after) : ([] as DiffStep[]);
  $: hasDiff = steps.some((s) => s.op !== 'eq');

  function onCancel() {
    dispatch('close');
  }
</script>

<Dialog
  {open}
  title={t($locale, 'studio.dryrun.diff.title')}
  cancelLabel={t($locale, 'studio.dryrun.diff.close')}
  confirmLabel={t($locale, 'studio.dryrun.diff.close')}
  on:cancel={onCancel}
  on:confirm={onCancel}
>
  <div class="diff-meta">
    <span class="diff-format">{format || 'text'}</span>
    {#if !hasDiff}
      <span class="diff-equal">{t($locale, 'studio.dryrun.diff.equal')}</span>
    {/if}
  </div>
  {#if truncated}
    <p class="diff-truncated" role="status">
      {t($locale, 'studio.dryrun.diff.truncated')}
    </p>
  {/if}
  <div class="diff-grid" role="table" aria-label={t($locale, 'studio.dryrun.diff.title')}>
    <div class="diff-head" role="row">
      <span class="diff-head-cell" role="columnheader">{beforeLabel || t($locale, 'studio.dryrun.diff.before')}</span>
      <span class="diff-head-cell" role="columnheader">{afterLabel || t($locale, 'studio.dryrun.diff.after')}</span>
    </div>
    <div class="diff-body" role="rowgroup">
      {#each steps as step, idx (idx)}
        <div class="diff-row" role="row" data-op={step.op}>
          <div class="diff-cell diff-cell-left" class:is-del={step.op === 'del'} role="cell">
            <span class="diff-no">{step.leftNo ?? ''}</span>
            <span class="diff-text">{step.left}</span>
          </div>
          <div class="diff-cell diff-cell-right" class:is-add={step.op === 'add'} role="cell">
            <span class="diff-no">{step.rightNo ?? ''}</span>
            <span class="diff-text">{step.right}</span>
          </div>
        </div>
      {/each}
    </div>
  </div>
</Dialog>

<style>
  .diff-meta {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    margin-block-end: 0.5rem;
    font-size: 0.75rem;
    color: var(--text-muted);
  }
  .diff-format {
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 600;
    padding-block: 0.125rem;
    padding-inline: 0.5rem;
    border-radius: 999px;
    background: var(--surface-high);
    color: var(--text);
  }
  .diff-equal {
    color: var(--success);
  }
  .diff-truncated {
    margin: 0 0 0.5rem;
    padding: 0.5rem;
    background: var(--warning-bg);
    color: var(--warning);
    border-radius: 8px;
    font-size: 0.75rem;
  }
  .diff-grid {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
    /* Hard ceiling on dialog body height: Dialog already caps at
       viewport-32px, but the diff itself should scroll inside. */
    max-block-size: 60vh;
  }
  .diff-head {
    display: grid;
    grid-template-columns: 1fr 1fr;
    background: var(--surface-high);
    border-block-end: 1px solid var(--border);
  }
  .diff-head-cell {
    padding: 0.375rem 0.625rem;
    font-size: 0.6875rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-tertiary);
    font-weight: 600;
  }
  .diff-head-cell + .diff-head-cell {
    border-inline-start: 1px solid var(--border);
  }
  .diff-body {
    overflow: auto;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.75rem;
    line-height: 1.45;
  }
  .diff-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
  }
  .diff-cell {
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
  .diff-cell + .diff-cell {
    border-inline-start: 1px solid var(--border);
  }
  .diff-cell.is-del {
    background: var(--danger-bg);
  }
  .diff-cell.is-add {
    background: var(--success-bg);
  }
  .diff-no {
    color: var(--text-tertiary);
    user-select: none;
    text-align: end;
    padding-inline-end: 0.5rem;
  }
  .diff-text {
    /* Empty cells get a min-height so the row keeps its place. */
    min-block-size: 1em;
  }
</style>
