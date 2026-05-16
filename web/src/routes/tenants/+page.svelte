<!--
  /tenants — list every tenant the user belongs to, with role + member
  management for the ones the user owns.

  System-admins (owners of the default tenant) get a "New tenant" button.
  Everyone else sees only the tenants they're a member of.

  Per-tenant members are loaded lazily when the user expands a row,
  so a user in many tenants doesn't pay for N round-trips at load.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Membership, type Role, type TenantMembership, type ApiError } from '$lib/api';
  import { tenants } from '$lib/stores/tenants';
  import { auth } from '$lib/stores/auth';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Dialog from '$lib/components/Dialog.svelte';

  let error = '';
  let creating: { slug: string; name: string } | null = null;

  // Per-tenant member lists, keyed by tenant id. Lazy-loaded.
  let memberLists: Record<string, Membership[]> = {};
  let expanded: Record<string, boolean> = {};
  let memberError: Record<string, string> = {};

  // Per-tenant "add member" draft.
  let memberDraft: Record<string, { sub: string; username: string; role: Role }> = {};

  onMount(async () => {
    if (!$tenants.initialised) await tenants.refresh();
  });

  // Heuristic: the system admin is whoever sees ≥2 tenants AND is owner
  // of the default tenant. Cheaper than another round trip — the server
  // refuses creation anyway, so a wrong heuristic is just a UX glitch.
  $: isSystemAdmin = $tenants.memberships.some(
    (m: TenantMembership) =>
      m.tenant.slug === 'default' && m.role === 'owner'
  );

  $: roleOptions = [
    { value: 'viewer', label: t($locale, 'tenants.role.viewer') },
    { value: 'operator', label: t($locale, 'tenants.role.operator') },
    { value: 'admin', label: t($locale, 'tenants.role.admin') },
    { value: 'owner', label: t($locale, 'tenants.role.owner') }
  ];

  function ensureDraft(tid: string) {
    if (!memberDraft[tid]) {
      memberDraft[tid] = { sub: '', username: '', role: 'viewer' };
      memberDraft = memberDraft;
    }
  }

  async function loadMembers(tid: string) {
    if (memberLists[tid]) return;
    try {
      const res = await api.get<Membership[]>(`/v1/tenants/${tid}/members`);
      memberLists[tid] = res ?? [];
      memberLists = memberLists;
    } catch (err) {
      const e = err as ApiError;
      memberError[tid] = e?.message || 'failed';
      memberError = memberError;
    }
  }

  async function toggleExpand(tid: string) {
    expanded[tid] = !expanded[tid];
    expanded = expanded;
    if (expanded[tid]) {
      ensureDraft(tid);
      await loadMembers(tid);
    }
  }

  function startCreate() {
    creating = { slug: '', name: '' };
  }
  async function saveCreate() {
    if (!creating?.slug || !creating?.name) return;
    try {
      await api.post('/v1/tenants', { slug: creating.slug, name: creating.name });
      creating = null;
      await tenants.refresh();
    } catch (err) {
      const e = err as ApiError;
      error = e?.message || 'create failed';
    }
  }
  function cancelCreate() {
    creating = null;
  }

  async function addMember(tid: string) {
    const draft = memberDraft[tid];
    if (!draft?.sub) return;
    try {
      await api.post(`/v1/tenants/${tid}/members`, {
        user_sub: draft.sub,
        username: draft.username,
        role: draft.role
      });
      delete memberLists[tid]; // force reload
      memberDraft[tid] = { sub: '', username: '', role: 'viewer' };
      memberDraft = memberDraft;
      await loadMembers(tid);
    } catch (err) {
      const e = err as ApiError;
      memberError[tid] = e?.message || 'add failed';
      memberError = memberError;
    }
  }

  let pendingRemove: { tenantId: string; sub: string; username: string } | null = null;
  async function confirmRemove() {
    if (!pendingRemove) return;
    const { tenantId, sub } = pendingRemove;
    try {
      await api.del(`/v1/tenants/${tenantId}/members/${encodeURIComponent(sub)}`);
      delete memberLists[tenantId];
      pendingRemove = null;
      await loadMembers(tenantId);
    } catch (err) {
      const e = err as ApiError;
      memberError[tenantId] = e?.message || 'remove failed';
      memberError = memberError;
      pendingRemove = null;
    }
  }
</script>

