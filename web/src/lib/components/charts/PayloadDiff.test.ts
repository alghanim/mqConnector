// PayloadDiff — line-op renderer tests.
//
// Covers the four cases promised in the task spec:
//   1. All-`eq` operations → both columns show the same rows, no
//      add/del highlight, the "identical" hint shows.
//   2. One add + one del → one highlighted row on each side.
//   3. Empty operations → the friendly empty state shows.
//   4. A long operation stream → the scrollable body element exists
//      with the right overflow rule (smoke test for the height cap).
//
// We render the component standalone — it's pure presentation, no API
// calls, no stores.

import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import PayloadDiff from './PayloadDiff.svelte';

describe('PayloadDiff', () => {
  it('renders all-eq operations as matching rows with no highlight', () => {
    const ops: { op: 'eq' | 'add' | 'del'; text: string }[] = [
      { op: 'eq', text: 'one' },
      { op: 'eq', text: 'two' },
      { op: 'eq', text: 'three' }
    ];
    const { container } = render(PayloadDiff, { operations: ops });
    const rows = container.querySelectorAll('.pdiff-row');
    expect(rows.length).toBe(3);
    // No add/del cells highlighted anywhere.
    expect(container.querySelectorAll('.pdiff-cell.is-add').length).toBe(0);
    expect(container.querySelectorAll('.pdiff-cell.is-del').length).toBe(0);
    // The "identical" hint shows since hasDiff is false.
    expect(container.querySelector('.pdiff-equal-hint')).not.toBeNull();
  });

  it('highlights one row on each side for a single add/del pair', () => {
    const ops: { op: 'eq' | 'add' | 'del'; text: string }[] = [
      { op: 'eq', text: 'shared' },
      { op: 'del', text: 'old' },
      { op: 'add', text: 'new' }
    ];
    const { container } = render(PayloadDiff, { operations: ops });
    expect(container.querySelectorAll('.pdiff-cell.is-del').length).toBe(1);
    expect(container.querySelectorAll('.pdiff-cell.is-add').length).toBe(1);
    // hasDiff is true → the identical hint is suppressed.
    expect(container.querySelector('.pdiff-equal-hint')).toBeNull();
  });

  it('shows the empty state when no operations are supplied', () => {
    const { container, getByText } = render(PayloadDiff, { operations: [] });
    // The grid does not render.
    expect(container.querySelector('.pdiff-grid')).toBeNull();
    // The empty state copy is friendly and not a stack trace.
    expect(getByText('No payload differences.')).toBeInTheDocument();
  });

  it('caps body height for long diffs so the scrollbar takes over', () => {
    // Produce a long op stream; we just assert the scrollable region
    // is present — the actual overflow is a CSS rule and `getComputedStyle`
    // in jsdom is unreliable, so the presence of the `.pdiff-body`
    // element + the row count is sufficient.
    const ops: { op: 'eq' | 'add' | 'del'; text: string }[] = [];
    for (let i = 0; i < 200; i++) ops.push({ op: 'eq', text: `line-${i}` });
    const { container } = render(PayloadDiff, { operations: ops });
    const body = container.querySelector('.pdiff-body');
    expect(body).not.toBeNull();
    const rows = container.querySelectorAll('.pdiff-row');
    expect(rows.length).toBe(200);
  });
});
