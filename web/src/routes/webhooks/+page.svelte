<!--
  /webhooks — outbound HTTP notification management.

  Webhooks deliver internal events (pipeline.{started,stopped,error},
  dlq.pushed, connection.test) to operator-registered URLs with
  HMAC-SHA256 signing. Receivers verify the X-MQC-Signature header.

  Page shape:
    - PageHeader with enabled/failing stat chips
    - List table: name, URL (truncated), event filter chips, enabled
      switch (inline toggle), last-delivery status badge, edit/delete
    - Empty state when none configured
    - Create / Edit dialog: name, URL, secret (with Generate helper),
      event-type checkboxes, enabled toggle
    - Delete confirm dialog
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type Webhook } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import { toasts } from '$lib/stores/toasts';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatChip from '$lib/components/StatChip.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { Plus, RefreshCw, Trash2, Edit2 } from 'lucide-svelte';

  // ─── State ────────────────────────────────────────────────────────
  let hooks: Webhook[] = [];
  let loading = true;

  // Edit / create form. New rows have id="" — the back-end mints one.
  let editing: Webhook | null = null;
  let saving = false;
  // The set of currently-ticked events for the form. "*" means all.
  let selectedEvents: Record<string, boolean> = {};
  let allEvents = false;

  let pendingDelete: Webhook | null = null;
  let deleting = false;

  const EVENT_TYPES = [
    'pipeline.started',
    'pipeline.stopped',
    'pipeline.error',
    'dlq.pushed',
    'connection.test'
  ];

  // ─── Data ─────────────────────────────────────────────────────────
  async function refresh() {
    loading = true;
    try {
      const res = await api.get<{ items: Webhook[] }>('/v1/webhooks');
      hooks = res.items ?? [];
    } catch (e: unknown) {
      toasts.error('Failed to load webhooks', (e as { message?: string }).message ?? '');
    } finally {
      loading = false;
    }
  }
  onMount(refresh);

  // ─── Create / Edit ────────────────────────────────────────────────
  function openCreate() {
    editing = blankWebhook();
    parseEventsIntoForm('*');
  }
  function openEdit(h: Webhook) {
    editing = { ...h };
    parseEventsIntoForm(h.events);
  }
  function blankWebhook(): Webhook {
    return {
      id: '',
      tenant_id: '',
      name: '',
      url: '',
      secret: '',
      events: '*',
      enabled: true,
      last_status: 0,
      last_error: '',
      created_at: '',
      updated_at: ''
    };
  }
  function parseEventsIntoForm(events: string) {
    selectedEvents = {};
    if (events === '*' || !events) {
      allEvents = true;
      return;
    }
    allEvents = false;
    for (const e of events.split(',').map((s) => s.trim()).filter(Boolean)) {
      selectedEvents[e] = true;
    }
  }
  function eventsFromForm(): string {
    if (allEvents) return '*';
    const list = EVENT_TYPES.filter((e) => selectedEvents[e]);
    return list.length ? list.join(',') : '*';
  }

  async function generateSecret() {
    // 32 bytes of crypto-random, hex-encoded. window.crypto is part of
    // every modern browser; svelte-check infers it as always-defined,
    // so we use it directly.
    if (!editing) return;
    const bytes = new Uint8Array(32);
    crypto.getRandomValues(bytes);
    editing.secret = Array.from(bytes)
      .map((b) => b.toString(16).padStart(2, '0'))
      .join('');
    editing = editing;
  }

  async function saveEditing() {
    if (!editing) return;
    saving = true;
    const payload = { ...editing, events: eventsFromForm() };
    try {
      let saved: Webhook;
      if (editing.id) {
        saved = await api.put<Webhook>(`/v1/webhooks/${editing.id}`, payload);
        hooks = hooks.map((h) => (h.id === saved.id ? saved : h));
        toasts.success('Webhook saved', saved.name);
      } else {
        saved = await api.post<Webhook>('/v1/webhooks', payload);
        hooks = [saved, ...hooks];
        toasts.success('Webhook created', saved.name);
      }
      editing = null;
    } catch (e: unknown) {
      const msg = (e as { message?: string }).message ?? 'save failed';
      toasts.error('Could not save webhook', msg);
    } finally {
      saving = false;
    }
  }

  // Inline enable/disable directly from the row toggle, without
  // opening the edit dialog — minor convenience, sees a lot of use
  // when an operator silences a flapping receiver.
  async function toggleEnabled(h: Webhook) {
    const updated = { ...h, enabled: !h.enabled };
    try {
      const saved = await api.put<Webhook>(`/v1/webhooks/${h.id}`, updated);
      hooks = hooks.map((x) => (x.id === saved.id ? saved : x));
    } catch (e: unknown) {
      toasts.error('Could not toggle webhook', (e as { message?: string }).message ?? '');
    }
  }

  // ─── Delete ───────────────────────────────────────────────────────
  function askDelete(h: Webhook) {
    pendingDelete = h;
  }
  async function confirmDelete() {
    if (!pendingDelete) return;
    deleting = true;
    try {
      await api.del(`/v1/webhooks/${pendingDelete.id}`);
      hooks = hooks.filter((h) => h.id !== pendingDelete!.id);
      pendingDelete = null;
    } catch (e: unknown) {
      toasts.error('Could not delete webhook', (e as { message?: string }).message ?? '');
    } finally {
      deleting = false;
    }
  }

  // ─── Derived ──────────────────────────────────────────────────────
  $: stats = hooks.reduce(
    (acc, h) => {
      if (h.enabled) acc.enabled++;
      // last_status outside 2xx is "failing" — last_status=0 means
      // we've never tried; don't flag that as a failure.
      if (h.last_status >= 400 || (h.last_status > 0 && h.last_status < 200)) {
        acc.failing++;
      }
      return acc;
    },
    { enabled: 0, failing: 0 }
  );

  function statusVariant(code: number): 'success' | 'warning' | 'danger' | 'neutral' {
    if (code === 0) return 'neutral';
    if (code >= 200 && code < 300) return 'success';
    if (code >= 400 && code < 500) return 'warning';
    if (code >= 500) return 'danger';
    return 'neutral';
  }
  function fmtDate(s: string | null | undefined): string {
    if (!s) return t($locale, 'webhooks.never');
    try {
      return new Date(s).toLocaleString();
    } catch {
      return s;
    }
  }
