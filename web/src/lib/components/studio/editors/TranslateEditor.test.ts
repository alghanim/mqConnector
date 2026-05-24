// TranslateEditor tests — three cases:
//
//   1. Renders the format dropdown with the four options.
//   2. Switching to 'protobuf' reveals the proto_message input.
//   3. Advanced JSON escape hatch preserves unknown keys.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import TranslateEditor from './TranslateEditor.svelte';

beforeEach(() => {
  globalThis.fetch = vi.fn(async () =>
    new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } })
  ) as unknown as typeof fetch;
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('TranslateEditor', () => {
  it('renders the format dropdown options', () => {
    const { getByText } = render(TranslateEditor, {
      props: { config: '{"output_format":"same"}', valid: true }
    });
    expect(getByText('JSON')).toBeInTheDocument();
    expect(getByText('XML')).toBeInTheDocument();
    expect(getByText('Protobuf')).toBeInTheDocument();
  });

  it('switching to protobuf reveals the proto_message input', async () => {
    const { container, queryByText } = render(TranslateEditor, {
      props: { config: '{"output_format":"same"}', valid: true }
    });
    expect(queryByText(/Proto message/i)).toBeNull();
    const select = container.querySelector('select') as HTMLSelectElement;
    select.value = 'protobuf';
    await fireEvent.change(select);
    await waitFor(() => expect(queryByText(/Proto message/i)).not.toBeNull());
  });

  it('Advanced JSON escape hatch preserves unknown keys', () => {
    const { container } = render(TranslateEditor, {
      props: { config: '{"output_format":"json","future_flag":"x"}', valid: true }
    });
    const textarea = container.querySelector('details textarea') as HTMLTextAreaElement;
    expect(textarea.value).toContain('future_flag');
  });
});
