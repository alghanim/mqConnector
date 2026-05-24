// TransformEditor tests — two cases:
//
//   1. Renders the wrapped TransformListEditor's empty-state when
//      there are no transforms + the wrapper's help string.
//   2. Pre-populated transforms render in the row UI (verifies the
//      wrap-and-bind passes through correctly).
import { describe, expect, it } from 'vitest';
import { render } from '@testing-library/svelte';
import TransformEditor from './TransformEditor.svelte';
import type { Transform } from '$lib/api';

const rows: Transform[] = [
  {
    transform_type: 'rename',
    source_path: 'a',
    target_path: 'b',
    mask_pattern: '',
    mask_replace: '',
    set_value: '',
    order: 1
  },
  {
    transform_type: 'mask',
    source_path: 'c',
    target_path: '',
    mask_pattern: '.*',
    mask_replace: 'X',
    set_value: '',
    order: 2
  }
];

describe('TransformEditor', () => {
  it('renders empty-state message when there are no transforms', () => {
    const { getByText } = render(TransformEditor, {
      props: { config: '{}', valid: true, transforms: [] }
    });
    // Empty-state comes from the wrapped TransformListEditor.
    expect(getByText(/No transforms/i)).toBeInTheDocument();
    // Help string is from our wrapper.
    expect(getByText(/transform list below/i)).toBeInTheDocument();
  });

  it('renders pre-populated transforms via the wrapped list editor', () => {
    const { container } = render(TransformEditor, {
      props: { config: '{}', valid: true, transforms: rows }
    });
    // Two row containers from TransformListEditor.
    const txRows = container.querySelectorAll('.tx-row');
    expect(txRows.length).toBe(2);
  });

  it('exposes the Advanced JSON escape hatch', () => {
    const { container } = render(TransformEditor, {
      props: { config: '{"future_only":1}', valid: true, transforms: [] }
    });
    const details = container.querySelector('details');
    expect(details).toBeTruthy();
    const textarea = details?.querySelector('textarea') as HTMLTextAreaElement;
    // Unknown keys round-trip through the editor (legacy parity).
    expect(textarea.value).toContain('future_only');
  });
});
