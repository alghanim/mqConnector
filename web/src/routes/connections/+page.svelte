<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Connection, type ConnectionType } from '$lib/api';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';

  let connections: Connection[] = [];
  let editing: Connection | null = null;
  let error = '';

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
  async function remove(c: Connection) {
    if (!c.id) return;
    if (!confirm(`Delete connection "${c.name}"?`)) return;
    try {
      await api.del(`/v1/connections/${c.id}`);
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'delete failed';
    }
  }
</script>

<div class="space-y-6 max-w-5xl">
  <div class="flex items-baseline justify-between">
    <h2 class="text-2xl font-semibold" style="color: var(--text)">Connections</h2>
    <Button on:click={startNew}>Add connection</Button>
  </div>

  {#if error}
    <p style="color: var(--danger)">{error}</p>
  {/if}

  {#if editing}
    <Card strip>
      <p class="section-heading mb-4">{editing.id ? 'Edit connection' : 'New connection'}</p>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <Input bind:value={editing.name} label="Name" required />
        <Select bind:value={editing.type} label="Type" options={typeOptions} />

        {#if editing.type === 'ibm'}
          <Input bind:value={editing.queue_manager} label="Queue manager" />
          <Input bind:value={editing.conn_name} label="Connection name (host(port))" />
          <Input bind:value={editing.channel} label="Channel" />
          <Input bind:value={editing.queue_name} label="Queue name" />
          <Input bind:value={editing.username} label="Username" />
          <Input bind:value={editing.password} type="password" label="Password" />
        {:else if editing.type === 'rabbitmq'}
          <Input bind:value={editing.url} label="AMQP URL" placeholder="amqp://user:pw@host/" />
          <Input bind:value={editing.queue_name} label="Queue name" />
        {:else if editing.type === 'kafka'}
          <Input bind:value={editing.brokers} label="Brokers (comma-separated)" />
          <Input bind:value={editing.topic} label="Topic" />
        {/if}
      </div>
      <div class="flex gap-2 justify-end mt-5">
        <Button variant="ghost" on:click={cancel}>Cancel</Button>
        <Button on:click={save}>Save</Button>
      </div>
    </Card>
  {/if}

  <Card>
    {#if connections.length === 0}
      <p style="color: var(--text-muted)">No connections yet.</p>
    {:else}
      <table class="table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Endpoint</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each connections as c}
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
                <div class="flex gap-2 justify-end">
                  <Button variant="ghost" on:click={() => startEdit(c)}>Edit</Button>
                  <Button variant="outline" on:click={() => remove(c)}>Delete</Button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </Card>
</div>

<style>
  /* tighten the table action column */
  td:last-child { text-align: end; }
</style>
