// Component tests for /settings.
//
// Two cards on the page: Export and Import. The export buttons
// trigger a download via Blob URL; the import card stages a file,
// runs dry-run, then applies. The tests verify:
//
//   • clicking Download YAML hits /api/v1/config/export?format=yaml
//     and creates an object URL the browser would download.
//   • dropping a file into the drop-zone (simulated via the staged
//     <input type=file>) shows the file row + Preview / Apply buttons.
//   • Preview hits /import?dry_run=true; on 200, the summary chip
//     renders the connection / pipeline counts.
//   • Apply hits /import without dry_run when the dry-run was OK.
//   • A 409 conflict response surfaces the server's error text.
//
// We stub URL.createObjectURL because jsdom doesn't ship a full
// implementation, and HTMLAnchorElement.click() so the test doesn't
// try to actually navigate.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import SettingsPage from './+page.svelte';

type FetchCall = { url: string; init: RequestInit; body?: string };

function stubFetch(handler: (url: string, init: RequestInit) => Response | Promise<Response>) {
  const calls: FetchCall[] = [];
  const spy = vi.fn(async (urlIn: string | URL | Request, init: RequestInit | undefined) => {
    const url = typeof urlIn === 'string' ? urlIn : urlIn.toString();
    const safeInit = init ?? {};
    calls.push({
      url,
      init: safeInit,
      body: typeof safeInit.body === 'string' ? safeInit.body : undefined
    });
    return handler(url, safeInit);
  });
  globalThis.fetch = spy as unknown as typeof fetch;
  return { spy, calls };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  });
}

beforeEach(() => {
  // jsdom doesn't implement URL.createObjectURL/revokeObjectURL.
  Object.defineProperty(URL, 'createObjectURL', {
    configurable: true,
    value: vi.fn(() => 'blob:fake-url')
  });
  Object.defineProperty(URL, 'revokeObjectURL', {
    configurable: true,
    value: vi.fn()
  });
  // Stop the temporary <a> the export helper appends from actually
  // navigating (jsdom would just log it, but better to be explicit).
  HTMLAnchorElement.prototype.click = vi.fn();
});

afterEach(() => {
  vi.restoreAllMocks();
});

// stageFile uses `await file.text()` (a FileReader microtask under
// our jsdom polyfill) before setting stagedText. Tests that click
// Preview / Apply right after a fake file-change need to give those
// microtasks a chance to run, or postImport's `!stagedText` guard
// silently bails. A short macro-task wait is the simplest reliable
// flush — Promise.resolve() loops don't always drain FileReader's
// callbacks.
async function flushMicrotasks(): Promise<void> {
  await new Promise((r) => setTimeout(r, 20));
}

