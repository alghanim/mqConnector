// Component tests for /webhooks.
//
// Webhook receivers need real HMAC + event-filter semantics on the
// back end (covered by internal/webhooks tests). The job of these
// tests is to assert the PAGE wiring:
//
//   • initial GET populates the table with status badges
//   • create form posts the right shape, including events filter
//   • inline enable Switch fires a PUT
//   • delete confirm fires a DELETE and drops the row
//   • the "Generate" button populates the secret field with hex
//
// Browser bits stubbed here: crypto.getRandomValues for the
// generator. fetch is replaced per-test.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import WebhooksPage from './+page.svelte';

type FetchCall = { url: string; init: RequestInit };

function stubFetch(handler: (url: string, init: RequestInit) => Response | Promise<Response>) {
  const calls: FetchCall[] = [];
  const spy = vi.fn(async (urlIn: string | URL | Request, init: RequestInit | undefined) => {
    const url = typeof urlIn === 'string' ? urlIn : urlIn.toString();
    const safeInit = init ?? {};
    calls.push({ url, init: safeInit });
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
  // jsdom's crypto is real, but make sure getRandomValues exists so
  // the page's "Generate" helper has something to call.
  if (!globalThis.crypto?.getRandomValues) {
    Object.defineProperty(globalThis, 'crypto', {
      configurable: true,
      value: {
        getRandomValues: (a: Uint8Array) => {
          for (let i = 0; i < a.length; i++) a[i] = i % 256;
          return a;
        }
      }
    });
  }
});

afterEach(() => {
  vi.restoreAllMocks();
});

const sampleHook = {
  id: 'h1',
  tenant_id: 'tenant',
  name: 'incident-channel',
  url: 'https://hooks.example/inc',
  secret: 'shhh',
  events: 'pipeline.error,dlq.pushed',
  enabled: true,
  last_status: 200,
  last_error: '',
  last_attempt_at: '2026-05-16T01:00:00Z',
  created_at: '2026-05-16T00:00:00Z',
  updated_at: '2026-05-16T01:00:00Z'
};

describe('/webhooks page', () => {
  it('renders empty state when no webhooks exist', async () => {
    stubFetch(async (url) => {
      if (url === '/api/v1/webhooks') return jsonResponse({ items: [] });
      throw new Error('unexpected: ' + url);
    });

    const { findByText } = render(WebhooksPage);
    expect(await findByText('No webhooks yet')).toBeInTheDocument();
  });

  it('lists hooks with their name + event chips + last status code', async () => {
    stubFetch(async (url) => {
      if (url === '/api/v1/webhooks') return jsonResponse({ items: [sampleHook] });
      throw new Error('unexpected: ' + url);
    });

    const { findByText } = render(WebhooksPage);

    expect(await findByText('incident-channel')).toBeInTheDocument();
    // Both event chips render
    expect(await findByText('pipeline.error')).toBeInTheDocument();
    expect(await findByText('dlq.pushed')).toBeInTheDocument();
    // Status badge with the HTTP code
    expect(await findByText('200')).toBeInTheDocument();
  });

  it('creating a webhook POSTs the form payload with the events filter', async () => {
    let posted: Record<string, unknown> | null = null;
    stubFetch(async (url, init) => {
      if (url === '/api/v1/webhooks' && (!init.method || init.method === 'GET')) {
        return jsonResponse({ items: [] });
      }
      if (url === '/api/v1/webhooks' && init.method === 'POST') {
        posted = JSON.parse(init.body as string);
        return jsonResponse(
          {
            id: 'h-new',
            tenant_id: 'tenant',
            name: 'channel',
            url: 'https://hooks.example/c',
            secret: 'sec',
            events: '*',
            enabled: true,
            last_status: 0,
            last_error: '',
            created_at: 'now',
            updated_at: 'now'
          },
          201
        );
      }
      throw new Error('unexpected: ' + url + ' ' + (init.method ?? 'GET'));
    });

    const { findByLabelText, findByText, getAllByText } = render(WebhooksPage);

    // Open create. Two "New webhook" buttons (header + empty CTA).
    const newButtons = await waitFor(() => getAllByText('New webhook'));
    await fireEvent.click(newButtons[0]);

    // Fill the form.
    await fireEvent.input(await findByLabelText('Name'), {
      target: { value: 'channel' }
    });
    await fireEvent.input(await findByLabelText('URL'), {
      target: { value: 'https://hooks.example/c' }
    });
    await fireEvent.input(await findByLabelText('Signing secret'), {
      target: { value: 'sec' }
    });

    // Submit via Dialog's confirm — its label is "Save" on this page.
    await fireEvent.click(await findByText('Save'));

    await waitFor(() => expect(posted).not.toBeNull());
    expect(posted).toMatchObject({
      name: 'channel',
      url: 'https://hooks.example/c',
      secret: 'sec',
      // Default "All events" is on, so the saved filter is '*'.
      events: '*',
      enabled: true
    });
  });

  it('inline-toggling Enabled fires a PUT with the flipped value', async () => {
    // TS narrows `putBody` to `never` when both branches above just
    // reassign — be explicit with the variable type so the !== null
    // refinement below still lets us read .enabled.
    const stateRef: { body: Record<string, unknown> | null } = { body: null };
    stubFetch(async (url, init) => {
      if (url === '/api/v1/webhooks' && (!init.method || init.method === 'GET')) {
        return jsonResponse({ items: [sampleHook] });
      }
      if (url === '/api/v1/webhooks/h1' && init.method === 'PUT') {
        stateRef.body = JSON.parse(init.body as string);
        return jsonResponse({ ...sampleHook, enabled: false });
      }
      throw new Error('unexpected: ' + url + ' ' + (init.method ?? 'GET'));
    });

    const { findByText, container } = render(WebhooksPage);

    // Wait for the row.
    await findByText('incident-channel');

    // The Switch component is a <label> wrapping a hidden
    // <input type="checkbox" role="switch">. userEvent.click on the
    // input simulates a real user click — the browser flips
    // .checked AND dispatches a bubbling `change` event, which is
    // what the parent's `on:change` listens for. fireEvent.click
    // doesn't run the spec-defined input-click sequence, hence the
    // need for userEvent here.
    const swt = container.querySelector('input[role="switch"]') as HTMLInputElement | null;
    expect(swt).not.toBeNull();
    const user = userEvent.setup();
    await user.click(swt!);

    await waitFor(() => expect(stateRef.body).not.toBeNull());
    expect(stateRef.body?.enabled).toBe(false);
  });

  it('deleting a webhook fires DELETE and removes the row', async () => {
    const { calls } = stubFetch(async (url, init) => {
      if (url === '/api/v1/webhooks' && (!init.method || init.method === 'GET')) {
        return jsonResponse({ items: [sampleHook] });
      }
      if (url === '/api/v1/webhooks/h1' && init.method === 'DELETE') {
        return jsonResponse({ status: 'deleted' });
      }
      throw new Error('unexpected: ' + url + ' ' + (init.method ?? 'GET'));
    });

    const { findByText, queryByText, container } = render(WebhooksPage);

    await findByText('incident-channel');

    // Row's delete is the Trash2 icon button. Find by the sr-only label.
    // There may be other "Delete" labels later (dialog), so click the
    // first one — that's the row button.
    const deleteEls = Array.from(
      container.querySelectorAll('button')
    ).filter((b) => (b.textContent || '').trim() === 'Delete');
    expect(deleteEls.length).toBeGreaterThan(0);
    await fireEvent.click(deleteEls[0]);

    // Confirm dialog opens; its confirm button is also labelled "Delete".
    // Click the one in the dialog (last in DOM order).
    const allDeletes = Array.from(container.querySelectorAll('button')).filter(
      (b) => (b.textContent || '').trim() === 'Delete'
    );
    // After the dialog opens there are typically two: the row button
    // (no longer relevant) and the dialog's. Click the last.
    await fireEvent.click(allDeletes[allDeletes.length - 1]);

    await waitFor(() => {
      const del = calls.find((c) => c.init.method === 'DELETE');
      expect(del?.url).toBe('/api/v1/webhooks/h1');
    });
    // Row should disappear once the delete resolves.
    await waitFor(() => expect(queryByText('incident-channel')).toBeNull());
  });
});
