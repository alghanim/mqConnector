<!--
  /connections — MQ endpoint registry.

  Page anatomy:
    PageHeader        title + count + subtitle + "Add" CTA + StatChips
                      (totals split by type)
    Filter bar        Search by name + type filter
    Table             Sortable columns, icon-per-type, status badges,
                      last-tested ms, inline test button, edit & delete.
                      Hover reveals row affordances.
    Empty state       EmptyState illustration + CTA
    Dialog            Confirm-delete
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Connection, type ConnectionType } from '$lib/api';
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
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatChip from '$lib/components/StatChip.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  import {
    Plus,
    Search as SearchIcon,
    Rabbit,
    Server,
    Database,
    Pencil,
    Trash2,
    PlayCircle,
    CheckCircle2,
    XCircle,
    Loader2
  } from 'lucide-svelte';

  let connections: Connection[] = [];
  let loading = true;
  let editing: Connection | null = null;
  let error = '';
  let pendingDelete: Connection | null = null;
  let deleting = false;
  let testing: Record<string, 'idle' | 'pending' | 'ok' | 'fail'> = {};
  let testMsg: Record<string, string> = {};

  // Filter bar state.
  let query = '';
  let typeFilter: '' | ConnectionType = '';
  let sortBy: 'name' | 'type' = 'name';

  const typeOptions = [
    { value: 'rabbitmq', label: 'RabbitMQ' },
    { value: 'kafka', label: 'Kafka' },
    { value: 'ibm', label: 'IBM MQ' },
    { value: 'mqtt', label: 'MQTT' },
    { value: 'nats', label: 'NATS / JetStream' },
    { value: 'amqp10', label: 'AMQP 1.0' }
  ];
  $: filterOptions = [
    { value: '', label: t($locale, 'connections.allTypes') ?? 'All types' },
    ...typeOptions
  ];

  async function refresh() {
    loading = true;
    try {
      connections = (await api.get<Connection[]>('/v1/connections')) ?? [];
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void refresh();
    // Auto-open the new-form when /connections?new=1 (used by Command Palette).
    if ($page.url.searchParams.get('new') === '1') {
      startNew();
    }
  });

  function startNew() {
    editing = { name: '', type: 'rabbitmq' as ConnectionType };
  }
  function startEdit(c: Connection) {
    editing = { ...c };
  }
  function cancel() {
    editing = null;
  }
  async function save() {
    if (!editing) return;
    try {
      if (editing.id) {
        await api.put<Connection>(`/v1/connections/${editing.id}`, editing);
        toasts.success(t($locale, 'connections.toast.saved'), editing.name);
      } else {
        await api.post<Connection>('/v1/connections', editing);
        toasts.success(t($locale, 'connections.toast.created'), editing.name);
      }
      editing = null;
      await refresh();
    } catch (e: unknown) {
      const msg = (e as { message?: string }).message || 'save failed';
      error = msg;
      toasts.error(t($locale, 'connections.toast.saveFailed'), msg);
    }
  }
  function askRemove(c: Connection) {
    if (!c.id) return;
    pendingDelete = c;
  }
  async function confirmRemove() {
    if (!pendingDelete?.id) return;
    deleting = true;
    try {
      await api.del(`/v1/connections/${pendingDelete.id}`);
      toasts.success(t($locale, 'connections.toast.deleted'), pendingDelete.name);
      pendingDelete = null;
      await refresh();
    } catch (e: unknown) {
      const msg = (e as { message?: string }).message || 'delete failed';
      error = msg;
      toasts.error(t($locale, 'connections.toast.deleteFailed'), msg);
    } finally {
      deleting = false;
    }
  }
  async function testConn(c: Connection) {
    if (!c.id) return;
    testing[c.id] = 'pending';
    testing = testing;
    try {
      const res = await api.post<{ ok: boolean; elapsed_ms: number; error?: string }>(
        `/v1/connections/${c.id}/test`
      );
      if (res.ok) {
        testing[c.id] = 'ok';
        testMsg[c.id] = `${res.elapsed_ms} ms`;
        toasts.success(`${t($locale, 'connections.test.success')}`, `${c.name} · ${res.elapsed_ms} ms`);
      } else {
        testing[c.id] = 'fail';
        testMsg[c.id] = res.error || 'failed';
        toasts.error(`${t($locale, 'connections.test.failure')}: ${c.name}`, res.error);
      }
    } catch (e: unknown) {
      testing[c.id] = 'fail';
      testMsg[c.id] = (e as { message?: string }).message || 'failed';
    }
    testing = testing;
    testMsg = testMsg;
  }

  function endpointOf(c: Connection): string {
    if (c.type === 'ibm') return `${c.queue_manager ?? ''} @ ${c.conn_name ?? ''}`;
    if (c.type === 'rabbitmq') return c.url ?? '';
    if (c.type === 'kafka') return c.brokers ?? '';
    return '';
  }

  $: filtered = connections
    .filter((c) => (!typeFilter || c.type === typeFilter))
    .filter((c) => {
      if (!query.trim()) return true;
      const q = query.toLowerCase();
      return (
        c.name.toLowerCase().includes(q) ||
        (c.queue_name ?? '').toLowerCase().includes(q) ||
        endpointOf(c).toLowerCase().includes(q)
      );
    })
    .sort((a, b) => {
      if (sortBy === 'type') return a.type.localeCompare(b.type) || a.name.localeCompare(b.name);
      return a.name.localeCompare(b.name);
    });

  $: countsByType = connections.reduce<Record<string, number>>((acc, c) => {
    acc[c.type] = (acc[c.type] ?? 0) + 1;
    return acc;
  }, {});
