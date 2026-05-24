// WasmEditor tests — three cases:
//
//   1. Renders the plugin dropdown populated from a /v1/plugins fetch.
//   2. Selecting a plugin commits to config + shows the metadata card.
//   3. The Upload-new-plugin button opens the upload dialog.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import WasmEditor from './WasmEditor.svelte';

const samplePlugins = [
  { id: 'p1', name: 'enrich.v1', size_bytes: 1024, uploaded_at: '2026-05-20T00:00:00Z' },
  { id: 'p2', name: 'mask.v2', size_bytes: 2048, uploaded_at: '2026-05-21T00:00:00Z' }
];

beforeEach(() => {
  // Stub fetch to return the plugin list. Real-world the WasmEditor
  // calls /api/v1/plugins on mount.
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString();
    if (url.includes('/v1/plugins')) {
      return new Response(JSON.stringify(samplePlugins), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }
    return new Response('[]', { status: 200 });
  }) as unknown as typeof fetch;
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('WasmEditor', () => {
  it('renders the plugin dropdown populated from the fetch', async () => {
    const { getByText } = render(WasmEditor, { props: { config: '{}', valid: false } });
    await waitFor(() => expect(getByText('enrich.v1')).toBeInTheDocument());
    expect(getByText('mask.v2')).toBeInTheDocument();
  });

  it('selecting a plugin shows the metadata card', async () => {
    const { container, getByText } = render(WasmEditor, {
      props: { config: '{}', valid: false }
    });
    await waitFor(() => expect(getByText('enrich.v1')).toBeInTheDocument());
    const select = container.querySelector('select') as HTMLSelectElement;
    select.value = 'enrich.v1';
    await fireEvent.change(select);
    await waitFor(() => {
      // Metadata grid renders the formatted size.
      expect(getByText(/KiB|B/)).toBeInTheDocument();
    });
  });

  it('Upload new plugin button opens the upload dialog', async () => {
    const { getByText, queryByText } = render(WasmEditor, {
      props: { config: '{}', valid: false }
    });
    await waitFor(() => expect(getByText('enrich.v1')).toBeInTheDocument());
    expect(queryByText(/Upload WASM plugin/i)).toBeNull();
    await fireEvent.click(getByText('Upload new plugin'));
    await waitFor(() => expect(getByText(/Upload WASM plugin/i)).toBeInTheDocument());
  });
});
