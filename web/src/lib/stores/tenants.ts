// Tenant store — tracks the caller's memberships and the active tenant.
// Loaded on app boot, then refreshed when the user switches tenants.
import { writable } from 'svelte/store';
import { api, type TenantMembership, type ApiError } from '$lib/api';

interface TenantState {
  memberships: TenantMembership[];
  active: TenantMembership | null;
  loading: boolean;
  initialised: boolean;
}

function createTenantsStore() {
  const { subscribe, set, update } = writable<TenantState>({
    memberships: [],
    active: null,
    loading: false,
    initialised: false
  });

  return {
    subscribe,

    /** Refresh memberships from the server. */
    async refresh() {
      update((s) => ({ ...s, loading: true }));
      try {
        const res = await api.get<{ items: TenantMembership[] }>('/v1/tenants');
        const items = res.items ?? [];
        const active = items.find((m) => m.is_active) ?? items[0] ?? null;
        set({ memberships: items, active, loading: false, initialised: true });
      } catch (err) {
        const e = err as ApiError;
        // 401 means the session just expired — drop state silently
        // so the auth store can redirect to /login.
        if (e?.status !== 401) {
          // eslint-disable-next-line no-console
          console.error('tenants refresh failed', e);
        }
        set({ memberships: [], active: null, loading: false, initialised: true });
      }
    },

    /** Switch the active tenant. Returns true on success. */
    async switchTo(tenantId: string): Promise<boolean> {
      try {
        await api.post(`/v1/tenants/${tenantId}/switch`);
        // Refresh so the active flag, role, and any tenant-scoped
        // resource lists invalidate consistently.
        await this.refresh();
        return true;
      } catch (err) {
        const e = err as ApiError;
        // eslint-disable-next-line no-console
        console.error('tenant switch failed', e);
        return false;
      }
    },

    /** Reset to the uninitialised state (called on logout). */
    reset() {
      set({ memberships: [], active: null, loading: false, initialised: false });
    }
  };
}

export const tenants = createTenantsStore();
