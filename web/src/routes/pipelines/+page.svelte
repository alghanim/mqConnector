<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Pipeline, type Connection } from '$lib/api';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';

  let pipelines: Pipeline[] = [];
  let connections: Connection[] = [];
  let editing: Pipeline | null = null;
  let filterPathsRaw = '';
  let error = '';

  async function refresh() {
    try {
      [pipelines, connections] = await Promise.all([
        api.get<Pipeline[]>('/v1/pipelines').then((v) => v ?? []),
        api.get<Connection[]>('/v1/connections').then((v) => v ?? [])
      ]);
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    }
  }

  onMount(refresh);

  $: connOptions = connections.map((c) => ({ value: c.id || '', label: `${c.name} (${c.type})` }));

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
      } else {
        await api.post<Pipeline>('/v1/pipelines', editing);
      }
      editing = null;
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'save failed';
    }
  }
  async function remove(p: Pipeline) {
    if (!p.id) return;
    if (!confirm(`Delete pipeline "${p.name}"?`)) return;
    try {
      await api.del(`/v1/pipelines/${p.id}`);
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'delete failed';
    }
  }
  async function toggleEnabled(p: Pipeline) {
    if (!p.id) return;
    try {
      await api.put(`/v1/pipelines/${p.id}`, { ...p, enabled: !p.enabled });
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'toggle failed';
    }
  }
  async function reload() {
    try {
      await api.post('/v1/reload');
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'reload failed';
    }
  }
</script>

<div class="space-y-6 max-w-5xl">
  <div class="flex items-baseline justify-between">
    <h2 class="text-2xl font-semibold" style="color: var(--text)">Pipelines</h2>
    <div class="flex gap-2">
      <Button variant="ghost" on:click={reload}>Reload all</Button>
      <Button on:click={startNew}>Add pipeline</Button>
    </div>
  </div>

  {#if error}
    <p style="color: var(--danger)">{error}</p>
  {/if}

  {#if editing}
    <Card strip>
      <p class="section-heading mb-4">{editing.id ? 'Edit pipeline' : 'New pipeline'}</p>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <Input bind:value={editing.name} label="Name" required />
        <Select
          bind:value={editing.output_format}
          label="Output format"
          options={[
            { value: 'same', label: 'Same as source' },
            { value: 'json', label: 'JSON' },
            { value: 'xml', label: 'XML' }
          ]}
        />
        <Select bind:value={editing.source_id} label="Source connection" options={connOptions} />
        <Select
          bind:value={editing.destination_id}
          label="Destination connection"
          options={connOptions}
        />
      </div>
      <div class="mt-4">
        <Input bind:value={filterPathsRaw} label="Filter paths (comma-separated dot paths)" />
      </div>
      <label class="mt-4 flex items-center gap-2 text-sm" style="color: var(--text)">
        <input type="checkbox" bind:checked={editing.enabled} />
        Enabled
      </label>
      <div class="flex gap-2 justify-end mt-5">
        <Button variant="ghost" on:click={cancel}>Cancel</Button>
        <Button on:click={save}>Save</Button>
      </div>
    </Card>
  {/if}

  <Card>
    {#if pipelines.length === 0}
      <p style="color: var(--text-muted)">No pipelines yet.</p>
    {:else}
      <table class="table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Source → Destination</th>
            <th>Output</th>
            <th>Status</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each pipelines as p}
            {@const src = connections.find((c) => c.id === p.source_id)}
            {@const dst = connections.find((c) => c.id === p.destination_id)}
            <tr>
              <td style="color: var(--text)">{p.name}</td>
              <td style="color: var(--text-muted)">
                {src?.name || '?'} → {dst?.name || '?'}
              </td>
              <td><Badge variant="neutral">{p.output_format}</Badge></td>
              <td>
                {#if p.enabled}
                  <Badge variant="success">enabled</Badge>
                {:else}
                  <Badge variant="warning">disabled</Badge>
                {/if}
              </td>
              <td>
                <div class="flex gap-2 justify-end">
                  <Button variant="ghost" on:click={() => toggleEnabled(p)}>
                    {p.enabled ? 'Disable' : 'Enable'}
                  </Button>
                  <Button variant="ghost" on:click={() => startEdit(p)}>Edit</Button>
                  <Button variant="outline" on:click={() => remove(p)}>Delete</Button>
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
  td:last-child { text-align: end; }
</style>