describe('/settings page', () => {
  it('renders both Export and Import sections', async () => {
    const { findByText } = render(SettingsPage);
    expect(await findByText('Export configuration')).toBeInTheDocument();
    expect(await findByText('Import configuration')).toBeInTheDocument();
  });

  it('clicking "Download YAML" fetches the export endpoint and triggers a download', async () => {
    const { calls } = stubFetch(async (url) => {
      if (url === '/api/v1/config/export?format=yaml') {
        return new Response('version: 1\n', {
          status: 200,
          headers: { 'Content-Type': 'application/yaml' }
        });
      }
      throw new Error('unexpected: ' + url);
    });

    const { findByText } = render(SettingsPage);
    await fireEvent.click(await findByText('Download YAML'));

    // Wait for the WHOLE async chain (fetch → res.blob() →
    // URL.createObjectURL → a.click()) to settle. Just waiting on
    // fetch's call list would exit at the spy-recorded moment,
    // before res.blob() resolves on the next microtask — leaving the
    // downstream URL/click assertions racing.
    await waitFor(() => {
      expect(URL.createObjectURL).toHaveBeenCalled();
      expect(HTMLAnchorElement.prototype.click).toHaveBeenCalled();
    });
    // The export fetch must carry credentials so the session cookie
    // accompanies the request — we replaced a plain <a download>
    // link with a programmatic fetch precisely for this.
    const got = calls.find((c) => c.url === '/api/v1/config/export?format=yaml');
    expect(got).toBeTruthy();
    expect(got!.init.credentials).toBe('include');
  });

  it('staging a file shows Preview / Apply buttons and a clear handle', async () => {
    const { container, findByText } = render(SettingsPage);
    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    expect(fileInput).not.toBeNull();

    const file = new File([`version: 1\nconnections: []\npipelines: []\n`], 'cfg.yaml', {
      type: 'application/yaml'
    });
    // Programmatically stuff a file into the input + fire change. jsdom
    // doesn't simulate a drop's `DataTransfer.files`, but the drop-zone
    // shares its handler with the click-to-pick file input — covering
    // one path covers both.
    Object.defineProperty(fileInput, 'files', { configurable: true, value: [file] });
    await fireEvent.change(fileInput);

    expect(await findByText('cfg.yaml')).toBeInTheDocument();
    expect(await findByText('Preview (dry-run)')).toBeInTheDocument();
    expect(await findByText('Apply import')).toBeInTheDocument();
  });

  it('Preview hits /import?dry_run=true and surfaces the count summary', async () => {
    let lastURL = '';
    stubFetch(async (url, init) => {
      lastURL = url;
      if (url.startsWith('/api/v1/config/import') && init.method === 'POST') {
        return jsonResponse(
          {
            status: 'ok',
            connections: 3,
            pipelines: 2,
            dry_run: url.includes('dry_run=true')
          },
          200
        );
      }
      throw new Error('unexpected: ' + url);
    });

    const { container, findByText } = render(SettingsPage);
    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File([`{"version":1,"connections":[],"pipelines":[]}`], 'cfg.json', {
      type: 'application/json'
    });
    Object.defineProperty(fileInput, 'files', { configurable: true, value: [file] });
    await fireEvent.change(fileInput);

    // The page's stageFile() does `stagedText = await f.text()` — that
    // sets state two microtasks deep (FileReader → text resolver →
    // assignment). Flush them so postImport's `!stagedText` guard
    // doesn't silently no-op when we click Preview next.
    await flushMicrotasks();

    await fireEvent.click(await findByText('Preview (dry-run)'));

    await waitFor(() => {
      expect(lastURL).toContain('dry_run=true');
    });

    // Summary chip rendered.
    expect(await findByText('3')).toBeInTheDocument(); // connections count
    expect(await findByText('2')).toBeInTheDocument(); // pipelines count
    expect(await findByText('connections')).toBeInTheDocument();
    expect(await findByText('pipelines')).toBeInTheDocument();
  });

  it('Apply hits /import WITHOUT dry_run when invoked after a successful Preview', async () => {
    const { calls } = stubFetch(async (url, init) => {
      if (url.startsWith('/api/v1/config/import') && init.method === 'POST') {
        return jsonResponse(
          {
            status: 'imported',
            connections: 1,
            pipelines: 1,
            dry_run: url.includes('dry_run=true')
          },
          url.includes('dry_run=true') ? 200 : 201
        );
      }
      throw new Error('unexpected: ' + url);
    });

    const { container, findByText } = render(SettingsPage);
    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    Object.defineProperty(fileInput, 'files', {
      configurable: true,
      value: [
        new File([`{"version":1,"connections":[],"pipelines":[]}`], 'cfg.json', {
          type: 'application/json'
        })
      ]
    });
    await fireEvent.change(fileInput);
    await flushMicrotasks();

    // Must Preview first so dry-run state is set + Apply enables.
    await fireEvent.click(await findByText('Preview (dry-run)'));
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes('dry_run=true'))).toBe(true)
    );

    await fireEvent.click(await findByText('Apply import'));

    await waitFor(() => {
      const apply = calls.find(
        (c) => c.url === '/api/v1/config/import' && c.init.method === 'POST'
      );
      expect(apply).toBeTruthy();
    });
  });

  it('a 409 conflict surfaces the server error verbatim', async () => {
    stubFetch(async (url, init) => {
      if (url.startsWith('/api/v1/config/import') && init.method === 'POST') {
        return new Response(
          JSON.stringify({ error: 'connection name already exists: existing' }),
          { status: 409, headers: { 'Content-Type': 'application/json' } }
        );
      }
      throw new Error('unexpected: ' + url);
    });

    const { container, findByText } = render(SettingsPage);
    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    Object.defineProperty(fileInput, 'files', {
      configurable: true,
      value: [
        new File([`{"version":1,"connections":[{"name":"existing"}],"pipelines":[]}`], 'cfg.json', {
          type: 'application/json'
        })
      ]
    });
    await fireEvent.change(fileInput);
    await flushMicrotasks();
    await fireEvent.click(await findByText('Preview (dry-run)'));

    expect(await findByText(/connection name already exists/)).toBeInTheDocument();
  });
});
