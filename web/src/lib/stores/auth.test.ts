import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { get } from 'svelte/store';
import { auth } from './auth';

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.useFakeTimers();
  fetchMock = vi.fn();
  globalThis.fetch = fetchMock as unknown as typeof fetch;
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  });
}

describe('auth store', () => {
  it('refresh() populates user on 200', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ sub: 'u1', preferred_username: 'alice' }));
    await auth.refresh();
    expect(get(auth).user?.preferred_username).toBe('alice');
    expect(get(auth).initialised).toBe(true);
  });

  it('refresh() clears user on 401', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: 'unauthorized' }, 401));
    await auth.refresh();
    expect(get(auth).user).toBeNull();
    expect(get(auth).initialised).toBe(true);
  });

  it('login() calls /auth/login then /auth/me', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ status: 'ok' })) // login
      .mockResolvedValueOnce(jsonResponse({ sub: 'u1', preferred_username: 'alice' })); // me
    await auth.login('alice', 'pw');
    expect(fetchMock.mock.calls[0][0]).toBe('/api/auth/login');
    expect(fetchMock.mock.calls[1][0]).toBe('/api/auth/me');
    expect(get(auth).user?.preferred_username).toBe('alice');
  });

  it('starts silent refresh after login and stops after logout', async () => {
    // `auth` is a module-level singleton — earlier tests may have left an
    // active refresh timer behind. Logout explicitly to clear it.
    fetchMock.mockResolvedValueOnce(jsonResponse({ status: 'ok' }));
    await auth.logout();
    fetchMock.mockClear();

    // Login: login + me
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ status: 'ok' }))
      .mockResolvedValueOnce(jsonResponse({ sub: 'u1', preferred_username: 'alice' }));
    await auth.login('alice', 'pw');

    // Advance past the 30-min refresh tick. Expect one /auth/refresh call.
    fetchMock.mockResolvedValueOnce(jsonResponse({ status: 'ok' }));
    await vi.advanceTimersByTimeAsync(30 * 60 * 1000 + 1000);

    const refreshCalls = fetchMock.mock.calls.filter(([u]) => u === '/api/auth/refresh');
    expect(refreshCalls.length).toBe(1);

    // Logout — subsequent ticks should NOT make further refresh calls.
    fetchMock.mockResolvedValueOnce(jsonResponse({ status: 'ok' })); // logout
    await auth.logout();
    fetchMock.mockClear();
    await vi.advanceTimersByTimeAsync(60 * 60 * 1000);
    const after = fetchMock.mock.calls.filter(([u]) => u === '/api/auth/refresh');
    expect(after.length).toBe(0);
  });
});
