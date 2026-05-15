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
  import { api, type Connection } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';

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

  const palette: { kind: NodeKind; label: string; tone: string }[] = [
    { kind: 'source', label: 'S · source', tone: '#3FB950' },
    { kind: 'destination', label: 'D · destination', tone: '#F85149' },
    { kind: 'filter', label: 'F · filter', tone: '#9D6BFF' },
    { kind: 'transform', label: 'T · transform', tone: '#D29922' },
    { kind: 'translate', label: 'X · translate', tone: '#58A6FF' },
    { kind: 'route', label: 'R · route', tone: '#F18A4B' },
    { kind: 'script', label: 'JS · script', tone: '#39C5CF' },
    { kind: 'validate', label: 'V · validate', tone: '#D462C2' }
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

  // ─── Load saved connections so source/destination nodes can pick one ──
  async function loadConnections() {
    try {
      connections = (await api.get<Connection[]>('/v1/connections')) ?? [];
    } catch {
      connections = [];
    }
  }
  onMount(loadConnections);

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

  // Kahn's algorithm — also our cycle detector.
  function topoFromSource(sourceId: string): string[] {
    const inDeg = new Map<string, number>();
    nodes.forEach((n) => inDeg.set(n.id, 0));
    edges.forEach((e) => inDeg.set(e.to, (inDeg.get(e.to) || 0) + 1));

    const adj = new Map<string, string[]>();
    nodes.forEach((n) => adj.set(n.id, []));
    edges.forEach((e) => adj.get(e.from)!.push(e.to));

    const order: string[] = [];
    const queue: string[] = [sourceId];
    const visited = new Set<string>();
    while (queue.length) {
      const id = queue.shift()!;
      if (visited.has(id)) continue;
      visited.add(id);
      order.push(id);
      for (const next of adj.get(id) || []) {
        inDeg.set(next, (inDeg.get(next) || 0) - 1);
        if ((inDeg.get(next) || 0) <= 0) queue.push(next);
      }
    }
    if (order.length < nodes.filter((n) => visited.has(n.id) || n.kind === 'source').length) {
      // Some node still has inbound edges — cycle.
      if (edges.length > order.length - 1) {
        throw new Error(t($locale, 'flow.error.cycleDetected'));
      }
    }
    return order;
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
        <label class="config-label" for="props-config">{t($locale, 'flow.props.config')}</label>
        <textarea id="props-config" class="config-input" rows="9" bind:value={selected.config}></textarea>
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
  .port-out { right: -7px; }
  .port-in  { left:  -7px; }

  .props-empty { color: var(--text-muted); font-size: 13px; }
  .config-label {
    display: block;
    margin-top: 12px;
    margin-bottom: 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .config-input {
    width: 100%;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    padding: 8px 10px;
    resize: vertical;
  }
  .section-heading {
    font-size: 12px; font-weight: 600;
    color: var(--text);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
</style>
