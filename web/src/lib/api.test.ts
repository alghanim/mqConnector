import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { api } from './api';

// Stub global fetch with a recorder so we can assert on the request the
// thin client builds, and control what it sees back.
function stubFetch(handler: (url: string, init: RequestInit) => Response | Promise<Response>) {
  const spy = vi.fn(handler);
  globalThis.fetch = spy as unknown as typeof fetch;
  return spy;
}

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('api client', () => {
  it('prepends /api and sends credentials', async () => {
    const spy = stubFetch(async (_url, _init) =>
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      })
    );
    await api.get<{ ok: boolean }>('/health');
    expect(spy).toHaveBeenCalledOnce();
    const [url, init] = spy.mock.calls[0];
    expect(url).toBe('/api/health');
    expect(init?.credentials).toBe('include');
  });

  it('sets Content-Type only when there is a body', async () => {
    const spy = stubFetch(async () =>
      new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    );
    await api.post('/x', { a: 1 });
    const headers = (spy.mock.calls[0][1]?.headers ?? {}) as Record<string, string>;
    expect(headers['Content-Type']).toBe('application/json');

    spy.mockClear();
    await api.get('/x');
    const headersGet = (spy.mock.calls[0][1]?.headers ?? {}) as Record<string, string>;
    expect(headersGet['Content-Type']).toBeUndefined();
  });

  it('throws an ApiError on non-2xx', async () => {
    stubFetch(async () =>
      new Response(JSON.stringify({ error: 'nope' }), {
        status: 403,
        headers: { 'Content-Type': 'application/json' }
      })
    );
    await expect(api.get('/x')).rejects.toMatchObject({ status: 403, message: 'nope' });
  });

  it('preserves the response status on plain-text errors', async () => {
    stubFetch(async () => new Response('limit reached', { status: 429 }));
    await expect(api.get('/x')).rejects.toMatchObject({ status: 429 });
  });
});
