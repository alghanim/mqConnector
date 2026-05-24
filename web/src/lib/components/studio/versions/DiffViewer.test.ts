// DiffViewer tests — four cases:
//
//   1. Renders pipeline_fields entries in the table.
//   2. Renders Added / Removed / Modified stage cards.
//   3. Empty diff (all sections empty + no schema_version) → "No
//      differences between rev #A and rev #B" empty state.
//   4. The Rollback CTA calls onRollback with revA.
import { describe, expect, it, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import DiffViewer, { type SnapshotDiff } from './DiffViewer.svelte';

function emptyDiff(): SnapshotDiff {
  return {
    pipeline_fields: [],
    stages: { added: [], removed: [], modified: [] },
    transforms: { added: [], removed: [], modified: [] },
    routing_rules: { added: [], removed: [], modified: [] }
  };
}

describe('DiffViewer', () => {
  it('renders pipeline_fields entries with path / before / after', () => {
    const diff: SnapshotDiff = {
      ...emptyDiff(),
      pipeline_fields: [
        { path: 'name', before: 'old-name', after: 'new-name' },
        { path: 'enabled', before: true, after: false }
      ]
    };
    const { getByText, container } = render(DiffViewer, {
      props: { revA: 1, revB: 2, diff }
    });
    expect(getByText(/Pipeline fields/i)).toBeInTheDocument();
    // Paths render as monospace cells.
    expect(getByText('name')).toBeInTheDocument();
    expect(getByText('enabled')).toBeInTheDocument();
    // Before/After cells contain stringified JSON. `getByText` is
    // strict, so we assert via the table cells' text content.
    const cells = container.querySelectorAll('td.diff-val pre');
    const texts = Array.from(cells).map((c) => c.textContent || '');
    expect(texts.some((t) => t.includes('old-name'))).toBe(true);
    expect(texts.some((t) => t.includes('new-name'))).toBe(true);
  });

  it('renders Added / Removed / Modified stage cards', () => {
    const diff: SnapshotDiff = {
      ...emptyDiff(),
      stages: {
        added: [{ id: 'a', order: 1, value: { stage_type: 'filter' } }],
        removed: [{ id: 'b', order: 2, value: { stage_type: 'route' } }],
        modified: [
          {
            id: 'c',
            before_id: 'c0',
            order: 3,
            before: { stage_type: 'script', enabled: true },
            after: { stage_type: 'script', enabled: false }
          }
        ]
      }
    };
    const { getByText } = render(DiffViewer, {
      props: { revA: 4, revB: 5, diff }
    });
    expect(getByText(/Stages/)).toBeInTheDocument();
    expect(getByText(/Added/)).toBeInTheDocument();
    expect(getByText(/Removed/)).toBeInTheDocument();
    expect(getByText(/Modified/)).toBeInTheDocument();
    // The added card summarises by stage_type.
    expect(getByText('filter')).toBeInTheDocument();
    expect(getByText('route')).toBeInTheDocument();
  });

  it('renders "No differences" when every section is empty', () => {
    const { getByText } = render(DiffViewer, {
      props: { revA: 7, revB: 8, diff: emptyDiff() }
    });
    expect(getByText(/No differences/i)).toBeInTheDocument();
  });

  it('Rollback button calls onRollback with revA when revA < revB', async () => {
    const onRollback = vi.fn();
    const diff: SnapshotDiff = {
      ...emptyDiff(),
      pipeline_fields: [{ path: 'name', before: 'a', after: 'b' }]
    };
    const { getByText } = render(DiffViewer, {
      props: { revA: 3, revB: 5, diff, onRollback }
    });
    const btn = getByText(/Rollback to this revision/i);
    await fireEvent.click(btn);
    expect(onRollback).toHaveBeenCalledTimes(1);
    expect(onRollback).toHaveBeenCalledWith(3);
  });
});
