// Auth store — tracks the current authenticated user. The session itself is
// an HttpOnly cookie set by the backend; this store mirrors whatever
// /api/auth/me returns.
import { writable } from 'svelte/store';
import { api, type Me, type ApiError } from '$lib/api';

interface AuthState {
  user: Me | null;
  loading: boolean;
  initialised: boolean;
}

function createAuth() {
  const { subscribe, set, update } = writable<AuthState>({
    user: null,
    loading: false,
    initialised: false
  });

  return {
    subscribe,
    async refresh() {
      update((s) => ({ ...s, loading: true }));
      try {
        const me = await api.get<Me>('/auth/me');
        set({ user: me, loading: false, initialised: true });
      } catch {
        set({ user: null, loading: false, initialised: true });
      }
    },
    async login(username: string, password: string) {
      await api.post<{ status: string }>('/auth/login', { username, password });
      await this.refresh();
    },
    async logout() {
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