</script>

<div class="space-y-6 max-w-6xl">
  <PageHeader title={t($locale, 'webhooks.title')} subtitle={t($locale, 'webhooks.subtitle')}>
    <svelte:fragment slot="stats">
      <StatChip label={t($locale, 'webhooks.stat.enabled')} value={String(stats.enabled)} tone="success" />
      <StatChip
        label={t($locale, 'webhooks.stat.failing')}
        value={String(stats.failing)}
        tone={stats.failing > 0 ? 'danger' : 'default'}
      />
    </svelte:fragment>
    <svelte:fragment slot="primary">
      <Button on:click={openCreate}>
        <Plus size={14} strokeWidth={1.75} />
        <span>{t($locale, 'webhooks.new')}</span>
      </Button>
    </svelte:fragment>
  </PageHeader>

  <Card>
    {#if loading}
      <p class="muted">{t($locale, 'common.loading')}</p>
    {:else if hooks.length === 0}
      <EmptyState
        illustration="audit"
        title={t($locale, 'empty.webhooks.title')}
        body={t($locale, 'empty.webhooks.body')}
      >
        <svelte:fragment slot="action">
          <Button on:click={openCreate}>
            <Plus size={14} strokeWidth={1.75} />
            <span>{t($locale, 'webhooks.new')}</span>
          </Button>
        </svelte:fragment>
      </EmptyState>
    {:else}
      <table class="table webhooks-table" aria-label={t($locale, 'webhooks.title')}>
        <thead>
          <tr>
            <th>{t($locale, 'webhooks.name')}</th>
            <th>{t($locale, 'webhooks.url')}</th>
            <th>{t($locale, 'webhooks.events')}</th>
            <th>{t($locale, 'webhooks.lastStatus')}</th>
            <th>{t($locale, 'webhooks.lastAttempt')}</th>
            <th>{t($locale, 'webhooks.enabled')}</th>
            <th><span class="sr-only">{t($locale, 'common.actions')}</span></th>
          </tr>
        </thead>
        <tbody>
          {#each hooks as h (h.id)}
            <tr>
              <td class="wh-name">{h.name}</td>
              <td><code class="wh-url" title={h.url}>{h.url}</code></td>
              <td>
                {#if h.events === '*' || h.events === ''}
                  <span class="wh-event-chip wh-event-all">{t($locale, 'webhooks.events.all')}</span>
                {:else}
                  <span class="wh-events-wrap">
                    {#each h.events.split(',').map((s) => s.trim()).filter(Boolean) as ev (ev)}
                      <span class="wh-event-chip">{ev}</span>
                    {/each}
                  </span>
                {/if}
              </td>
              <td>
                {#if h.last_status === 0}
                  <span class="muted">—</span>
                {:else}
                  <Badge variant={statusVariant(h.last_status)}>{h.last_status}</Badge>
                {/if}
                {#if h.last_error}
                  <p class="wh-err" title={h.last_error}>{h.last_error}</p>
                {/if}
              </td>
              <td class="muted">{fmtDate(h.last_attempt_at)}</td>
              <td>
                <!-- Native `change` events bubble through real DOM but
                     not across Svelte component boundaries without
                     explicit forwarding. Wrapping Switch in a div with
                     on:change catches the bubbling input event so the
                     toggle fires without modifying Switch.svelte. -->
                <div on:change={() => toggleEnabled(h)} role="presentation">
                  <Switch checked={h.enabled} label={t($locale, 'webhooks.enabled')} />
                </div>
              </td>
              <td>
                <div class="flex justify-end gap-2">
                  <Button variant="ghost" on:click={() => openEdit(h)}>
                    <Edit2 size={14} strokeWidth={1.75} />
                    <span class="sr-only">{t($locale, 'webhooks.edit')}</span>
                  </Button>
                  <Button variant="outline" on:click={() => askDelete(h)}>
                    <Trash2 size={14} strokeWidth={1.75} />
                    <span class="sr-only">{t($locale, 'common.delete')}</span>
                  </Button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </Card>
</div>

<!-- ─── Create / Edit dialog ─────────────────────────────────────── -->
<Dialog
  open={editing !== null}
  title={editing && editing.id ? t($locale, 'webhooks.edit.title') : t($locale, 'webhooks.create.title')}
  confirmLabel={t($locale, 'webhooks.save')}
  cancelLabel={t($locale, 'common.cancel')}
  busy={saving}
  on:confirm={saveEditing}
  on:cancel={() => (editing = null)}
>
  {#if editing}
    <div class="space-y-3">
      <Input bind:value={editing.name} label={t($locale, 'webhooks.name')} placeholder={t($locale, 'webhooks.namePlaceholder')} />
      <Input bind:value={editing.url} label={t($locale, 'webhooks.url')} placeholder={t($locale, 'webhooks.urlPlaceholder')} />

      <div>
        <label class="wh-label" for="wh-secret-input">{t($locale, 'webhooks.secret')}</label>
        <div class="wh-secret-row">
          <input
            id="wh-secret-input"
            type="text"
            class="input wh-secret-input"
            bind:value={editing.secret}
            autocomplete="off"
            spellcheck="false"
          />
          <button type="button" class="wh-secret-gen" on:click={generateSecret}>
            <RefreshCw size={14} strokeWidth={1.75} />
            <span>{t($locale, 'webhooks.secret.generate')}</span>
          </button>
        </div>
        <p class="hint">{t($locale, 'webhooks.secret.help')}</p>
      </div>

      <div>
        <p class="wh-label">{t($locale, 'webhooks.events')}</p>
        <label class="wh-checkbox">
          <input type="checkbox" bind:checked={allEvents} />
          <span>{t($locale, 'webhooks.events.all')}</span>
        </label>
        {#if !allEvents}
          <div class="wh-event-grid">
            {#each EVENT_TYPES as ev (ev)}
              <label class="wh-checkbox">
                <input type="checkbox" bind:checked={selectedEvents[ev]} />
                <code>{ev}</code>
              </label>
            {/each}
          </div>
        {/if}
      </div>

      <label class="wh-checkbox">
        <input type="checkbox" bind:checked={editing.enabled} />
        <span>{t($locale, 'webhooks.enabled')}</span>
      </label>
    </div>
  {/if}
</Dialog>

<!-- ─── Delete confirm ───────────────────────────────────────────── -->
<Dialog
  open={pendingDelete !== null}
  title={t($locale, 'webhooks.delete.confirm.title')}
  confirmLabel={t($locale, 'common.delete')}
  cancelLabel={t($locale, 'common.cancel')}
  busy={deleting}
  on:confirm={confirmDelete}
  on:cancel={() => (pendingDelete = null)}
>
  <p>{t($locale, 'webhooks.delete.confirm.body')}</p>
  {#if pendingDelete}
    <p class="hint mt-2">
      <strong>{pendingDelete.name}</strong> → <code>{pendingDelete.url}</code>
    </p>
  {/if}
</Dialog>

<style>
  .muted {
    color: var(--text-muted);
  }
  .hint {
    color: var(--text-tertiary);
    font-size: 12px;
    line-height: 1.45;
    margin-block-start: 4px;
  }
  .webhooks-table tbody tr {
    content-visibility: auto;
    contain-intrinsic-size: auto 56px;
  }
  .wh-name {
    color: var(--text);
    font-weight: 500;
  }
  .wh-url {
    display: inline-block;
    max-inline-size: 280px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    vertical-align: middle;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    color: var(--text);
  }
  .wh-events-wrap {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .wh-event-chip {
    display: inline-block;
    padding: 1px 8px;
    border: 1px solid var(--divider);
    border-radius: 12px;
    background: var(--surface-2);
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 10px;
  }
  .wh-event-all {
    border-color: color-mix(in srgb, var(--secondary) 30%, transparent);
    background: color-mix(in srgb, var(--secondary) 12%, transparent);
    color: var(--secondary);
  }
  :global([data-theme='light']) .wh-event-all {
    border-color: color-mix(in srgb, var(--primary) 30%, transparent);
    background: color-mix(in srgb, var(--primary) 10%, transparent);
    color: var(--primary);
  }
  .wh-err {
    color: var(--danger);
    font-size: 11px;
    margin-block-start: 4px;
    max-inline-size: 280px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Edit dialog */
  .wh-label {
    display: block;
    color: var(--text);
    font-size: 13px;
    font-weight: 500;
    margin-block-end: 4px;
  }
  .wh-secret-row {
    display: flex;
    gap: 6px;
  }
  .wh-secret-input {
    flex: 1;
    min-inline-size: 0;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
  }
  .wh-secret-gen {
    /* Interactive button → 12dp per §7 rule 10. */
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 6px 12px;
    border: 1px solid var(--border-strong);
    border-radius: 12px;
    background: var(--surface);
    color: var(--text);
    font-size: 12px;
    cursor: pointer;
  }
  .wh-secret-gen:hover {
    border-color: var(--secondary);
    color: var(--secondary);
  }
  :global([data-theme='light']) .wh-secret-gen:hover {
    border-color: var(--primary);
    color: var(--primary);
  }
  .wh-event-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px;
    margin-block-start: 6px;
  }
  .wh-checkbox {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--text);
    cursor: pointer;
  }
  .wh-checkbox input {
    accent-color: var(--primary);
  }
</style>