</script>

<PageHeader
  title={t($locale, 'connections.title')}
  subtitle={t($locale, 'connections.pageSubtitle')}
  count={connections.length}
>
  <svelte:fragment slot="primary">
    <Button on:click={startNew}>
      <Plus size={14} aria-hidden="true" />
      <span class="ms-1">{t($locale, 'connections.add')}</span>
    </Button>
  </svelte:fragment>

  <svelte:fragment slot="stats">
    <StatChip label="RabbitMQ" value={countsByType.rabbitmq ?? 0} />
    <StatChip label="Kafka" value={countsByType.kafka ?? 0} />
    <StatChip label="IBM MQ" value={countsByType.ibm ?? 0} />
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
      <Select bind:value={typeFilter} options={filterOptions} />
    </div>
  </svelte:fragment>
</PageHeader>

{#if error}
  <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
{/if}

{#if editing}
  <Card strip>
    <p class="section-heading mb-4">
      {editing.id ? t($locale, 'connections.edit') : t($locale, 'connections.new')}
    </p>
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
      <Input bind:value={editing.name} label={t($locale, 'connections.name')} required />
      <Select bind:value={editing.type} label={t($locale, 'connections.type')} options={typeOptions} />

      {#if editing.type === 'ibm'}
        <Input bind:value={editing.queue_manager} label={t($locale, 'connections.queueManager')} />
        <Input bind:value={editing.conn_name} label={t($locale, 'connections.connName')} />
        <Input bind:value={editing.channel} label={t($locale, 'connections.channel')} />
        <Input bind:value={editing.queue_name} label={t($locale, 'connections.queueName')} />
        <Input bind:value={editing.username} label={t($locale, 'connections.username')} />
        <Input bind:value={editing.password} type="password" label={t($locale, 'connections.password')} />
      {:else if editing.type === 'rabbitmq'}
        <Input bind:value={editing.url} label={t($locale, 'connections.amqpUrl')} placeholder="amqp://user:pw@host/" />
        <Input bind:value={editing.queue_name} label={t($locale, 'connections.queueName')} />
      {:else if editing.type === 'kafka'}
        <Input bind:value={editing.brokers} label={t($locale, 'connections.brokers')} />
        <Input bind:value={editing.topic} label={t($locale, 'connections.topic')} />
        <Input
          bind:value={editing.group_id}
          label={t($locale, 'connections.groupId')}
          placeholder={t($locale, 'connections.groupIdPlaceholder')}
        />
      {:else if editing.type === 'mqtt'}
        <!-- MQTT: URL scheme picks TLS (mqtt:// vs ssl://). Topic
             can use + and # wildcards on subscribe. ClientID must
             be unique per broker — leave blank to auto-generate. -->
        <Input bind:value={editing.url} label={t($locale, 'connections.mqttUrl')} placeholder="tcp://host:1883" />
        <Input bind:value={editing.topic} label={t($locale, 'connections.topic')} placeholder="sensors/temp" />
        <Input bind:value={editing.client_id} label={t($locale, 'connections.clientId')} placeholder="auto-generated" />
        <Input bind:value={editing.username} label={t($locale, 'connections.username')} />
        <Input bind:value={editing.password} type="password" label={t($locale, 'connections.password')} />
        <Input bind:value={editing.qos} type="number" label={t($locale, 'connections.qos')} />
      {:else if editing.type === 'nats'}
        <!-- NATS: leaving stream + consumer blank → core NATS (fire-
             and-forget). Setting both → JetStream (durable). Subject
             goes in Topic. -->
        <Input bind:value={editing.url} label={t($locale, 'connections.natsUrl')} placeholder="nats://host:4222" />
        <Input bind:value={editing.topic} label={t($locale, 'connections.subject')} placeholder="events.>" />
        <Input bind:value={editing.stream_name} label={t($locale, 'connections.streamName')} placeholder={t($locale, 'connections.streamName.placeholder')} />
        <Input bind:value={editing.consumer_name} label={t($locale, 'connections.consumerName')} placeholder={t($locale, 'connections.consumerName.placeholder')} />
        <Input bind:value={editing.username} label={t($locale, 'connections.username')} />
        <Input bind:value={editing.password} type="password" label={t($locale, 'connections.password')} />
      {:else if editing.type === 'amqp10'}
        <!-- AMQP 1.0 (the standard, not RabbitMQ's 0.9.1). Covers
             Azure Service Bus, ActiveMQ Artemis, Solace. -->
        <Input bind:value={editing.url} label={t($locale, 'connections.amqp10Url')} placeholder="amqps://host:5671" />
        <Input bind:value={editing.topic} label={t($locale, 'connections.address')} placeholder="queue-or-topic-name" />
        <Input bind:value={editing.client_id} label={t($locale, 'connections.containerId')} placeholder="mqconnector" />
        <Input bind:value={editing.username} label={t($locale, 'connections.username')} />
        <Input bind:value={editing.password} type="password" label={t($locale, 'connections.password')} />
      {/if}
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
      {#each Array(5) as _, i (i)}
        <div class="skel-row">
          <Skeleton width="22px" height="22px" radius="6px" />
          <Skeleton width="34%" height="0.85em" />
          <Skeleton width="20%" height="0.85em" />
          <Skeleton width="28%" height="0.85em" />
        </div>
      {/each}
    </div>
  {:else if connections.length === 0}
    <EmptyState
      illustration="connections"
      title={t($locale, 'empty.connections.title')}
      body={t($locale, 'empty.connections.body')}
    >
      <svelte:fragment slot="action">
        <Button on:click={startNew}>
          <Plus size={14} aria-hidden="true" />
          <span class="ms-1">{t($locale, 'connections.add')}</span>
        </Button>
      </svelte:fragment>
    </EmptyState>
  {:else if filtered.length === 0}
    <div class="empty-filter">
      <p class="text-muted">{t($locale, 'common.none')}</p>
    </div>
  {:else}
    <table class="conn-table">
      <thead>
        <tr>
          <th aria-sort={sortBy === 'name' ? 'ascending' : 'none'}>
            <button type="button" class="th-sort" on:click={() => (sortBy = 'name')}>
              {t($locale, 'connections.name')}
            </button>
          </th>
          <th aria-sort={sortBy === 'type' ? 'ascending' : 'none'}>
            <button type="button" class="th-sort" on:click={() => (sortBy = 'type')}>
              {t($locale, 'connections.type')}
            </button>
          </th>
          <th>{t($locale, 'connections.endpoint')}</th>
          <th>{t($locale, 'connections.queueName')}</th>
          <th class="th-actions"></th>
        </tr>
      </thead>
      <tbody>
        {#each filtered as c (c.id || c.name)}
          <tr>
            <td>
              <div class="cell-name">
                <span class="cell-type-icon" data-type={c.type} aria-hidden="true">
                  {#if c.type === 'rabbitmq'}
                    <Rabbit size={14} />
                  {:else if c.type === 'kafka'}
                    <Server size={14} />
                  {:else}
                    <Database size={14} />
                  {/if}
                </span>
                <span class="cell-name-text">{c.name}</span>
                {#if c.id && testing[c.id] === 'ok'}
                  <span class="test-pill ok" title={testMsg[c.id]}>
                    <CheckCircle2 size={12} aria-hidden="true" />
                    {testMsg[c.id]}
                  </span>
                {:else if c.id && testing[c.id] === 'fail'}
                  <span class="test-pill fail" title={testMsg[c.id]}>
                    <XCircle size={12} aria-hidden="true" />
                    {t($locale, 'connections.test.failure')}
                  </span>
                {/if}
              </div>
            </td>
            <td>
              <Badge variant="neutral">{c.type}</Badge>
            </td>
            <td class="cell-mono">{endpointOf(c) || '—'}</td>
            <td class="cell-mono">{c.queue_name || c.topic || '—'}</td>
            <td>
              <div class="row-actions">
                <button
                  type="button"
                  class="icon-action"
                  aria-label={t($locale, 'connections.test')}
                  title={t($locale, 'connections.test')}
                  disabled={c.id ? testing[c.id] === 'pending' : false}
                  on:click={() => testConn(c)}
                >
                  {#if c.id && testing[c.id] === 'pending'}
                    <Loader2 size={14} class="spin" aria-hidden="true" />
                  {:else}
                    <PlayCircle size={14} aria-hidden="true" />
                  {/if}
                </button>
                <button
                  type="button"
                  class="icon-action"
                  aria-label={t($locale, 'common.edit')}
                  title={t($locale, 'common.edit')}
                  on:click={() => startEdit(c)}
                >
                  <Pencil size={14} aria-hidden="true" />
                </button>
                <button
                  type="button"
                  class="icon-action danger"
                  aria-label={t($locale, 'common.delete')}
                  title={t($locale, 'common.delete')}
                  on:click={() => askRemove(c)}
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
    <p class="text-muted" style="margin-top: 0.25rem">{endpointOf(pendingDelete)}</p>
  {/if}
</Dialog>

<style>
  /* ─── filter bar ──────────────────────────────────────────────── */
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
  .filter-select :global(select) {
    min-width: 8rem;
  }

  /* ─── table ──────────────────────────────────────────────────── */
  .conn-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.8125rem;
  }
  .conn-table thead th {
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
  .th-sort {
    background: transparent;
    border: 0;
    color: inherit;
    font: inherit;
    cursor: pointer;
    padding: 0;
  }
  .th-sort:hover {
    color: var(--text);
  }
  .th-actions {
    width: 1%;
  }
  .conn-table tbody tr {
    transition: background-color 100ms;
  }
  .conn-table tbody tr:hover {
    background: var(--surface-2);
  }
  .conn-table td {
    padding: 0.625rem 0.75rem;
    border-bottom: 1px solid var(--border);
    color: var(--text);
  }
  .conn-table tbody tr:last-child td {
    border-bottom: 0;
  }
  .cell-name {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .cell-name-text {
    font-weight: 500;
  }
  .cell-mono {
    font-family: 'SFMono-Regular', Menlo, monospace;
    font-size: 0.75rem;
    color: var(--text-muted);
    max-width: 22rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* per-type colour hint, very subtle */
  .cell-type-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    border-radius: 6px;
    background: var(--surface-2);
    color: var(--text-muted);
  }
  .cell-type-icon[data-type='rabbitmq'] {
    color: #ff6600;
  }
  .cell-type-icon[data-type='kafka'] {
    color: #6b6b6b;
  }
  .cell-type-icon[data-type='ibm'] {
    color: #1f70c1;
  }

  /* test status pill — narrow, inline with the name */
  .test-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 6px;
    border-radius: 999px;
    font-size: 0.6875rem;
    font-weight: 500;
    margin-inline-start: 6px;
  }
  .test-pill.ok {
    background: color-mix(in srgb, var(--success) 16%, transparent);
    color: var(--success);
  }
  .test-pill.fail {
    background: color-mix(in srgb, var(--danger) 16%, transparent);
    color: var(--danger);
  }

  /* row actions */
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
  .icon-action:disabled {
    cursor: progress;
    opacity: 0.6;
  }
  :global(.spin) {
    animation: spin 0.9s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    :global(.spin) {
      animation: none !important;
    }
  }

  .text-muted {
    color: var(--text-muted);
  }
  .empty-filter {
    padding: 2rem 1rem;
    text-align: center;
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
