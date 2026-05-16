<!--
  /pipelines — pipeline registry. Each row shows source → destination
  visually (two type-icons connected by a thin arrow) so the operator
  doesn't have to read endpoint names to understand wiring.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Pipeline, type Connection } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import { toasts } from '$lib/stores/toasts';
  import { page } from '$app/stores';

  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatChip from '$lib/components/StatChip.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  import {
    Plus,
    Search as SearchIcon,
    RotateCw,
    ArrowRight,
    Power,
    PowerOff,
    Pencil,
    Trash2,
    Settings2,
    GitFork,
    Rabbit,
    Server,
    Database
  } from 'lucide-svelte';

  let pipelines: Pipeline[] = [];
  let connections: Connection[] = [];
  let editing: Pipeline | null = null;
  let filterPathsRaw = '';
  let error = '';
  let pendingDelete: Pipeline | null = null;
  let deleting = false;
  let loading = true;

  let query = '';
  let statusFilter: '' | 'enabled' | 'disabled' = '';

  async function refresh() {
    loading = true;
    try {
      [pipelines, connections] = await Promise.all([
        api.get<Pipeline[]>('/v1/pipelines').then((v) => v ?? []),
        api.get<Connection[]>('/v1/connections').then((v) => v ?? [])
      ]);
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void refresh();
    if ($page.url.searchParams.get('new') === '1') {
      startNew();
    }
  });

  $: connOptions = connections.map((c) => ({
    value: c.id || '',
    label: `${c.name} (${c.type})`
  }));
  $: outputOptions = [
    { value: 'same', label: t($locale, 'pipelines.outputFormat.same') },
    { value: 'json', label: 'JSON' },
    { value: 'xml', label: 'XML' }
  ];
  $: statusFilterOptions = [
    { value: '', label: t($locale, 'common.all') ?? 'All' },
    { value: 'enabled', label: t($locale, 'common.enabled') },
    { value: 'disabled', label: t($locale, 'common.disabled') }
  ];

  function startNew() {
    editing = {
      name: '',
      source_id: connections[0]?.id || '',
      destination_id: connections[0]?.id || '',
      output_format: 'same',
      filter_paths: [],
      enabled: true
    };
    filterPathsRaw = '';
  }
  function startEdit(p: Pipeline) {
    editing = { ...p, filter_paths: [...p.filter_paths] };
    filterPathsRaw = (p.filter_paths || []).join(', ');
  }
  function cancel() {
    editing = null;
  }
  async function save() {
    if (!editing) return;
    editing.filter_paths = filterPathsRaw
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    try {
      if (editing.id) {
        await api.put<Pipeline>(`/v1/pipelines/${editing.id}`, editing);
        toasts.success(t($locale, 'pipelines.toast.saved'), editing.name);
      } else {
        await api.post<Pipeline>('/v1/pipelines', editing);
        toasts.success(t($locale, 'pipelines.toast.created'), editing.name);
      }
      editing = null;
      await refresh();
    } catch (e: unknown) {
      const msg = (e as { message?: string }).message || 'save failed';
      error = msg;
      toasts.error(t($locale, 'pipelines.toast.saveFailed'), msg);
    }
  }
  function askRemove(p: Pipeline) {
    if (!p.id) return;
    pendingDelete = p;
  }
  async function confirmRemove() {
    if (!pendingDelete?.id) return;
    deleting = true;
    try {
      await api.del(`/v1/pipelines/${pendingDelete.id}`);
      toasts.success(t($locale, 'pipelines.toast.deleted'), pendingDelete.name);
      pendingDelete = null;
      await refresh();
    } catch (e: unknown) {
      const msg = (e as { message?: string }).message || 'delete failed';
      error = msg;
      toasts.error(t($locale, 'pipelines.toast.deleteFailed'), msg);
    } finally {
      deleting = false;
    }
  }
  async function toggleEnabled(p: Pipeline) {
    if (!p.id) return;
    try {
      await api.put(`/v1/pipelines/${p.id}`, { ...p, enabled: !p.enabled });
      toasts.success(
        !p.enabled ? t($locale, 'pipelines.toast.enabled') : t($locale, 'pipelines.toast.disabled'),
        p.name
      );
      await refresh();
    } catch (e: unknown) {
      const msg = (e as { message?: string }).message || 'toggle failed';
      error = msg;
      toasts.error(t($locale, 'pipelines.toast.toggleFailed'), msg);
    }
  }
  async function reload() {
    try {
      const res = await api.post<{ started: number }>('/v1/reload');
      toasts.success(t($locale, 'pipelines.toast.reloaded'), `${res.started ?? '?'} pipelines`);
      await refresh();
    } catch (e: unknown) {
      const msg = (e as { message?: string }).message || 'reload failed';
      error = msg;
      toasts.error(t($locale, 'pipelines.toast.reloadFailed'), msg);
    }
  }

  function typeIcon(t: string) {
    if (t === 'rabbitmq') return Rabbit;
    if (t === 'kafka') return Server;
    return Database;
  }

  $: filtered = pipelines
    .filter((p) =>
      statusFilter === 'enabled'
        ? p.enabled
        : statusFilter === 'disabled'
          ? !p.enabled
          : true
    )
    .filter((p) => !query.trim() || p.name.toLowerCase().includes(query.toLowerCase()));

  $: enabledCount = pipelines.filter((p) => p.enabled).length;
