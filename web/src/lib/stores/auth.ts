// Auth store — tracks the current authenticated user. The session itself is
// an HttpOnly cookie set by the backend; this store mirrors whatever
// /api/auth/me returns and runs a silent refresh on a timer so an active
// operator never gets a surprise 12 h logout.
import { writable } from 'svelte/store';
import { browser } from '$app/environment';
import { api, type Me, type ApiError } from '$lib/api';

interface AuthState {
  user: Me | null;
  loading: boolean;
  initialised: boolean;
}

// Refresh well before the default 12 h session expiry. The backend's
// /api/auth/refresh swaps the cookies in place. Tuned so a single tab will
// refresh ~12 times across a full day — cheap and conservative.
const REFRESH_INTERVAL_MS = 30 * 60 * 1000; // 30 minutes

function createAuth() {
  const { subscribe, set, update } = writable<AuthState>({
    user: null,
    loading: false,
    initialised: false
  });

  let refreshTimer: ReturnType<typeof setInterval> | undefined;

  function startSilentRefresh() {
    if (!browser || refreshTimer !== undefined) return;
    refreshTimer = setInterval(async () => {
      try {
        await api.post('/auth/refresh');
      } catch (err) {
        const e = err as ApiError;
        // 401 means the refresh token is gone too — drop the user state.
        // Other errors (network blips) leave the timer running.
        if (e?.status === 401) {
          stopSilentRefresh();
          set({ user: null, loading: false, initialised: true });
        }
      }
    }, REFRESH_INTERVAL_MS);
  }

  function stopSilentRefresh() {
    if (refreshTimer !== undefined) {
      clearInterval(refreshTimer);
      refreshTimer = undefined;
    }
  }

  return {
    subscribe,
    async refresh() {
      update((s) => ({ ...s, loading: true }));
      try {
        const me = await api.get<Me>('/auth/me');
        set({ user: me, loading: false, initialised: true });
        startSilentRefresh();
      } catch {
        stopSilentRefresh();
        set({ user: null, loading: false, initialised: true });
      }
    },
    async login(username: string, password: string) {
      await api.post<{ status: string }>('/auth/login', { username, password });
      await this.refresh();
    },
    async logout() {
      stopSilentRefresh();
      try {
        await api.post('/auth/logout');
      } catch (err) {
        const e = err as ApiError;
        if (e.status !== 401) throw err;
      }
      set({ user: null, loading: false, initialised: true });
    }
  };
}

export const auth = createAuth();
