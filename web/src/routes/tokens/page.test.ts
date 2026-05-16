// Component tests for /tokens.
//
// We render the page with @testing-library/svelte against a stubbed
// global fetch. The test doubles aren't trying to cover the API layer
// (api.test.ts already does that) — the goal here is to assert the
// page's INTERACTIONS work end-to-end through the DOM:
//
//   • the initial list renders rows for each token
//   • clicking "New token" → submitting the form posts the right body
//     and triggers the one-shot reveal modal with the secret
//   • clicking Revoke confirms then PUTs delete and flips the row's
//     status badge
//
// Browser APIs that jsdom doesn't ship (`navigator.clipboard`,
// `crypto.getRandomValues`) are stubbed in setup.ts or inline below.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import TokensPage from './+page.svelte';

type FetchCall = { url: string; init: RequestInit };

/**
 * stubFetch installs a recording fake on globalThis.fetch. The handler
 * receives the parsed URL + init for each call and returns a Response.
 * Calls are also captured in the `calls` array for assertion.
 */
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

// Lightweight clipboard stub — not all of jsdom builds expose it.
beforeEach(() => {
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: vi.fn(async () => undefined) }
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('/tokens page', () => {
  it('renders an empty state when the server returns no tokens', async () => {
    stubFetch(async (url) => {
      if (url === '/api/v1/tokens') return jsonResponse({ items: [] });
      throw new Error('unexpected url: ' + url);
    });

    const { findByText } = render(TokensPage);

    // Empty-state heading from locale.ts → 'empty.tokens.title'
    expect(await findByText('No API tokens yet')).toBeInTheDocument();
  });

  it('lists tokens from the server with prefix + role + status', async () => {
    stubFetch(async (url) => {
      if (url === '/api/v1/tokens') {
        return jsonResponse({
          items: [
            {
              id: 't1',
              tenant_id: 'tenant',
              user_sub: 'u1',
              name: 'ci-pipeline',
              prefix: 'abc12345',
              role: 'operator',
              created_at: '2026-05-16T00:00:00Z',
              expires_at: null,
              last_used_at: null,
              revoked_at: null
            }
          ]
        });
      }
      throw new Error('unexpected url: ' + url);
    });

    const { findByText, findAllByText } = render(TokensPage);

    expect(await findByText('ci-pipeline')).toBeInTheDocument();
    // Prefix is rendered as `mqct_abc12345…` in a <code> tag.
    expect(await findByText(/mqct_abc12345/)).toBeInTheDocument();
    // 'Active' status badge appears.
    expect(await findAllByText('Active')).not.toHaveLength(0);
  });

  it('creating a token POSTs the form payload and reveals the one-shot secret', async () => {
    const newSecret = 'mqct_deadbeef_0123456789abcdef0123456789abcdef01234567';
    let postSeen = false;
    const { calls } = stubFetch(async (url, init) => {
      if (url === '/api/v1/tokens' && (!init.method || init.method === 'GET')) {
        return jsonResponse({ items: [] });
      }
      if (url === '/api/v1/tokens' && init.method === 'POST') {
        postSeen = true;
        return jsonResponse(
          {
            secret: newSecret,
            token: {
              id: 't-new',
              tenant_id: 'tenant',
              user_sub: 'u1',
              name: 'first-token',
              prefix: 'deadbeef',
              role: 'admin',
              created_at: '2026-05-16T00:00:00Z'
            },
            warning: 'one-shot'
          },
          201
        );
      }
      throw new Error('unexpected: ' + url + ' ' + (init.method ?? 'GET'));
    });

    const { findByText, findByLabelText, getAllByText } = render(TokensPage);

    // Open the create dialog. There are two "New token" buttons (header +
    // empty-state CTA when list is empty); both should work — click the
    // first.
    const newButtons = await waitFor(() => getAllByText('New token'));
    await fireEvent.click(newButtons[0]);

    const nameInput = await findByLabelText('Name');
    await fireEvent.input(nameInput, { target: { value: 'first-token' } });

    // Submit. Dialog.svelte's confirm button label is "Create token".
    await fireEvent.click(await findByText('Create token'));

    // The POST should fire.
    await waitFor(() => expect(postSeen).toBe(true));

    // The reveal modal shows the secret + the warning + the dismiss button.
    expect(await findByText(newSecret)).toBeInTheDocument();
    expect(
      await findByText('Copy this secret now — it cannot be retrieved later.')
    ).toBeInTheDocument();

    // Body must include name + role from the inputs.
    const post = calls.find((c) => c.init.method === 'POST')!;
    expect(post).toBeTruthy();
    const body = JSON.parse(post.init.body as string);
    expect(body).toMatchObject({ name: 'first-token', role: 'admin' });
    // expires_in_seconds is omitted when "never" is selected.
    expect(body.expires_in_seconds).toBeUndefined();
  });

  it('revoking a token DELETEs the row and flips its status optimistically', async () => {
    const { calls } = stubFetch(async (url, init) => {
      if (url === '/api/v1/tokens' && (!init.method || init.method === 'GET')) {
        return jsonResponse({
          items: [
            {
              id: 't1',
              tenant_id: 'tenant',
              user_sub: 'u1',
              name: 'doomed',
              prefix: 'aaaaaaaa',
              role: 'admin',
              created_at: '2026-05-16T00:00:00Z',
              expires_at: null,
              last_used_at: null,
              revoked_at: null
            }
          ]
        });
      }
      if (url === '/api/v1/tokens/t1' && init.method === 'DELETE') {
        return jsonResponse({ status: 'revoked' });
      }
      throw new Error('unexpected: ' + url + ' ' + (init.method ?? 'GET'));
    });

    const { findByText, getAllByText } = render(TokensPage);

    // Wait for the row to render, then click its Revoke button.
    await findByText('doomed');
    const revokes = getAllByText('Revoke');
    // The first Revoke is the row's button; subsequent ones (if any)
    // are inside the confirm dialog which isn't open yet.
    await fireEvent.click(revokes[0]);

    // Confirm dialog opens with the same "Revoke" label — find it now.
    const confirmButtons = getAllByText('Revoke');
    // The last button is in the dialog (rendered later in the DOM).
    await fireEvent.click(confirmButtons[confirmButtons.length - 1]);

    // DELETE fires.
    await waitFor(() => {
      const del = calls.find((c) => c.init.method === 'DELETE');
      expect(del?.url).toBe('/api/v1/tokens/t1');
    });

    // Row's status badge becomes "Revoked".
    expect(await findByText('Revoked')).toBeInTheDocument();
  });
});