</script>

<PageHeader
  title={t($locale, 'pipelines.title')}
  subtitle={t($locale, 'pipelines.pageSubtitle')}
  count={pipelines.length}
>
  <svelte:fragment slot="secondary">
    <Button variant="ghost" on:click={reload}>
      <RotateCw size={14} aria-hidden="true" />
      <span class="ms-1">{t($locale, 'common.reload')}</span>
    </Button>
  </svelte:fragment>
  <svelte:fragment slot="primary">
    <Button on:click={startNew}>
      <Plus size={14} aria-hidden="true" />
      <span class="ms-1">{t($locale, 'pipelines.add')}</span>
    </Button>
  </svelte:fragment>

  <svelte:fragment slot="stats">
    <StatChip
      label={t($locale, 'common.enabled')}
      value={enabledCount}
      tone={enabledCount > 0 ? 'success' : 'default'}
    />
    <StatChip
      label={t($locale, 'common.disabled')}
      value={pipelines.length - enabledCount}
      tone={pipelines.length - enabledCount > 0 ? 'warning' : 'default'}
    />
  </svelte:fragment>

  <svelte:fragment slot="filters">
    <div class="filter-search">
      <SearchIcon size={14} aria-hidden="true" />
      <input
        bind:value={query}
        placeholder={t($locale, 'common.search')}
        aria-label={t($locale, 'common.search')}
      />
    </div>
    <div class="filter-select">
      <Select bind:value={statusFilter} options={statusFilterOptions} />
    </div>
  </svelte:fragment>
</PageHeader>

