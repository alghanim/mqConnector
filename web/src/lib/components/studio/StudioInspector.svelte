<!--
  StudioInspector — the right panel for the Pipeline Studio.

  Three branches based on `selectionKind`:

    1. Nothing selected            → EmptyState ("Select a node to inspect")
    2. Source / destination node   → read-only Card with the connection
                                     details (name + type + brokers/queue)
    3. Stage node                  → Card with stage type + order, an
                                     enabled toggle, a per-type editor
                                     (Task 10), and a Delete button.

  Per-stage editors (Task 10) live in ./editors and are picked by
  stage_type. Each editor exposes a `bind:config` (the stage_config
  JSON string) + `bind:valid` (true when the editor's structured fields
  produce an acceptable JSON config). The inspector mirrors edits back
  into the studio store via `studio.patchStage(stageId, {stage_config})`
  on every change.

  Transform + Route editors are pipeline-scoped (they wrap the existing
  TransformListEditor / RoutingRuleListEditor and bind the pipeline-
  wide `transforms` / `routingRules` lists). Edits to those lists
  patch the studio draft via direct mutation through the wrapped
  list editor's existing bind contract; the inspector watches for
  reference changes and propagates them to the store as a fresh draft
  with `studio.updateDraft` semantics — we do this inline rather than
  adding a new store helper because Task 12 will need similar
  mutations and we don't want to land an API we'll have to rewrite.
