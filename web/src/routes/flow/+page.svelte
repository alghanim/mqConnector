<!--
  /flow — visual pipeline builder.

  Mental model:
    - One canvas, one pipeline (matches the 2024 prototype shape).
    - Exactly one Source node + one or more Destination nodes are required.
    - Everything between source and destination is the linear stage chain.
    - Connections are AMQ-style "out → in" only; no fan-in to a single node.

  Save & Deploy is the real thing: it POSTs a new pipeline + PUTs the stages
  + transforms + routing rules + POSTs /reload. The 2024 version
  serialised to localStorage and never deployed.

  Routing rules deserve a note: a `route` stage's destinations are
  represented in the canvas as outgoing edges to multiple Destination
  nodes, but the storage model keeps routing rules as a flat list. For
  simplicity, we configure each rule on the route node's property panel
  and tie its target to whichever Destination node the operator drags.
-->
<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { api, type Connection, type Schema, type StageType } from '$lib/api';
  import { topoFromSource as topoFromSourceCore } from '$lib/flow-graph';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import StageConfigForm from '$lib/components/StageConfigForm.svelte';
  import { SAMPLE_FIXTURES } from '$lib/sample-fixtures';

  // ─── Types ────────────────────────────────────────────────────────
  type NodeKind =
    | 'source'
    | 'destination'
    | 'filter'
    | 'transform'
    | 'translate'
    | 'route'
    | 'script'
    | 'validate';

  interface FlowNode {
    id: string;
    kind: NodeKind;
    x: number;
    y: number;
    label: string;
    /**
     * For source/destination: the storage Connection id. Always a string
     * (possibly empty) so two-way binding stays type-stable.
     */
    connection_id: string;
    /** Raw JSON config for stage-type nodes. */
    config: string;
  }
  interface FlowEdge {
    from: string;
    to: string;
  }

  // ─── State ────────────────────────────────────────────────────────
  let pipelineName = '';
  let nodes: FlowNode[] = [];
  let edges: FlowEdge[] = [];
  let selectedId: string | null = null;
  let error = '';
  let savedMsg = '';
  let saving = false;
  let connections: Connection[] = [];

  let canvasEl: HTMLElement | null = null;
  let svgEl: SVGSVGElement | null = null;

  let dragging: { id: string; offsetX: number; offsetY: number } | null = null;
  let connecting: { fromId: string; cursorX: number; cursorY: number } | null = null;
  let nextNodeIdCounter = 0;

  // Per Brand Guide §5: the palette is closed. Stage tones come from
  // CSS custom properties (set in brand-tokens.css) so both themes stay
  // on-brand. Source/Destination carry the only "warm" emphasis (gold +
  // copper); the six stage types use the slate + muted-neutral scale so
  // they read as a single visual class distinguished by glyph instead
  // of by colour saturation.
  const palette: { kind: NodeKind; label: string; tone: string }[] = [
    { kind: 'source',      label: 'S · source',      tone: 'var(--primary)'       },
    { kind: 'destination', label: 'D · destination', tone: 'var(--secondary)'     },
    { kind: 'filter',      label: 'F · filter',      tone: 'var(--qb-slate-130)'  },
    { kind: 'transform',   label: 'T · transform',   tone: 'var(--qb-olive-gold)' },
    { kind: 'translate',   label: 'X · translate',   tone: 'var(--qb-sand)'       },
    { kind: 'route',       label: 'R · route',       tone: 'var(--qb-slate-120)'  },
    { kind: 'script',      label: 'JS · script',     tone: 'var(--qb-slate-110)'  },
    { kind: 'validate',    label: 'V · validate',    tone: 'var(--qb-warm-gray)'  }
  ];

  $: selected = nodes.find((n) => n.id === selectedId) ?? null;
  $: connOptions = connections.map((c) => ({
    value: c.id || '',
    label: `${c.name} (${c.type})`
  }));

  function newNodeId() {
    nextNodeIdCounter++;
    return 'n' + nextNodeIdCounter;
  }
  function toneFor(kind: NodeKind): string {
    return palette.find((p) => p.kind === kind)?.tone ?? '#888';
  }
  function defaultConfig(kind: NodeKind): string {
    switch (kind) {
      case 'filter':
        return '{"paths":[]}';
      case 'translate':
        return '{"output_format":"xml"}';
      case 'script':
        return '{"script":"// msg.foo = 1\\nmsg;"}';
      case 'validate':
        return '{"schema_id":""}';
      case 'route':
        return '{}';
      default:
        return '{}';
    }
  }

  // ─── Load saved connections + schemas (schemas feed the validate-stage
  //     picker inside StageConfigForm).
  let schemas: Schema[] = [];
  async function loadBackground() {
    try {
      const [conns, sc] = await Promise.all([
        api.get<Connection[]>('/v1/connections').then((v) => v ?? []),
        api.get<Schema[]>('/v1/schemas').then((v) => v ?? [])
      ]);
      connections = conns;
      schemas = sc;
    } catch {
      connections = [];
      schemas = [];
    }
  }
  onMount(loadBackground);

  // ─── Sample → filter (the core app workflow) ────────────────────
  // Upload or paste a representative MQ message, see the paths inside
  // it, click which ones to strip. A clicked path lands in the first
  // filter node's config — created next to the source if there isn't
  // one yet, wired in between source and the first non-source neighbour.
  let flowSample = '';
  let flowExtractedPaths: string[] = [];
  let flowExtractError = '';

  async function onFlowSampleFile(e: Event) {
    const target = e.target as HTMLInputElement;
    const file = target.files?.[0];
    if (!file) return;
    flowSample = await file.text();
    await extractFlowPaths();
    target.value = '';
  }
  async function loadFlowSample(body: string) {
    flowSample = body;
    await extractFlowPaths();
  }
  async function extractFlowPaths() {
    flowExtractError = '';
    if (!flowSample.trim()) {
      flowExtractedPaths = [];
      return;
    }
    try {
      const r = await api.postRaw<{ format: string; paths: string[] }>(
        '/v1/samples/extract',
        flowSample,
        'application/octet-stream'
      );
      flowExtractedPaths = r.paths || [];
    } catch (e: unknown) {
      flowExtractedPaths = [];
      flowExtractError = (e as { message?: string }).message || 'extract failed';
    }
  }

  function findOrCreateFilterNode(): FlowNode {
    const existing = nodes.find((n) => n.kind === 'filter');
    if (existing) return existing;
    // Position next to the source if there's exactly one.
    const source = nodes.find((n) => n.kind === 'source');
    const node: FlowNode = {
      id: newNodeId(),
      kind: 'filter',
      x: source ? source.x + 220 : 200,
      y: source ? source.y : 120,
      label: 'filter',
      connection_id: '',
      config: '{"paths":[]}'
    };
    nodes = [...nodes, node];
    if (source) {
      // If the source has an outgoing edge already, splice the new filter
      // in between (source → filter → previous_target).
      const out = edges.find((e) => e.from === source.id);
      if (out) {
        edges = [
          ...edges.filter((e) => !(e.from === source.id && e.to === out.to)),
          { from: source.id, to: node.id },
          { from: node.id, to: out.to }
        ];
      } else {
        edges = [...edges, { from: source.id, to: node.id }];
      }
    }
    return node;
  }

  function readNodePaths(n: FlowNode): string[] {
    try {
      const v = JSON.parse(n.config || '{}');
      return Array.isArray(v.paths)
        ? v.paths.filter((p: unknown): p is string => typeof p === 'string')
        : [];
    } catch {
      return [];
    }
  }
  function writeNodePaths(n: FlowNode, paths: string[]) {
    let v: Record<string, unknown> = {};
    try {
      const parsed = JSON.parse(n.config || '{}');
      if (parsed && typeof parsed === 'object') v = parsed;
    } catch {
      // start fresh
    }
    v.paths = paths;
    n.config = JSON.stringify(v);
    nodes = [...nodes];
  }
  function addPathToFlowFilter(p: string) {
    const n = findOrCreateFilterNode();
    const cur = readNodePaths(n);
    if (cur.includes(p)) return;
    writeNodePaths(n, [...cur, p]);
    selectedId = n.id;
  }
  function addAllPathsToFlowFilter() {
    if (flowExtractedPaths.length === 0) return;
    const n = findOrCreateFilterNode();
    const cur = readNodePaths(n);
    const merged = [...cur];
    for (const p of flowExtractedPaths) if (!merged.includes(p)) merged.push(p);
    writeNodePaths(n, merged);
    selectedId = n.id;
  }
  $: flowFilterPathSet = (() => {
    const n = nodes.find((x) => x.kind === 'filter');
    return n ? new Set(readNodePaths(n)) : new Set<string>();
  })();

  // ─── Palette drag → drop ─────────────────────────────────────────
  function onPaletteDragStart(e: DragEvent, kind: NodeKind) {
    e.dataTransfer?.setData('mqc/node-kind', kind);
  }
  function onCanvasDragOver(e: DragEvent) {
    e.preventDefault();
  }
  function onCanvasDrop(e: DragEvent) {
    e.preventDefault();
    const kind = e.dataTransfer?.getData('mqc/node-kind') as NodeKind | null;
    if (!kind || !canvasEl) return;
    const rect = canvasEl.getBoundingClientRect();
    const node: FlowNode = {
      id: newNodeId(),
      kind,
      x: Math.max(8, e.clientX - rect.left - 90),
      y: Math.max(8, e.clientY - rect.top - 24),
      label: kind,
      connection_id: '',
      config: defaultConfig(kind)
    };
    nodes = [...nodes, node];
    selectedId = node.id;
  }

  // ─── Node drag inside canvas ─────────────────────────────────────
  function onNodeMouseDown(e: MouseEvent, node: FlowNode) {
    // Allow clicks on inputs/buttons inside the node header to behave normally
    if ((e.target as HTMLElement).closest('.port, .delete-btn, input, button, select, textarea, a')) {
      return;
    }
    e.preventDefault();
    selectedId = node.id;
    const rect = canvasEl!.getBoundingClientRect();
    dragging = {
      id: node.id,
      offsetX: e.clientX - rect.left - node.x,
      offsetY: e.clientY - rect.top - node.y
    };
  }

  function onCanvasMouseMove(e: MouseEvent) {
    const rect = canvasEl?.getBoundingClientRect();
    if (!rect) return;
    if (dragging) {
      const id = dragging.id;
      const x = Math.max(0, e.clientX - rect.left - dragging.offsetX);
      const y = Math.max(0, e.clientY - rect.top - dragging.offsetY);
      nodes = nodes.map((n) => (n.id === id ? { ...n, x, y } : n));
    } else if (connecting) {
      connecting = {
        ...connecting,
        cursorX: e.clientX - rect.left,
        cursorY: e.clientY - rect.top
      };
    }
  }

  function onCanvasMouseUp() {
    dragging = null;
    // If the user dropped a connection outside any port, just cancel.
    connecting = null;
  }

  // ─── Port → port connect ────────────────────────────────────────
  function onPortOutMouseDown(e: MouseEvent, node: FlowNode) {
    if (node.kind === 'destination') return;
    e.preventDefault();
    e.stopPropagation();
    const rect = canvasEl!.getBoundingClientRect();
    connecting = {
      fromId: node.id,
      cursorX: e.clientX - rect.left,
      cursorY: e.clientY - rect.top
    };
  }
  function onPortInMouseUp(e: MouseEvent, node: FlowNode) {
    if (!connecting) return;
    if (node.kind === 'source') return;
    if (connecting.fromId === node.id) return;
    e.stopPropagation();
    addEdge(connecting.fromId, node.id);
    connecting = null;
  }
  function addEdge(from: string, to: string) {
    if (edges.some((e) => e.from === from && e.to === to)) return;
    edges = [...edges, { from, to }];
  }

  function deleteNode(id: string) {
    nodes = nodes.filter((n) => n.id !== id);
    edges = edges.filter((e) => e.from !== id && e.to !== id);
    if (selectedId === id) selectedId = null;
  }

  function clearAll() {
    if (!confirm('Clear the canvas?')) return;
    nodes = [];
    edges = [];
    selectedId = null;
    error = '';
    savedMsg = '';
  }

  // ─── Edge path helpers (drawn as SVG cubic Béziers) ──────────────
  const NODE_W = 180;
  const NODE_H = 56;
  function portOut(node: FlowNode) {
    return { x: node.x + NODE_W, y: node.y + NODE_H / 2 };
  }
  function portIn(node: FlowNode) {
    return { x: node.x, y: node.y + NODE_H / 2 };
  }
  function edgePath(from: FlowNode, to: FlowNode) {
    const a = portOut(from);
    const b = portIn(to);
    const cx = (a.x + b.x) / 2;
    return `M${a.x},${a.y} C${cx},${a.y} ${cx},${b.y} ${b.x},${b.y}`;
  }
  function dragPath(from: FlowNode, cx: number, cy: number) {
    const a = portOut(from);
    const mx = (a.x + cx) / 2;
    return `M${a.x},${a.y} C${mx},${a.y} ${mx},${cy} ${cx},${cy}`;
  }

  // ─── Save & Deploy ───────────────────────────────────────────────
  async function saveAndDeploy() {
    error = '';
    savedMsg = '';
    saving = true;
    try {
      const sources = nodes.filter((n) => n.kind === 'source');
      const destinations = nodes.filter((n) => n.kind === 'destination');
      if (sources.length !== 1) throw new Error(t($locale, 'flow.error.needSource'));
      if (destinations.length < 1) throw new Error(t($locale, 'flow.error.needDest'));
      const source = sources[0];
      if (!source.connection_id) throw new Error(t($locale, 'flow.error.missingConn'));
      for (const d of destinations) {
        if (!d.connection_id) throw new Error(t($locale, 'flow.error.missingConn'));
      }

      // Topological order from the source. Detect cycles. Ensure each
      // destination is reachable.
      const order = topoFromSource(source.id);
      const reachable = new Set(order);
      for (const d of destinations) {
        if (!reachable.has(d.id)) throw new Error(t($locale, 'flow.error.notConnected'));
      }

      // Pull the stage chain — everything between source (exclusive) and
      // the first destination encountered (exclusive).
      const stageNodes: FlowNode[] = [];
      for (const id of order) {
        const n = byId(id);
        if (!n) continue;
        if (n.kind === 'source') continue;
        if (n.kind === 'destination') break;
        if (n.kind === 'route') {
          // route stages live in the chain too — their per-rule
          // destinations are derived from outgoing edges
          stageNodes.push(n);
          continue;
        }
        stageNodes.push(n);
      }

      // Validate each stage's config is JSON before sending.
      for (const sNode of stageNodes) {
        try {
          JSON.parse(sNode.config || '{}');
        } catch {
          throw new Error(`${sNode.kind} (${sNode.label}): config is not valid JSON`);
        }
      }

      // 1. Create the pipeline.
      const defaultDest = destinations[0];
      const pipe = await api.post<{ id: string }>('/v1/pipelines', {
        name: pipelineName || 'flow-' + Date.now(),
        source_id: source.connection_id,
        destination_id: defaultDest.connection_id,
        output_format: 'same',
        filter_paths: [],
        enabled: true
      });

      // 2. Stages.
      const stages = stageNodes
        .filter((n) => n.kind !== 'destination')
        .map((n, i) => ({
          stage_order: i + 1,
          stage_type: n.kind === 'route' ? 'route' : n.kind,
          stage_config: n.config || '{}',
          enabled: true
        }));
      await api.put(`/v1/pipelines/${pipe.id}/stages`, stages);

      // 3. Routing rules — only if there's a route node. The rule list
      // comes from each outgoing edge of the route node to a destination
      // node; the rule predicate sits on each destination node's
      // properties (condition_path, operator, value, priority).
      const routeNode = stageNodes.find((n) => n.kind === 'route');
      if (routeNode) {
        const outEdges = edges.filter((e) => e.from === routeNode.id);
        const rules = outEdges
          .map((e, i) => {
            const target = byId(e.to);
            if (!target || target.kind !== 'destination') return null;
            const cfg = safeParse(target.config) as Record<string, unknown>;
            return {
              condition_path: String(cfg.condition_path || ''),
              condition_operator: String(cfg.condition_operator || 'eq'),
              condition_value: String(cfg.condition_value || ''),
              destination_id: target.connection_id || '',
              priority: Number(cfg.priority) || i + 1,
              enabled: true
            };
          })
          .filter(Boolean);
        await api.put(`/v1/pipelines/${pipe.id}/routing-rules`, rules);
      } else {
        // No route node — wipe any stale rules.
        await api.put(`/v1/pipelines/${pipe.id}/routing-rules`, []);
      }

      // 4. Hot-reload so workers pick the new pipeline up immediately.
      await api.post('/v1/reload');

      savedMsg = t($locale, 'flow.saved');
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'save failed';
    } finally {
      saving = false;
      // Refresh the canvas hint area without losing nodes.
      await tick();
    }
  }

  function byId(id: string): FlowNode | undefined {
    return nodes.find((n) => n.id === id);
  }
  function safeParse(s: string): unknown {
    try {
      return JSON.parse(s || '{}');
    } catch {
      return {};
    }
  }

  // Delegate to the shared module so the live behaviour is pinned by
  // the unit tests in flow-graph.test.ts.
  function topoFromSource(sourceId: string): string[] {
    try {
      return topoFromSourceCore(
        nodes.map((n) => ({ id: n.id, kind: n.kind })),
        edges,
        sourceId
      );
    } catch (e) {
      // Surface the module's English error wrapped in the locale string
      // the editor already uses so RTL/Arabic users see translated copy.
      throw new Error(t($locale, 'flow.error.cycleDetected'));
    }
  }