{#if error}
  <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
{/if}

{#if editing}
  <Card strip>
    <p class="section-heading mb-4">
      {editing.id ? t($locale, 'pipelines.edit') : t($locale, 'pipelines.new')}
    </p>
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
      <Input bind:value={editing.name} label={t($locale, 'connections.name')} required />
      <Select bind:value={editing.output_format} label={t($locale, 'pipelines.outputFormat')} options={outputOptions} />
      <Select bind:value={editing.source_id} label={t($locale, 'pipelines.source')} options={connOptions} />
      <Select bind:value={editing.destination_id} label={t($locale, 'pipelines.destination')} options={connOptions} />
    </div>
    <div class="mt-4">
      <Input bind:value={filterPathsRaw} label={t($locale, 'pipelines.filterPaths')} />
    </div>
    <div class="mt-4">
      <Switch bind:checked={editing.enabled} label={t($locale, 'common.enabled')} />
    </div>
    <div class="flex gap-2 justify-end mt-5">
      <Button variant="ghost" on:click={cancel}>{t($locale, 'common.cancel')}</Button>
      <Button on:click={save}>{t($locale, 'common.save')}</Button>
    </div>
  </Card>
{/if}

<Card>
  {#if loading}
    <div class="skel-rows">
      {#each Array(4) as _, i (i)}
        <div class="skel-row">
          <Skeleton width="38%" height="0.85em" />
          <Skeleton width="40%" height="0.85em" />
          <Skeleton width="12%" height="0.85em" />
          <Skeleton width="10%" height="0.85em" />
        </div>
      {/each}
    </div>
  {:else if pipelines.length === 0}
    <EmptyState
      illustration="pipelines"
      title={t($locale, 'empty.pipelines.title')}
      body={t($locale, 'empty.pipelines.body')}
    >
      <svelte:fragment slot="action">
        <Button on:click={startNew}>
          <Plus size={14} aria-hidden="true" />
          <span class="ms-1">{t($locale, 'pipelines.add')}</span>
        </Button>
      </svelte:fragment>
    </EmptyState>
  {:else if filtered.length === 0}
    <p class="empty-filter">{t($locale, 'common.none')}</p>
  {:else}
    <table class="pipe-table">
      <thead>
        <tr>
          <th>{t($locale, 'connections.name')}</th>
          <th>{t($locale, 'pipelines.flow')}</th>
          <th>{t($locale, 'pipelines.output')}</th>
          <th>{t($locale, 'common.status')}</th>
          <th class="th-actions"></th>
        </tr>
      </thead>
      <tbody>
        {#each filtered as p (p.id || p.name)}
          {@const src = connections.find((c) => c.id === p.source_id)}
          {@const dst = connections.find((c) => c.id === p.destination_id)}
          {@const SrcIcon = typeIcon(src?.type || '')}
          {@const DstIcon = typeIcon(dst?.type || '')}
          <tr>
            <td>
              <div class="cell-name">
                <span class="cell-icon" aria-hidden="true">
                  <GitFork size={14} />
                </span>
                <span class="cell-name-text">{p.name}</span>
              </div>
            </td>
            <td>
              <div class="flow">
                <span class="flow-end">
                  <span class="flow-ico" data-type={src?.type || ''} aria-hidden="true">
                    <svelte:component this={SrcIcon} size={12} />
                  </span>
                  <span class="flow-name">{src?.name || '?'}</span>
                </span>
                <ArrowRight size={14} aria-hidden="true" class="flow-arr" />
                <span class="flow-end">
                  <span class="flow-ico" data-type={dst?.type || ''} aria-hidden="true">
                    <svelte:component this={DstIcon} size={12} />
                  </span>
                  <span class="flow-name">{dst?.name || '?'}</span>
                </span>
              </div>
            </td>
            <td>
              <Badge variant="neutral">{p.output_format}</Badge>
            </td>
            <td>
              {#if p.enabled}
                <Badge variant="success">{t($locale, 'common.enabled')}</Badge>
              {:else}
                <Badge variant="warning">{t($locale, 'common.disabled')}</Badge>
              {/if}
            </td>
            <td>
              <div class="row-actions">
                {#if p.id}
                  <a
                    class="icon-action"
                    href="/flow?pipeline={p.id}"
                    aria-label={t($locale, 'flow.openVisual')}
                    title={t($locale, 'flow.openVisual')}
                  >
                    <GitFork size={14} aria-hidden="true" />
                  </a>
                  <a
                    class="icon-action"
                    href="/pipelines/{p.id}"
                    aria-label={t($locale, 'pipelines.configure')}
                    title={t($locale, 'pipelines.configure')}
                  >
                    <Settings2 size={14} aria-hidden="true" />
                  </a>
                {/if}
                <button
                  type="button"
                  class="icon-action"
                  aria-label={p.enabled ? t($locale, 'common.disable') : t($locale, 'common.enable')}
                  title={p.enabled ? t($locale, 'common.disable') : t($locale, 'common.enable')}
                  on:click={() => toggleEnabled(p)}
                >
                  {#if p.enabled}
                    <PowerOff size={14} aria-hidden="true" />
                  {:else}
                    <Power size={14} aria-hidden="true" />
                  {/if}
                </button>
                <button
                  type="button"
                  class="icon-action"
                  aria-label={t($locale, 'common.edit')}
                  title={t($locale, 'common.edit')}
                  on:click={() => startEdit(p)}
                >
                  <Pencil size={14} aria-hidden="true" />
                </button>
                <button
                  type="button"
                  class="icon-action danger"
                  aria-label={t($locale, 'common.delete')}
                  title={t($locale, 'common.delete')}
                  on:click={() => askRemove(p)}
                >
                  <Trash2 size={14} aria-hidden="true" />
                </button>
              </div>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</Card>

<Dialog
  open={pendingDelete !== null}
  title={t($locale, 'common.confirmDelete')}
  confirmLabel={t($locale, 'common.delete')}
  cancelLabel={t($locale, 'common.cancel')}
  busy={deleting}
  on:cancel={() => (pendingDelete = null)}
  on:confirm={confirmRemove}
>
  {#if pendingDelete}
    <p style="color: var(--text)"><strong>{pendingDelete.name}</strong></p>
  {/if}
</Dialog>

<style>
  .filter-search {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0 0.625rem;
    border-radius: 8px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text-muted);
    min-width: 14rem;
  }
  .filter-search input {
    flex: 1;
    background: transparent;
    border: 0;
    color: var(--text);
    font: inherit;
    font-size: 0.8125rem;
    outline: none;
    padding-block: 0.5rem;
  }

  .pipe-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.8125rem;
  }
  .pipe-table thead th {
    text-align: start;
    padding: 0.5rem 0.75rem;
    color: var(--text-muted);
    font-size: 0.6875rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom: 1px solid var(--border);
    background: var(--surface);
    position: sticky;
    top: 0;
  }
  .pipe-table tbody tr {
    transition: background-color 100ms;
  }
  .pipe-table tbody tr:hover {
    background: var(--surface-2);
  }
  .pipe-table td {
    padding: 0.625rem 0.75rem;
    border-bottom: 1px solid var(--border);
    color: var(--text);
  }
  .pipe-table tbody tr:last-child td {
    border-bottom: 0;
  }
  .th-actions {
    width: 1%;
  }

  .cell-name {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .cell-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    border-radius: 6px;
    background: var(--surface-2);
    color: var(--text-muted);
  }
  .cell-name-text {
    font-weight: 500;
  }

  /* flow viz */
  .flow {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: nowrap;
  }
  .flow-end {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 8px 3px 6px;
    border-radius: 999px;
    background: var(--surface-2);
    border: 1px solid var(--border);
  }
  .flow-ico {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--text-muted);
  }
  .flow-ico[data-type='rabbitmq'] {
    color: #ff6600;
  }
  .flow-ico[data-type='kafka'] {
    color: #6b6b6b;
  }
  .flow-ico[data-type='ibm'] {
    color: #1f70c1;
  }
  .flow-name {
    font-size: 0.75rem;
    color: var(--text);
  }
  :global(.flow-arr) {
    color: var(--text-tertiary);
  }
  :global([dir='rtl']) :global(.flow-arr) {
    transform: scaleX(-1);
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
    width: 28px;
    height: 28px;
    border-radius: 6px;
    background: transparent;
    border: 1px solid transparent;
    color: var(--text-muted);
    cursor: pointer;
    text-decoration: none;
    transition: all 120ms;
  }
  .icon-action:hover {
    background: var(--surface);
    border-color: var(--border);
    color: var(--text);
  }
  .icon-action.danger:hover {
    color: var(--danger);
    border-color: color-mix(in srgb, var(--danger) 35%, transparent);
  }

  .empty-filter {
    padding: 2rem 1rem;
    text-align: center;
    color: var(--text-muted);
  }
  .skel-rows {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.5rem;
  }
  .skel-row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }
</style>
