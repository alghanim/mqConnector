<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Pipeline, type Connection } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Switch from '$lib/components/Switch.svelte';

  let pipelines: Pipeline[] = [];
  let connections: Connection[] = [];
  let editing: Pipeline | null = null;
  let filterPathsRaw = '';
  let error = '';
  let pendingDelete: Pipeline | null = null;
  let deleting = false;

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
  $: outputOptions = [
    { value: 'same', label: t($locale, 'pipelines.outputFormat.same') },
    { value: 'json', label: 'JSON' },
    { value: 'xml', label: 'XML' }
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
      } else {
        await api.post<Pipeline>('/v1/pipelines', editing);
      }
      editing = null;
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'save failed';
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
      pendingDelete = null;
      await refresh();
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'delete failed';
    } finally {
      deleting = false;
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
    <h2 class="text-2xl font-semibold" style="color: var(--text)">
      {t($locale, 'pipelines.title')}
    </h2>
    <div class="flex gap-2">
      <Button variant="ghost" on:click={reload}>{t($locale, 'common.reload')}</Button>
      <Button on:click={startNew}>{t($locale, 'pipelines.add')}</Button>
    </div>
  </div>

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
        <Select bind:value={editing.output_format} label={t($locale, 'pipelines.outputFormat')}
          options={outputOptions} />
        <Select bind:value={editing.source_id} label={t($locale, 'pipelines.source')} options={connOptions} />
        <Select bind:value={editing.destination_id} label={t($locale, 'pipelines.destination')}
          options={connOptions} />
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
    {#if pipelines.length === 0}
      <p style="color: var(--text-muted)">{t($locale, 'common.none')}</p>
    {:else}
      <table class="table">
        <thead>
          <tr>
            <th>{t($locale, 'connections.name')}</th>
            <th>{t($locale, 'pipelines.flow')}</th>
            <th>{t($locale, 'pipelines.output')}</th>
            <th>{t($locale, 'common.status')}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each pipelines as p (p.id || p.name)}
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
                  <Badge variant="success">{t($locale, 'common.enabled')}</Badge>
                {:else}
                  <Badge variant="warning">{t($locale, 'common.disabled')}</Badge>
                {/if}
              </td>
              <td>
                <div class="flex gap-2 justify-end">
                  {#if p.id}
                    <a href="/pipelines/{p.id}" class="btn-link">{t($locale, 'pipelines.configure')}</a>
                  {/if}
                  <Button variant="ghost" on:click={() => toggleEnabled(p)}>
                    {p.enabled ? t($locale, 'common.disable') : t($locale, 'common.enable')}
                  </Button>
                  <Button variant="ghost" on:click={() => startEdit(p)}>{t($locale, 'common.edit')}</Button>
                  <Button variant="outline" on:click={() => askRemove(p)}>{t($locale, 'common.delete')}</Button>
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
    <p>{t($locale, 'pipelines.delete.confirm')} <strong>{pendingDelete.name}</strong>?</p>
  {/if}
</Dialog>

<style>
  td:last-child { text-align: end; }
  /*
   * .btn-link is the in-row "Configure" affordance. Rule 16 forbids
   * maroon outside the primary-CTA / count-badge surface set, so this
   * uses the gold-family (--primary) as an outlined chip — matches
   * the §5.4 Outlined button colour for consistency.
   */
  .btn-link {
    display: inline-flex; align-items: center;
    padding: 6px 12px;
    border-radius: 12px;
    color: var(--primary);
    border: 1px solid var(--primary);
    font-size: 13px;
    line-height: 1.2;
    text-decoration: none;
    transition: background-color 200ms, color 200ms;
  }
  .btn-link:hover {
    background: color-mix(in srgb, var(--primary) 12%, transparent);
  }
  .btn-link:active {
    background: color-mix(in srgb, var(--primary) 20%, transparent);
  }
</style>
