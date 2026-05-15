<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Connection, type ConnectionType } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Dialog from '$lib/components/Dialog.svelte';

  let connections: Connection[] = [];
  let editing: Connection | null = null;
  let error = '';
  // Confirmation dialog state — Dialog replaces window.confirm() so the
  // destructive prompt sits inside the app's brand surface.
  let pendingDelete: Connection | null = null;
  let deleting = false;
  let testing: Record<string, 'idle' | 'pending' | 'ok' | 'fail'> = {};
  let testMsg: Record<string, string> = {};

  const typeOptions = [
    { value: 'rabbitmq', label: 'RabbitMQ' },
    { value: 'kafka', label: 'Kafka' },
    { value: 'ibm', label: 'IBM MQ' }
  ];

  async function refresh() {
    try {
      connections = (await api.get<Connection[]>('/v1/connections')) ?? [];
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    }
  }

  onMount(refresh);

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
      } else {
        await api.post<Connection>('/v1/connections', editing);
      }
      editing = null;
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'save failed';
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
      pendingDelete = null;
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'delete failed';
    } finally {
      deleting = false;
    }
  }

  async function testConn(c: Connection) {
    if (!c.id) return;
    testing[c.id] = 'pending';
    testing = testing; // notify svelte
    try {
      const res = await api.post<{ ok: boolean; elapsed_ms: number; error?: string }>(
        `/v1/connections/${c.id}/test`
      );
      if (res.ok) {
        testing[c.id] = 'ok';
        testMsg[c.id] = `${res.elapsed_ms}ms`;
      } else {
        testing[c.id] = 'fail';
        testMsg[c.id] = res.error || 'failed';
      }
    } catch (e: unknown) {
      testing[c.id] = 'fail';
      testMsg[c.id] = (e as { message?: string }).message || 'failed';
    }
    testing = testing;
    testMsg = testMsg;
  }
</script>

<div class="space-y-6 max-w-5xl">
  <div class="flex items-baseline justify-between">
    <h2 class="text-2xl font-semibold" style="color: var(--text)">
      {t($locale, 'connections.title')}
    </h2>
    <Button on:click={startNew}>{t($locale, 'connections.add')}</Button>
  </div>

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
          <Input bind:value={editing.url} label={t($locale, 'connections.amqpUrl')}
            placeholder="amqp://user:pw@host/" />
          <Input bind:value={editing.queue_name} label={t($locale, 'connections.queueName')} />
        {:else if editing.type === 'kafka'}
          <Input bind:value={editing.brokers} label={t($locale, 'connections.brokers')} />
          <Input bind:value={editing.topic} label={t($locale, 'connections.topic')} />
        {/if}
      </div>
      <div class="flex gap-2 justify-end mt-5">
        <Button variant="ghost" on:click={cancel}>{t($locale, 'common.cancel')}</Button>
        <Button on:click={save}>{t($locale, 'common.save')}</Button>
      </div>
    </Card>
  {/if}

  <Card>
    {#if connections.length === 0}
      <p style="color: var(--text-muted)">{t($locale, 'common.none')}</p>
    {:else}
      <table class="table">
        <thead>
          <tr>
            <th>{t($locale, 'connections.name')}</th>
            <th>{t($locale, 'connections.type')}</th>
            <th>{t($locale, 'connections.endpoint')}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each connections as c (c.id || c.name)}
            <tr>
              <td style="color: var(--text)">{c.name}</td>
              <td><Badge variant="neutral">{c.type}</Badge></td>
              <td style="color: var(--text-muted)">
                {#if c.type === 'ibm'}
                  {c.queue_manager} @ {c.conn_name}
                {:else if c.type === 'rabbitmq'}
                  {c.url}
                {:else if c.type === 'kafka'}
                  {c.brokers}
                {/if}
              </td>
              <td>
                <div class="flex gap-2 justify-end items-center">
                  {#if c.id && testing[c.id] === 'ok'}
                    <Badge variant="success">{t($locale, 'connections.test.success')} · {testMsg[c.id]}</Badge>
                  {:else if c.id && testing[c.id] === 'fail'}
                    <Badge variant="danger">{t($locale, 'connections.test.failure')}</Badge>
                  {/if}
                  <Button variant="ghost" loading={c.id ? testing[c.id] === 'pending' : false}
                    on:click={() => testConn(c)}>{t($locale, 'connections.test')}</Button>
                  <Button variant="ghost" on:click={() => startEdit(c)}>{t($locale, 'common.edit')}</Button>
                  <Button variant="outline" on:click={() => askRemove(c)}>{t($locale, 'common.delete')}</Button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </Card>
</div>

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
    <p>
      {t($locale, 'connections.delete.confirm')}
      <strong>{pendingDelete.name}</strong>?
    </p>
  {/if}
</Dialog>

<style>
  td:last-child { text-align: end; }
</style>
