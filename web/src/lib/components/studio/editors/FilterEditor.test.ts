// FilterEditor tests — three cases:
//
//   1. Renders existing paths as chips from the parsed JSON config.
//   2. Adding a path via the Add button updates the bound config.
//   3. Advanced JSON fallback preserves unknown fields (forward-compat).
import { describe, expect, it } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import FilterEditor from './FilterEditor.svelte';

describe('FilterEditor', () => {
  it('renders existing paths from the config as chips', () => {
    const { getByText } = render(FilterEditor, {
      props: { config: '{"paths":["a.b","c.d"]}', valid: true }
    });
    expect(getByText('a.b')).toBeInTheDocument();
    expect(getByText('c.d')).toBeInTheDocument();
  });

  it('adding a path updates the chip list', async () => {
    const { getByText, getByPlaceholderText } = render(FilterEditor, {
      props: { config: '{"paths":[]}', valid: true }
    });
    const input = getByPlaceholderText(/customer\.secret/i) as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'new.path' } });
    await fireEvent.click(getByText('Add path'));
    // The chip appears via two-way binding + reactive re-render — same
    // contract StageConfigForm.test asserts.
    await waitFor(() => expect(getByText('new.path')).toBeInTheDocument());
  });

  it('the Advanced details preserves unknown JSON fields', () => {
    const initial = '{"paths":["a"],"future_flag":42}';
    const { container } = render(FilterEditor, {
      props: { config: initial, valid: true }
    });
    // The <details> escape hatch is present (closed by default).
    const details = container.querySelector('details');
    expect(details).toBeTruthy();
    // The raw textarea value mirrors the original config (including
    // the future_flag we don't model in the structured form).
    const textarea = details?.querySelector('textarea') as HTMLTextAreaElement;
    expect(textarea.value).toContain('future_flag');
  });
});
