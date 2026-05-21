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
  import PageHeader from '$lib/components/PageHeader.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import StatChip from '$lib/components/StatChip.svelte';
  import Avatar from '$lib/components/Avatar.svelte';
  import { Plus, ChevronDown, ChevronRight, Star, Users2, Gauge, Trash2 } from 'lucide-svelte';

  let error = '';
  let creating: { slug: string; name: string } | null = null;

  // Per-tenant member lists, keyed by tenant id. Lazy-loaded.
  let memberLists: Record<string, Membership[]> = {};
  let expanded: Record<string, boolean> = {};
  let memberError: Record<string, string> = {};
  let memberCount: Record<string, number> = {};

  function fmtDate(s?: string): string {
    if (!s) return '—';
    // Only show YYYY-MM-DD for the table — full timestamp goes into title.
    const m = /^(\d{4}-\d{2}-\d{2})/.exec(s);
    return m ? m[1] : s;
  }

  function roleTone(r: Role): 'success' | 'warning' | 'neutral' {
    if (r === 'owner') return 'success';
    if (r === 'admin') return 'warning';
    return 'neutral';
  }

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
      memberCount[tid] = res?.length ?? 0;
      memberCount = memberCount;
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

<div class="t-root">
  <PageHeader
    title={t($locale, 'tenants.title')}
    subtitle={t($locale, 'tenants.pageSubtitle')}
    count={$tenants.memberships.length}
  >
    <svelte:fragment slot="primary">
      {#if isSystemAdmin}
        <Button on:click={startCreate}>
          <Plus size={14} aria-hidden="true" />
          <span class="ms-1">{t($locale, 'tenants.add')}</span>
        </Button>
      {/if}
    </svelte:fragment>
    <svelte:fragment slot="stats">
      <StatChip
        label={t($locale, 'tenants.role.owner')}
        value={$tenants.memberships.filter((m) => m.role === 'owner').length}
      />
      <StatChip
        label={t($locale, 'tenants.role.admin')}
        value={$tenants.memberships.filter((m) => m.role === 'admin').length}
      />
      <StatChip
        label={t($locale, 'tenants.role.operator')}
        value={$tenants.memberships.filter((m) => m.role === 'operator').length}
      />
      <StatChip
        label={t($locale, 'tenants.role.viewer')}
        value={$tenants.memberships.filter((m) => m.role === 'viewer').length}
      />
    </svelte:fragment>
  </PageHeader>

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

  {#if $tenants.initialised && $tenants.memberships.length === 0}
    <Card>
      <EmptyState
        illustration="tenants"
        title={t($locale, 'empty.tenants.title')}
        body={t($locale, 'empty.tenants.body')}
      >
        <svelte:fragment slot="action">
          {#if isSystemAdmin}
            <Button on:click={startCreate}>
              <Plus size={14} aria-hidden="true" />
              <span class="ms-1">{t($locale, 'tenants.add')}</span>
            </Button>
          {/if}
        </svelte:fragment>
      </EmptyState>
    </Card>
  {:else}
    <Card>
      <div class="t-table-wrap">
        <table class="t-table">
          <thead>
            <tr>
              <th class="th-mark"></th>
              <th>{t($locale, 'tenants.name')}</th>
              <th>{t($locale, 'tenants.slug')}</th>
              <th>{t($locale, 'tenants.role')}</th>
              <th class="right">{t($locale, 'tenants.members')}</th>
              <th class="right">{t($locale, 'tenants.maxPipelines')}</th>
              <th class="right">{t($locale, 'tenants.rateLimit')}</th>
              <th>{t($locale, 'common.created')}</th>
              <th class="th-actions"></th>
            </tr>
          </thead>
          <tbody>
            {#each $tenants.memberships as m (m.tenant.id)}
              {@const open = !!expanded[m.tenant.id]}
              <tr class:row-active={m.is_active}>
                <td class="cell-mark">
                  {#if m.is_active}
                    <span class="active-star" title={t($locale, 'tenants.active')}><Star size={12} aria-hidden="true" /></span>
                  {/if}
                </td>
                <td>
                  <button type="button" class="t-name-btn" on:click={() => toggleExpand(m.tenant.id)} aria-expanded={open}>
                    {#if open}<ChevronDown size={14} aria-hidden="true" />{:else}<ChevronRight size={14} aria-hidden="true" class="rtl-flip" />{/if}
                    <span class="t-name">{m.tenant.name}</span>
                    {#if m.tenant.slug === 'default'}
                      <span class="t-default-pill">{t($locale, 'tenants.defaultTenant')}</span>
                    {/if}
                  </button>
                </td>
                <td><code class="t-slug">{m.tenant.slug}</code></td>
                <td><Badge variant={roleTone(m.role)}>{m.role}</Badge></td>
                <td class="right number">
                  {#if memberCount[m.tenant.id] !== undefined}
                    {memberCount[m.tenant.id]}
                  {:else if m.role === 'owner'}
                    <button type="button" class="num-load" on:click={() => loadMembers(m.tenant.id)}>{t($locale, 'common.load')}</button>
                  {:else}
                    <span class="muted">—</span>
                  {/if}
                </td>
                <td class="right number">{m.tenant.max_pipelines || '∞'}</td>
                <td class="right number">{m.tenant.max_msgs_per_minute || '∞'}<span class="unit"> /min</span></td>
                <td class="mono small muted" title={m.tenant.created_at}>{fmtDate(m.tenant.created_at)}</td>
                <td>
                  <div class="row-actions">
                    <button type="button" class="icon-action" on:click={() => toggleExpand(m.tenant.id)} aria-label={t($locale, 'tenants.members')} title={t($locale, 'tenants.members')}>
                      <Users2 size={14} aria-hidden="true" />
                    </button>
                  </div>
                </td>
              </tr>
              {#if open}
                <tr class="row-detail">
                  <td colspan="9">
                    <div class="t-detail">
                      <div class="t-detail-head">
                        <p class="section-heading">{t($locale, 'tenants.members')}</p>
                        <code class="text-caption">{m.tenant.id}</code>
                      </div>

                      {#if memberError[m.tenant.id]}
                        <Alert variant="error">{memberError[m.tenant.id]}</Alert>
                      {/if}

                      {#if m.role !== 'owner'}
                        <p class="text-caption">{t($locale, 'tenants.notOwner')}</p>
                      {:else}
                        <table class="t-member-table">
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
                                <td>
                                  <div class="member-cell">
                                    <Avatar name={mem.username} sub={mem.user_sub} size="sm" />
                                    <span>{mem.username || '—'}</span>
                                  </div>
                                </td>
                                <td class="muted"><code class="mono small">{mem.user_sub}</code></td>
                                <td><Badge variant={roleTone(mem.role)}>{mem.role}</Badge></td>
                                <td class="right">
                                  <button
                                    type="button"
                                    class="icon-action danger"
                                    aria-label={t($locale, 'tenants.removeMember')}
                                    title={t($locale, 'tenants.removeMember')}
                                    on:click={() =>
                                      (pendingRemove = {
                                        tenantId: m.tenant.id,
                                        sub: mem.user_sub,
                                        username: mem.username
                                      })}>
                                    <Trash2 size={14} aria-hidden="true" />
                                  </button>
                                </td>
                              </tr>
                            {/each}
                          </tbody>
                        </table>

                        <div class="t-add">
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
                            <Plus size={14} aria-hidden="true" />
                            <span class="ms-1">{t($locale, 'tenants.addMember')}</span>
                          </Button>
                        </div>
                      {/if}
                    </div>
                  </td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>
    </Card>
  {/if}
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
  .t-root {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .t-table-wrap {
    overflow-x: auto;
    margin-inline: -16px;
  }
  .t-table {
    inline-size: 100%;
    border-collapse: collapse;
    font-size: 0.8125rem;
  }
  .t-table thead th {
    text-align: start;
    padding: 0.5rem 0.625rem;
    color: var(--text-muted);
    font-size: 0.6875rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom: 1px solid var(--border);
    background: var(--surface);
    position: sticky;
    top: 0;
    z-index: 1;
  }
  .t-table tbody tr {
    transition: background-color 100ms;
  }
  .t-table tbody tr:hover:not(.row-detail) {
    background: var(--surface-2);
  }
  .t-table td {
    padding: 0.5rem 0.625rem;
    border-bottom: 1px solid var(--divider-subtle);
    color: var(--text);
    vertical-align: middle;
  }
  .row-active {
    background: color-mix(in srgb, var(--primary) 6%, transparent);
  }
  .row-detail td {
    padding: 0;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
  }
  .row-detail:hover {
    background: var(--surface) !important;
  }

  .th-mark,
  .cell-mark {
    inline-size: 24px;
    padding-inline-end: 0;
  }
  .active-star {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--primary);
  }
  .th-actions {
    inline-size: 1%;
  }
  .right {
    text-align: end;
  }
  .number {
    font-variant-numeric: tabular-nums;
    font-weight: 600;
  }
  .unit {
    color: var(--text-tertiary);
    font-weight: 500;
    font-size: 11px;
  }
  .muted {
    color: var(--text-muted);
  }
  .mono {
    font-family: 'SFMono-Regular', Menlo, monospace;
  }
  .mono.small {
    font-size: 11px;
  }

  .t-name-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    background: transparent;
    border: 0;
    padding: 0;
    margin: 0;
    color: inherit;
    font: inherit;
    text-align: start;
    cursor: pointer;
  }
  .t-name-btn:hover .t-name {
    color: var(--primary);
  }
  .t-name {
    font-weight: 600;
    color: var(--text);
  }
  /* Labeled "default" chip — 12dp per Rule 9. */
  .t-default-pill {
    display: inline-flex;
    align-items: center;
    padding: 1px 6px;
    border-radius: 12px;
    background: var(--chip-info-bg);
    color: var(--chip-info-text);
    border: 1px solid color-mix(in srgb, var(--primary) 30%, transparent);
    font-size: 10.5px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .t-slug {
    font-family: 'SFMono-Regular', Menlo, monospace;
    font-size: 0.75rem;
    color: var(--text-muted);
  }
  .num-load {
    background: transparent;
    border: 1px dashed var(--border);
    color: var(--text-muted);
    font-size: 10.5px;
    font-weight: 600;
    padding: 1px 6px;
    border-radius: 6px;
    cursor: pointer;
  }
  .num-load:hover {
    color: var(--text);
    border-color: var(--text-tertiary);
  }

  .row-actions {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    justify-content: flex-end;
  }
  .icon-action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    inline-size: 28px;
    block-size: 28px;
    border-radius: 6px;
    background: transparent;
    border: 1px solid transparent;
    color: var(--text-muted);
    cursor: pointer;
    transition: all 120ms;
  }
  .icon-action:hover {
    background: var(--surface-2);
    border-color: var(--border);
    color: var(--text);
  }
  .icon-action.danger:hover {
    color: var(--danger);
    border-color: color-mix(in srgb, var(--danger) 35%, transparent);
  }

  .t-detail {
    padding: 12px 16px 16px;
    background: var(--surface-2);
    border-inline-start: 3px solid var(--primary);
  }
  .t-detail-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 8px;
    margin-block-end: 10px;
  }
  .t-member-table {
    inline-size: 100%;
    border-collapse: collapse;
    font-size: 0.8125rem;
  }
  .t-member-table thead th {
    text-align: start;
    padding: 0.375rem 0.5rem;
    color: var(--text-tertiary);
    font-size: 0.625rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom: 1px solid var(--divider);
  }
  .t-member-table td {
    padding: 0.375rem 0.5rem;
    border-bottom: 1px solid var(--divider-subtle);
    vertical-align: middle;
  }
  .t-member-table tbody tr:last-child td {
    border-bottom: 0;
  }
  .t-add {
    margin-block-start: 12px;
    display: grid;
    grid-template-columns: 1fr 1fr 1fr auto;
    gap: 10px;
    align-items: end;
  }
  @media (max-width: 720px) {
    .t-add {
      grid-template-columns: 1fr;
    }
  }

  .member-cell {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    color: var(--text);
  }

  :global([dir='rtl']) :global(.rtl-flip) {
    transform: scaleX(-1);
  }
</style>
