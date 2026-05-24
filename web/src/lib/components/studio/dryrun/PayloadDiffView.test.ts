// PayloadDiffView tests — the hand-rolled LCS diff modal.
//
// Three cases cover the spec:
//   1. equal inputs → no rows are flagged as add/del
//   2. single-line change → one del + one add row
//   3. ESC dispatches close
//
// We render the dialog with `open=true` so the diff machinery actually
// runs; the Dialog primitive handles ESC via a window keydown listener.
import { afterEach, describe, expect, it } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import PayloadDiffView from './PayloadDiffView.svelte';

afterEach(() => {
  // Dialog locks document.documentElement.style.overflow when open; the
  // shared setup.ts resets data-theme but not overflow. Reset here so
  // the next test starts in a clean state.
  document.documentElement.style.overflow = '';
});

describe('PayloadDiffView', () => {
  it('renders equal inputs with no add/del rows', () => {
    const text = 'line1\nline2\nline3';
    const { container } = render(PayloadDiffView, {
      open: true,
      before: text,
      after: text,
      format: 'JSON'
    });
    // Every row's left cell must NOT have the is-del class, and right
    // cell must NOT have is-add. We also assert the "equal" hint shows.
    expect(container.querySelector('.diff-cell.is-del')).toBeNull();
    expect(container.querySelector('.diff-cell.is-add')).toBeNull();
    expect(container.querySelector('.diff-equal')).not.toBeNull();
    // 3 rows rendered (one per line).
    const rows = container.querySelectorAll('.diff-row');
    expect(rows.length).toBe(3);
  });

  it('marks a changed line with one del + one add', () => {
    const before = 'a\nb\nc';
    const after = 'a\nB\nc';
    const { container } = render(PayloadDiffView, {
      open: true,
      before,
      after,
      format: 'JSON'
    });
    // One del row (for "b") and one add row (for "B"). The LCS walk
    // emits them as separate rows.
    expect(container.querySelectorAll('.diff-cell.is-del').length).toBe(1);
    expect(container.querySelectorAll('.diff-cell.is-add').length).toBe(1);
    // The equal hint must be absent because differences exist.
    expect(container.querySelector('.diff-equal')).toBeNull();
  });

  it('ESC dispatches close', async () => {
    let closed = false;
    render(PayloadDiffView, {
      props: { open: true, before: 'x', after: 'y' },
      events: { close: () => (closed = true) }
    });
    // Dialog's keydown listener is on `svelte:window`. Fire on window
    // so the listener picks it up.
    await fireEvent.keyDown(window, { key: 'Escape' });
    expect(closed).toBe(true);
  });
});