-->
<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import { studio, type StudioStateData } from '$lib/stores/studio';
  import {
    api,
    type Connection,
    type Schema,
    type Transform,
    type RoutingRule
  } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import FilterEditor from './editors/FilterEditor.svelte';
  import TransformEditor from './editors/TransformEditor.svelte';
  import TranslateEditor from './editors/TranslateEditor.svelte';
  import RouteEditor from './editors/RouteEditor.svelte';
  import ScriptEditor from './editors/ScriptEditor.svelte';
  import ValidateEditor from './editors/ValidateEditor.svelte';
  import WasmEditor from './editors/WasmEditor.svelte';

  let s: StudioStateData;
  const unsub = studio.subscribe((v) => (s = v));
  onDestroy(unsub);

  // Connections cache — fetched once, used for the source/dest card and
  // for the RouteEditor's destination dropdown. Schemas similarly. We
  // fetch lazily and tolerate failure (empty list) so a missing endpoint
  // doesn't break the inspector entirely.
  let connections: Connection[] = [];
  let schemas: Schema[] = [];
  void (async () => {
    try {
      connections = (await api.get<Connection[]>('/v1/connections')) ?? [];
    } catch {
      connections = [];
    }
  })();
  void (async () => {
    try {
      schemas = (await api.get<Schema[]>('/v1/schemas')) ?? [];
    } catch {
      schemas = [];
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

  // Local mirrors that the per-type editors bind to. We keep an editor-
  // owned copy so the editor's 2-way binding doesn't fight the store
  // (each editor calls JSON.stringify on every keystroke; if we bound
  // directly to s.draft.stages[i].stage_config the store would emit
  // a fresh snapshot for every edit, re-rendering the editor and
  // discarding caret position). When the local mirror diverges from
  // the store we push the change via studio.patchStage.
  let stageConfig = '';
  let stageValid = true;
  let stageId = '';
  // Track of the last serialised value so we don't echo store-driven
  // updates back at the store (would infinite-loop).
  let lastStoreConfig = '';

  // Re-sync the local mirror whenever the selected stage changes or
  // the underlying stage_config rotates in from the store (e.g. on
  // hydrate / on discard draft).
  $: if (selectedStage && (selectedStage.id ?? '') !== stageId) {
    stageId = selectedStage.id ?? '';
    stageConfig = selectedStage.stage_config ?? '{}';
    lastStoreConfig = stageConfig;
    stageValid = true;
  } else if (selectedStage && selectedStage.stage_config !== lastStoreConfig) {
    stageConfig = selectedStage.stage_config ?? '{}';
    lastStoreConfig = stageConfig;
  }

  // Push local edits back to the store. The check against
  // lastStoreConfig prevents the reactive cycle described above.
  $: if (selectedStage && stageConfig !== lastStoreConfig) {
    const sid = selectedStage.id ?? '';
    lastStoreConfig = stageConfig;
    // queueMicrotask to escape the current reactive tick — Svelte's
    // batched updates can otherwise re-fire $: on the same statement.
    queueMicrotask(() => studio.patchStage(sid, { stage_config: stageConfig }));
  }

  // Pipeline-scoped lists for TransformEditor + RouteEditor. We mirror
  // them locally so the wrapped editors can use bind:transforms /
  // bind:rules without touching the store directly. When the bound
  // arrays change (reference or contents), we push back through a
  // local snapshot mutation + markDirty(). The draft.transforms /
  // draft.routingRules fields are intentionally plain arrays so a
  // shallow .slice() round-trip is cheap.
  let localTransforms: Transform[] = [];
  let localRules: RoutingRule[] = [];
  // Snapshot the store-side lists into the local mirrors any time the
  // store rotates (hydrate / discard / deploy). Comparing references
  // avoids clobbering a local in-progress edit.
  let lastTransformsRef: Transform[] | null = null;
  let lastRulesRef: RoutingRule[] | null = null;
  $: if (s?.draft && s.draft.transforms !== lastTransformsRef) {
    lastTransformsRef = s.draft.transforms;
    localTransforms = s.draft.transforms.slice();
  }
  $: if (s?.draft && s.draft.routingRules !== lastRulesRef) {
    lastRulesRef = s.draft.routingRules;
    localRules = s.draft.routingRules.slice();
  }
  // Push local mutations back into the store. The wrapped list editors
  // do .slice() / spread on every mutation, so the array reference
  // changes — we re-pin lastTransformsRef immediately to avoid the
  // store-back-to-local cycle.
  $: if (
    s?.draft &&
    localTransforms !== lastTransformsRef &&
    localTransforms !== s.draft.transforms
  ) {
    const cur = studio.snapshot();
    if (cur.draft) {
      cur.draft.transforms = localTransforms;
      lastTransformsRef = localTransforms;
      queueMicrotask(() => studio.markDirty());
    }
  }
  $: if (
    s?.draft &&
    localRules !== lastRulesRef &&
    localRules !== s.draft.routingRules
  ) {
    const cur = studio.snapshot();
    if (cur.draft) {
      cur.draft.routingRules = localRules;
      lastRulesRef = localRules;
      queueMicrotask(() => studio.markDirty());
    }
  }

  function onEnableToggle() {
    if (!selectedStage) return;
    studio.patchStage(selectedStage.id ?? '', { enabled: !selectedStage.enabled });
  }

  function onDelete() {
    if (!selectedStage) return;
    studio.removeStage(selectedStage.id ?? '');
  }

  // Per-stage editor "test" events bubble up here — wired into Task 11's
  // DryRunDock by the parent route. For Task 10 we just forward them up
  // through a DOM CustomEvent on the aside; the route can listen via
  // `<StudioInspector on:test={…}>` once the dock lands. To make that
  // work without prop-passing a handler down, we re-emit via tick.
  let asideEl: HTMLElement | null = null;
  async function onEditorTest(e: CustomEvent<unknown>) {
    await tick();
    asideEl?.dispatchEvent(
      new CustomEvent('test', { detail: { stage: selectedStage, payload: e.detail }, bubbles: true })
    );
  }

  function brokersLine(c: Connection): string {
    return c.brokers || c.conn_name || c.url || '';
  }
  function queueLine(c: Connection): string {
    return c.queue_name || c.topic || c.stream_name || '';
  }
</script>

<aside class="studio-inspector" aria-label="Inspector" bind:this={asideEl}>
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
      <div class="studio-inspector-valid">
        {#if stageValid}
          <span class="studio-inspector-valid-ok" aria-label={t($locale, 'studio.editor.valid')}
            >✓ {t($locale, 'studio.editor.valid')}</span
          >
        {:else}
          <span
            class="studio-inspector-valid-bad"
            aria-label={t($locale, 'studio.editor.invalid')}
          >! {t($locale, 'studio.editor.invalid')}</span>
        {/if}
      </div>

      {#if selectedStage.stage_type === 'filter'}
        <FilterEditor bind:config={stageConfig} bind:valid={stageValid} />
      {:else if selectedStage.stage_type === 'transform'}
        <TransformEditor
          bind:config={stageConfig}
          bind:valid={stageValid}
          bind:transforms={localTransforms}
        />
      {:else if selectedStage.stage_type === 'translate'}
        <TranslateEditor bind:config={stageConfig} bind:valid={stageValid} />
      {:else if selectedStage.stage_type === 'route'}
        <RouteEditor
          bind:config={stageConfig}
          bind:valid={stageValid}
          bind:rules={localRules}
          {connections}
        />
      {:else if selectedStage.stage_type === 'script'}
        <ScriptEditor
          bind:config={stageConfig}
          bind:valid={stageValid}
          on:test={onEditorTest}
        />
      {:else if selectedStage.stage_type === 'validate'}
        <ValidateEditor
          bind:config={stageConfig}
          bind:valid={stageValid}
          {schemas}
          on:test={onEditorTest}
        />
      {:else if (selectedStage.stage_type as string) === 'wasm'}
        <WasmEditor bind:config={stageConfig} bind:valid={stageValid} />
      {:else}
        <p class="studio-inspector-placeholder">
          {t($locale, 'studio.inspector.stage.configPlaceholder')}
        </p>
      {/if}
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
  .studio-inspector-valid {
    display: flex;
    justify-content: flex-end;
    margin-block-end: 0.5rem;
    font-size: 0.75rem;
    font-weight: 600;
  }
  .studio-inspector-valid-ok {
    color: var(--success);
  }
  .studio-inspector-valid-bad {
    color: var(--danger);
  }
</style>
