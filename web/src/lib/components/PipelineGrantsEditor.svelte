<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type PipelineGrant, type Role } from '$lib/api';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';

  /**
   * PipelineGrantsEditor renders the per-pipeline RBAC grants surface
   * for a single pipeline. Mounted inside the pipeline detail page so
   * the grants list lives next to the configuration it controls.
   *
   * Access model:
   *   - The pipeline list response carries `effective_role` for the
   *     viewing user. Only admin / owner sees this editor (the
   *     consumer toggles it off when the role is lower).
   *   - The component still calls the API directly, so a non-admin
   *     who somehow renders it just sees a 403 in the alert region —
   *     no silent failures.
   */
  export let pipelineId: string;
  /** When false the component renders a read-only summary so a user
   * with viewer/operator effective role can still understand who has
   * access without being able to mutate it. */
  export let canManage = true;

  let grants: PipelineGrant[] = [];
  let loading = false;
  let error = '';
  let saving = false;

  // Add-grant form state.
  let newUserSub = '';
  let newRole: Role = 'viewer';

  const roleOptions: { value: Role; label: string }[] = [
    { value: 'viewer', label: 'viewer (read-only)' },
    { value: 'operator', label: 'operator (edit config)' },
    { value: 'admin', label: 'admin (manage + delete)' },
    { value: 'owner', label: 'owner (full control)' }
  ];

  async function load() {
    if (!pipelineId) return;
    loading = true;
    error = '';
    try {
      const v = await api.get<PipelineGrant[]>(`/v1/pipelines/${pipelineId}/grants`);
      grants = v ?? [];
    } catch (e: unknown) {
      // 403 here means the user can't manage grants on this pipeline;
      // surface a clear message instead of the stock API error.
      const apiErr = e as { status?: number; message?: string };
      if (apiErr.status === 403) {
        error = "you don't have permission to view grants on this pipeline";
      } else {
        error = apiErr.message || 'failed to load grants';
      }
    } finally {
      loading = false;
    }
  }

  async function addGrant() {
    if (!newUserSub.trim()) {
      error = 'user sub is required';
      return;
    }
    saving = true;
    error = '';
    try {
      await api.put<PipelineGrant>(
        `/v1/pipelines/${pipelineId}/grants/${encodeURIComponent(newUserSub.trim())}`,
        { role: newRole }
      );
      newUserSub = '';
      newRole = 'viewer';
      await load();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to save grant';
    } finally {
      saving = false;
    }
  }

  async function changeRole(g: PipelineGrant, role: Role) {
    saving = true;
    error = '';
    try {
      await api.put<PipelineGrant>(
        `/v1/pipelines/${pipelineId}/grants/${encodeURIComponent(g.user_sub)}`,
        { role }
      );
      await load();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to update grant';
    } finally {
      saving = false;
    }
  }

  async function removeGrant(g: PipelineGrant) {
    if (!confirm(`Revoke ${g.role} from ${g.user_sub}?`)) return;
    saving = true;
    error = '';
    try {
      await api.del(`/v1/pipelines/${pipelineId}/grants/${encodeURIComponent(g.user_sub)}`);
      await load();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to revoke grant';
    } finally {
      saving = false;
    }
  }

  onMount(load);
</script>

<Card>
  <h2 class="card-title">Per-pipeline access</h2>
  <p class="hint">
    Grants escalate a user's tenant role for this pipeline only — they never demote. A user
    with no grant inherits their tenant role here. Adding an <strong>admin</strong> grant
    for a tenant-viewer lets that user edit this pipeline without giving them tenant-wide
    admin.
  </p>

  {#if error}
    <div class="mt-2">
      <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
    </div>
  {/if}

  {#if loading}
    <p class="muted">Loading grants…</p>
  {:else if grants.length === 0}
    <EmptyState
      illustration="tenants"
      title="No explicit grants"
      body="Every user with a tenant role can access this pipeline at their tenant level."
    />
  {:else}
    <table class="grants-table">
      <thead>
        <tr>
          <th>User</th>
          <th>Role</th>
          {#if canManage}
            <th></th>
          {/if}
        </tr>
      </thead>
      <tbody>
        {#each grants as g (g.user_sub)}
          <tr>
            <td class="mono">{g.user_sub}</td>
            <td>
              {#if canManage}
                <select
                  class="input role-select"
                  value={g.role}
                  disabled={saving}
                  on:change={(e) => changeRole(g, (e.currentTarget as HTMLSelectElement).value as Role)}
                >
                  {#each roleOptions as o (o.value)}
                    <option value={o.value}>{o.label}</option>
                  {/each}
                </select>
              {:else}
                <span class="role-pill">{g.role}</span>
              {/if}
            </td>
            {#if canManage}
              <td class="row-actions">
                <Button variant="danger" on:click={() => removeGrant(g)} disabled={saving}>
                  Revoke
                </Button>
              </td>
            {/if}
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}

  {#if canManage}
    <hr class="divider" />
    <h3 class="add-title">Add grant</h3>
    <div class="add-row">
      <div class="grow">
        <Input label="User sub" placeholder="user@example.com or sub claim" bind:value={newUserSub} />
      </div>
      <div class="role-picker">
        <label class="add-label" for="new-grant-role">Role</label>
        <select
          id="new-grant-role"
          class="input"
          bind:value={newRole}
          disabled={saving}
        >
          {#each roleOptions as o (o.value)}
            <option value={o.value}>{o.label}</option>
          {/each}
        </select>
      </div>
      <div class="add-action">
        <Button on:click={addGrant} disabled={saving}>Add</Button>
      </div>
    </div>
  {/if}
</Card>

<style>
  .card-title {
    margin: 0 0 12px;
    font-size: 16px;
    font-weight: 600;
  }
  .add-title {
    margin: 0 0 8px;
    font-size: 14px;
    font-weight: 600;
  }
  .add-label {
    display: block;
    font-size: 12px;
    font-weight: 500;
    margin-block-end: 4px;
    color: var(--text);
  }
  .role-select {
    inline-size: 100%;
  }
  .hint {
    color: var(--text-muted);
    font-size: 13px;
    margin-block-end: 12px;
  }
  .muted {
    color: var(--text-muted);
  }
  .grants-table {
    inline-size: 100%;
    border-collapse: collapse;
    margin-block: 8px;
  }
  .grants-table th,
  .grants-table td {
    padding: 8px 10px;
    text-align: start;
    border-block-end: 1px solid var(--border);
  }
  .grants-table th {
    font-size: 12px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .row-actions {
    text-align: end;
  }
  .mono {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 13px;
  }
  .role-pill {
    display: inline-block;
    padding: 2px 10px;
    border-radius: 999px;
    background: var(--surface-elevated);
    border: 1px solid var(--border);
    font-size: 12px;
  }
  .divider {
    border: 0;
    border-block-start: 1px solid var(--border);
    margin-block: 16px;
  }
  .add-row {
    display: flex;
    gap: 12px;
    align-items: end;
    flex-wrap: wrap;
  }
  .grow {
    flex: 1 1 220px;
  }
  .role-picker {
    flex: 0 0 220px;
  }
  .add-action {
    flex: 0 0 auto;
  }
</style>