</script>

<svelte:window on:mousemove={onCanvasMouseMove} on:mouseup={onCanvasMouseUp} />

<div class="flow-shell">
  <!-- ─── Palette + canvas + props ──────────────────────────────────── -->
  <aside class="palette">
    <p class="palette-heading">{t($locale, 'flow.palette.stages')}</p>
    {#each palette as p}
      <div
        class="palette-item"
        role="button"
        tabindex="0"
        draggable="true"
        on:dragstart={(e) => onPaletteDragStart(e, p.kind)}
        style:--tone={p.tone}
      >
        <span class="dot" style="background:{p.tone}"></span>
        {p.label}
      </div>
    {/each}

    <!-- ─── Sample → filter (the core workflow) ──────────────────────── -->
    <p class="palette-heading mt-4">{t($locale, 'preview.sample')}</p>
    <p class="palette-help">{t($locale, 'flow.sample.help')}</p>
    <input
      type="file"
      accept=".json,.xml,.txt"
      on:change={onFlowSampleFile}
      class="palette-file"
    />
    <div class="try-row">
      <span class="try-label">{t($locale, 'preview.try')}</span>
      {#each SAMPLE_FIXTURES as f}
        <button type="button" class="try-btn" on:click={() => loadFlowSample(f.body)}>
          {f.label}
        </button>
      {/each}
    </div>
    <textarea
      class="palette-sample"
      rows="4"
      bind:value={flowSample}
      on:blur={extractFlowPaths}
      placeholder={'{"id":"order-1","secret":"hush"}'}
    ></textarea>
    {#if flowExtractError}
      <p class="palette-help" style="color: var(--danger)">{flowExtractError}</p>
    {/if}
    {#if flowExtractedPaths.length > 0}
      <div class="palette-paths">
        {#each flowExtractedPaths as p}
          <button
            type="button"
            class="path-chip"
            class:on={flowFilterPathSet.has(p)}
            on:click={() => addPathToFlowFilter(p)}
            title={t($locale, 'preview.paths.chipHint')}
          >
            {flowFilterPathSet.has(p) ? '✓ ' : '+ '}{p}
          </button>
        {/each}
      </div>
      <button type="button" class="palette-use-all" on:click={addAllPathsToFlowFilter}>
        {t($locale, 'preview.paths.useAll')}
      </button>
    {/if}

    <p class="palette-heading mt-4">{t($locale, 'flow.palette.connections')}</p>
    {#if connections.length === 0}
      <p class="palette-help">{t($locale, 'flow.connections.empty')}</p>
    {:else}
      {#each connections as c}
        <button
          type="button"
          class="palette-conn"
          on:click={() => navigator.clipboard?.writeText(c.id || '')}
          title={c.id}
        >
          <span class="dot" style="background: var(--text-muted)"></span>
          {c.name} <span style="color: var(--text-muted)">({c.type})</span>
        </button>
      {/each}
    {/if}
  </aside>

  <section
    class="canvas-wrap"
    bind:this={canvasEl}
    on:dragover={onCanvasDragOver}
    on:drop={onCanvasDrop}
    role="application"
    aria-label="Flow canvas"
  >
    <!-- Edges go in an SVG underneath the nodes. pointer-events:none lets
         drags pass through onto nodes; the SVG only paints. -->
    <svg
      bind:this={svgEl}
      class="edges"
      preserveAspectRatio="xMinYMin"
      role="presentation"
    >
      {#each edges as e (e.from + '→' + e.to)}
        {@const a = byId(e.from)}
        {@const b = byId(e.to)}
        {#if a && b}
          <path d={edgePath(a, b)} class="edge" />
        {/if}
      {/each}
      {#if connecting}
        {@const a = byId(connecting.fromId)}
        {#if a}
          <path d={dragPath(a, connecting.cursorX, connecting.cursorY)} class="edge edge-drag" />
        {/if}
      {/if}
    </svg>

    {#if nodes.length === 0}
      <p class="canvas-empty">{t($locale, 'flow.canvas.empty')}</p>
    {/if}

    {#each nodes as n (n.id)}
      <div
        class="flow-node"
        class:selected={n.id === selectedId}
        style="left: {n.x}px; top: {n.y}px; --tone: {toneFor(n.kind)}"
        on:mousedown={(e) => onNodeMouseDown(e, n)}
        on:click={() => (selectedId = n.id)}
        role="presentation"
      >
        <div class="flow-node-head">
          <span class="dot" style="background: {toneFor(n.kind)}"></span>
          <span class="flow-node-kind">{n.kind}</span>
          {#if n.id === selectedId}
            <button
              type="button"
              class="delete-btn"
              title={t($locale, 'flow.props.delete')}
              on:click|stopPropagation={() => deleteNode(n.id)}
            >×</button>
          {/if}
        </div>
        <div class="flow-node-body">
          {#if n.kind === 'source' || n.kind === 'destination'}
            {connections.find((c) => c.id === n.connection_id)?.name || '— pick connection —'}
          {:else}
            {n.label || n.kind}
          {/if}
        </div>
        {#if n.kind !== 'source'}
          <span
            class="port port-in"
            role="presentation"
            on:mouseup={(e) => onPortInMouseUp(e, n)}
          ></span>
        {/if}
        {#if n.kind !== 'destination'}
          <span
            class="port port-out"
            role="presentation"
            on:mousedown={(e) => onPortOutMouseDown(e, n)}
          ></span>
        {/if}
      </div>
    {/each}
  </section>

  <aside class="props">
    {#if !selected}
      <p class="props-empty">{t($locale, 'flow.props.empty')}</p>
    {:else}
      <p class="section-heading mb-3">{selected.kind}</p>
      <Input
        bind:value={selected.label}
        label={t($locale, 'flow.props.label')}
      />
      {#if selected.kind === 'source' || selected.kind === 'destination'}
        <Select
          bind:value={selected.connection_id}
          options={connOptions}
          label={t($locale, 'flow.props.connection')}
        />
      {:else}
        <StageConfigForm type={selected.kind} bind:config={selected.config} {schemas} />
      {/if}
      <div class="flex mt-3 justify-end">
        <Button variant="outline" on:click={() => deleteNode(selected.id)}>
          {t($locale, 'flow.props.delete')}
        </Button>
      </div>
    {/if}
  </aside>
</div>

<!-- ─── Header (above the shell) ────────────────────────────────── -->
<div class="flow-header">
  <div>
    <h2 class="text-2xl font-semibold" style="color: var(--text)">
      {t($locale, 'flow.title')}
    </h2>
    <p class="text-sm mt-1" style="color: var(--text-muted)">{t($locale, 'flow.help')}</p>
  </div>
  <div class="flex items-center gap-2">
    <Input bind:value={pipelineName} label={t($locale, 'flow.props.name')} placeholder="flow-1" />
    <Button variant="ghost" on:click={clearAll}>{t($locale, 'flow.actions.clear')}</Button>
    <Button on:click={saveAndDeploy} loading={saving}>{t($locale, 'flow.actions.save')}</Button>
  </div>
</div>

{#if error}
  <p class="mt-2" style="color: var(--danger)">{error}</p>
{/if}
{#if savedMsg}
  <p class="mt-2" style="color: var(--success)">{savedMsg}</p>
{/if}

<style>
  /* The flex header sits above the shell, which is a 3-column grid:
     palette · canvas · props. */
  :global(main:has(.flow-shell)) { padding: 0; }

  .flow-header {
    display: flex; align-items: flex-end; justify-content: space-between;
    padding: 16px 20px;
    border-bottom: 1px solid var(--border);
    background: var(--surface);
    gap: 16px;
  }

  .flow-shell {
    display: grid;
    grid-template-columns: 240px 1fr 300px;
    height: calc(100vh - 140px);
    min-height: 480px;
    background: var(--bg);
  }

  .palette, .props {
    background: var(--surface);
    border-inline-end: 1px solid var(--border);
    padding: 16px;
    overflow-y: auto;
  }
  .props { border-inline-end: none; border-inline-start: 1px solid var(--border); }
  .palette-heading {
    font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em;
    color: var(--text-muted); margin-bottom: 8px;
  }
  .palette-item, .palette-conn {
    display: flex; align-items: center; gap: 8px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface-2);
    color: var(--text);
    cursor: grab;
    margin-bottom: 6px;
    font-size: 13px;
    width: 100%;
    text-align: start;
  }
  .palette-item:hover, .palette-conn:hover { border-color: var(--accent); }
  .palette-conn { cursor: pointer; }
  .palette-help { font-size: 12px; color: var(--text-muted); margin-bottom: 8px; }
  .dot { width: 10px; height: 10px; border-radius: 50%; flex-shrink: 0; }

  .palette-file {
    display: block;
    color: var(--text-muted);
    font-size: 11px;
    margin-bottom: 6px;
  }
  .palette-sample {
    width: 100%;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    color: var(--text);
    padding: 6px 8px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 11px;
    resize: vertical;
  }
  .palette-paths {
    margin-top: 8px;
    display: flex; flex-wrap: wrap; gap: 4px;
  }
  .path-chip {
    border: 1px solid var(--border);
    border-radius: 12px; /* labeled chip — Brand Guide §5 / Rule 9 (pill is count-badge only) */
    padding: 3px 10px;
    font-size: 11px;
    color: var(--text);
    background: var(--surface);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    cursor: pointer;
    transition: border-color 120ms, background-color 120ms, color 120ms;
  }
  .path-chip:hover { border-color: var(--accent); }
  .path-chip.on {
    border-color: var(--accent);
    background: var(--accent);
    color: var(--bg);
    font-weight: 600;
  }
  .palette-use-all {
    margin-top: 8px;
    width: 100%;
    padding: 6px 8px;
    background: transparent;
    border: 1px solid var(--accent);
    color: var(--accent);
    border-radius: 12px;
    cursor: pointer;
    font-size: 12px;
  }
  .palette-use-all:hover { background: var(--accent); color: var(--bg); }

  .try-row {
    display: flex; flex-wrap: wrap; align-items: center;
    gap: 4px;
    margin-bottom: 6px;
  }
  .try-label { font-size: 11px; color: var(--text-muted); }
  .try-btn {
    border: 1px solid var(--border);
    border-radius: 12px; /* labeled chip — Brand Guide §5 / Rule 9 (pill is count-badge only) */
    padding: 2px 8px;
    font-size: 10px;
    background: var(--surface);
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    cursor: pointer;
    transition: border-color 120ms, color 120ms;
  }
  .try-btn:hover { border-color: var(--accent); color: var(--accent); }

  .canvas-wrap {
    position: relative;
    overflow: auto;
    background-image: radial-gradient(circle, var(--surface-2) 1px, transparent 1px);
    background-size: 20px 20px;
  }
  .canvas-empty {
    position: absolute; inset: 0;
    display: flex; align-items: center; justify-content: center;
    color: var(--text-muted);
    font-size: 14px;
    pointer-events: none;
  }
  .edges { position: absolute; inset: 0; width: 100%; height: 100%; pointer-events: none; }
  .edge { stroke: var(--accent); stroke-width: 2; fill: none; }
  .edge-drag { stroke-dasharray: 5 4; opacity: 0.7; }

  .flow-node {
    position: absolute;
    width: 180px; min-height: 56px;
    background: var(--surface);
    border: 2px solid var(--border);
    border-radius: 12px;
    user-select: none;
    cursor: move;
    box-shadow: 0 2px 4px rgba(0,0,0,0.15);
  }
  .flow-node.selected {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 25%, transparent);
  }
  .flow-node-head {
    display: flex; align-items: center; gap: 6px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
    font-size: 12px;
    color: var(--text);
  }
  .flow-node-kind {
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    font-size: 11px;
  }
  .flow-node-body {
    padding: 8px 10px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .delete-btn {
    margin-inline-start: auto;
    background: var(--danger); color: #fff;
    border: none; cursor: pointer;
    width: 18px; height: 18px;
    border-radius: 50%;
    font-size: 12px; line-height: 1;
  }

  .port {
    position: absolute;
    top: 50%; transform: translateY(-50%);
    width: 12px; height: 12px;
    background: var(--accent);
    border: 2px solid var(--surface);
    border-radius: 50%;
    cursor: crosshair;
    z-index: 2;
  }
  /*
   * The ports live on the OUT (right) and IN (left) edges of every
   * stage node. This is the canvas's spatial convention — message flow
   * is source → destination, drawn left-to-right. We intentionally use
   * physical left/right here (not inline-start/end) because the canvas
   * stores absolute coordinates the operator dragged; flipping ports
   * under [dir="rtl"] would break the visual mapping of every saved
   * flow. Brand Guide §RTL applies to flowing content, not free-form
   * spatial editors.
   */
  .port-out { right: -7px; }
  .port-in  { left:  -7px; }

  .props-empty { color: var(--text-muted); font-size: 13px; }
  .section-heading {
    font-size: 12px; font-weight: 600;
    color: var(--text);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
</style>
