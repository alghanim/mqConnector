<!--
  StudioInspector — the right panel for the Pipeline Studio.

  Task 9 / Wave 1 — skeleton only. Three branches:

    1. Nothing selected            → EmptyState ("Select a node to inspect")
    2. Source / destination node   → read-only Card with the connection
                                     details (name + type + brokers/queue)
    3. Stage node                  → Card with stage type + order, an
                                     enabled toggle, a placeholder card
                                     where Task 10 will render the per-
                                     stage editor, and a Delete button.

  The inspector reads `selectedNodeId` + `draft` from the studio store
  and mutates the draft via `studio.patchStage` / `studio.removeStage`.
  It does not own state.
-->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { studio, type StudioStateData } from '$lib/stores/studio';
  import { api, type Connection } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';

  let s: StudioStateData;
  const unsub = studio.subscribe((v) => (s = v));
  onDestroy(unsub);

  // Connections cache so we can show a name + type for the source /
  // destination card. We fetch lazily (the studio store doesn't carry
  // this) and never throw — a fetch failure shows the raw id instead.
  let connections: Connection[] = [];
  void (async () => {
    try {
      connections = (await api.get<Connection[]>('/v1/connections')) ?? [];
    } catch {
      connections = [];
    }
  })();

  // Selection classification. selectedNodeId is one of:
  //   source-<connId>          (the source connection node)
  //   dest-<connId>            (the destination connection node)
  //   route-dest-<connId>      (an alternate route destination)
  //   <stage id, e.g. tmp-…>   (a stage node)
  $: selectionKind = (() => {
    const id = s?.selectedNodeId;
    if (!id) return 'none';
    if (id.startsWith('source-')) return 'source';
    if (id.startsWith('dest-')) return 'destination';
    if (id.startsWith('route-dest-')) return 'destination';
    // Otherwise it's a stage id — confirm by looking it up in draft.stages.
    const inStages = s?.draft?.stages.some((st) => st.id === id);
    return inStages ? 'stage' : 'none';
  })();

  $: selectedConnection = (() => {
    if (selectionKind !== 'source' && selectionKind !== 'destination') return null;
    const id = s.selectedNodeId ?? '';
    const connId = id
      .replace(/^source-/, '')
      .replace(/^route-dest-/, '')
      .replace(/^dest-/, '');
    return connections.find((c) => c.id === connId) ?? { id: connId, name: connId, type: 'ibm' as const };
  })();

  $: selectedStage = (() => {
    if (selectionKind !== 'stage') return null;
    return s.draft?.stages.find((st) => st.id === s.selectedNodeId) ?? null;
  })();

  function onEnableToggle() {
    if (!selectedStage) return;
    studio.patchStage(selectedStage.id ?? '', { enabled: !selectedStage.enabled });
  }

  function onDelete() {
    if (!selectedStage) return;
    studio.removeStage(selectedStage.id ?? '');
  }

  // Brokers/queue can come from a number of fields depending on the
  // connection type. We surface the first non-empty one so the
  // inspector card stays compact.
  function brokersLine(c: Connection): string {
    return c.brokers || c.conn_name || c.url || '';
  }
  function queueLine(c: Connection): string {
    return c.queue_name || c.topic || c.stream_name || '';
  }
</script>

<aside class="studio-inspector" aria-label="Inspector">
  {#if selectionKind === 'none'}
    <EmptyState
      illustration="metrics"
      title={t($locale, 'studio.inspector.empty.title')}
      body={t($locale, 'studio.inspector.empty.body')}
    />
  {:else if selectedConnection}
    <Card padding="md">
      <header class="studio-inspector-head">
        <h3 class="studio-inspector-h">{t($locale, 'studio.inspector.connection.heading')}</h3>
        <p class="studio-inspector-sub">{selectedConnection.name}</p>
      </header>
      <dl class="studio-inspector-meta">
        <dt>{t($locale, 'studio.inspector.connection.type')}</dt>
        <dd>{selectedConnection.type}</dd>
        {#if brokersLine(selectedConnection)}
          <dt>{t($locale, 'studio.inspector.connection.brokers')}</dt>
          <dd class="studio-inspector-mono">{brokersLine(selectedConnection)}</dd>
        {/if}
        {#if queueLine(selectedConnection)}
          <dt>{t($locale, 'studio.inspector.connection.queue')}</dt>
          <dd class="studio-inspector-mono">{queueLine(selectedConnection)}</dd>
        {/if}
      </dl>
    </Card>
  {:else if selectedStage}
    <Card padding="md">
      <header class="studio-inspector-head">
        <h3 class="studio-inspector-h">{t($locale, 'studio.inspector.stage.heading')}</h3>
        <p class="studio-inspector-sub">{selectedStage.stage_type}</p>
      </header>
      <dl class="studio-inspector-meta">
        <dt>{t($locale, 'studio.inspector.stage.order')}</dt>
        <dd>{selectedStage.stage_order}</dd>
        <dt>{t($locale, 'studio.inspector.stage.enabled')}</dt>
        <dd>
          <Switch
            checked={selectedStage.enabled}
            label={selectedStage.enabled
              ? t($locale, 'common.enabled')
              : t($locale, 'common.disabled')}
            on:change={onEnableToggle}
          />
        </dd>
      </dl>
    </Card>

    <Card padding="md">
      <p class="studio-inspector-placeholder">
        {t($locale, 'studio.inspector.stage.configPlaceholder')}
      </p>
    </Card>

    <div class="studio-inspector-actions">
      <Button variant="outline" on:click={onDelete}>
        {t($locale, 'studio.inspector.stage.delete')}
      </Button>
    </div>
  {/if}
</aside>

<style>
  .studio-inspector {
    display: flex;
    flex-direction: column;
    gap: 0.625rem;
    block-size: 100%;
    overflow-y: auto;
  }
  .studio-inspector-head {
    margin-block-end: 0.5rem;
    padding-block-end: 0.5rem;
    border-block-end: 1px solid var(--border);
  }
  .studio-inspector-h {
    margin: 0;
    font-size: 0.6875rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-tertiary);
  }
  .studio-inspector-sub {
    margin: 0;
    margin-block-start: 0.125rem;
    font-size: 0.9375rem;
    font-weight: 600;
    color: var(--text);
    text-transform: capitalize;
  }
  .studio-inspector-meta {
    margin: 0;
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.5rem 0.875rem;
    align-items: center;
  }
  .studio-inspector-meta dt {
    color: var(--text-muted);
    font-size: 0.75rem;
    font-weight: 600;
  }
  .studio-inspector-meta dd {
    margin: 0;
    color: var(--text);
    font-size: 0.8125rem;
  }
  .studio-inspector-mono {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.75rem;
    word-break: break-all;
  }
  .studio-inspector-placeholder {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.8125rem;
    font-style: italic;
    text-align: center;
  }
  .studio-inspector-actions {
    display: flex;
    justify-content: flex-end;
    padding-block-start: 0.25rem;
  }
</style>