<div class="space-y-6 max-w-5xl">
  <div class="flex items-baseline justify-between">
    <h2 class="text-2xl font-semibold" style="color: var(--text)">
      {t($locale, 'tenants.title')}
    </h2>
    {#if isSystemAdmin}
      <Button on:click={startCreate}>{t($locale, 'tenants.add')}</Button>
    {/if}
  </div>

  {#if error}
    <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
  {/if}

  {#if creating}
    <Card strip>
      <p class="section-heading mb-4">{t($locale, 'tenants.new')}</p>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <Input bind:value={creating.slug} label={t($locale, 'tenants.slug')} required />
        <Input bind:value={creating.name} label={t($locale, 'tenants.name')} required />
      </div>
      <div class="flex gap-2 justify-end mt-5">
        <Button variant="ghost" on:click={cancelCreate}>{t($locale, 'common.cancel')}</Button>
        <Button on:click={saveCreate}>{t($locale, 'common.save')}</Button>
      </div>
    </Card>
  {/if}

  <div class="space-y-3">
    {#each $tenants.memberships as m (m.tenant.id)}
      <Card>
        <div class="flex items-center justify-between gap-3">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <span class="font-semibold" style="color: var(--text)">{m.tenant.name}</span>
              <Badge variant="neutral">{m.tenant.slug}</Badge>
              {#if m.is_active}
                <Badge variant="success">{t($locale, 'common.enabled')}</Badge>
              {/if}
            </div>
            <div class="text-xs mt-1" style="color: var(--text-muted)">
              {m.tenant.id}
            </div>
          </div>
          <div class="flex items-center gap-2">
            <Badge variant="neutral">{m.role}</Badge>
            <Button variant="ghost" on:click={() => toggleExpand(m.tenant.id)}>
              {expanded[m.tenant.id] ? '−' : '+'}
            </Button>
          </div>
        </div>

        {#if expanded[m.tenant.id]}
          <div class="mt-5 border-t pt-4" style="border-color: var(--border);">
            <p class="section-heading mb-3">{t($locale, 'tenants.members')}</p>

            {#if memberError[m.tenant.id]}
              <Alert variant="error">{memberError[m.tenant.id]}</Alert>
            {/if}

            {#if m.role !== 'owner'}
              <p class="text-xs" style="color: var(--text-muted)">
                {t($locale, 'tenants.notOwner')}
              </p>
            {:else}
              <table class="table">
                <thead>
                  <tr>
                    <th>{t($locale, 'tenants.username')}</th>
                    <th>{t($locale, 'tenants.userSub')}</th>
                    <th>{t($locale, 'tenants.role')}</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {#each memberLists[m.tenant.id] ?? [] as mem (mem.user_sub)}
                    <tr>
                      <td style="color: var(--text)">{mem.username || '—'}</td>
                      <td style="color: var(--text-muted)">
                        <code class="text-xs">{mem.user_sub}</code>
                      </td>
                      <td><Badge variant="neutral">{mem.role}</Badge></td>
                      <td class="text-end">
                        <Button
                          variant="outline"
                          on:click={() =>
                            (pendingRemove = {
                              tenantId: m.tenant.id,
                              sub: mem.user_sub,
                              username: mem.username
                            })}>
                          {t($locale, 'tenants.removeMember')}
                        </Button>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>

              <div class="mt-4 grid grid-cols-1 sm:grid-cols-4 gap-3 items-end">
                <Input
                  bind:value={memberDraft[m.tenant.id].sub}
                  label={t($locale, 'tenants.userSub')} />
                <Input
                  bind:value={memberDraft[m.tenant.id].username}
                  label={t($locale, 'tenants.username')} />
                <Select
                  bind:value={memberDraft[m.tenant.id].role}
                  label={t($locale, 'tenants.role')}
                  options={roleOptions} />
                <Button on:click={() => addMember(m.tenant.id)}>
                  {t($locale, 'tenants.addMember')}
                </Button>
              </div>
            {/if}
          </div>
        {/if}
      </Card>
    {/each}
  </div>
</div>

<Dialog
  open={pendingRemove !== null}
  title={t($locale, 'common.confirmDelete')}
  confirmLabel={t($locale, 'tenants.removeMember')}
  cancelLabel={t($locale, 'common.cancel')}
  on:cancel={() => (pendingRemove = null)}
  on:confirm={confirmRemove}>
  {#if pendingRemove}
    <p style="color: var(--text)">
      {pendingRemove.username || pendingRemove.sub}
    </p>
  {/if}
</Dialog>

<style>
  td:last-child {
    text-align: end;
  }
</style>
