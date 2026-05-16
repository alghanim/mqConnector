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
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import {
    api,
    type Connection,
    type Pipeline,
    type RoutingRule,
    type Schema,
    type Stage,
    type StageType,
    type Transform
  } from '$lib/api';
  import {
    topoFromSource as topoFromSourceCore,
    pipelineToFlow
  } from '$lib/flow-graph';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import StageConfigForm from '$lib/components/StageConfigForm.svelte';
  import TransformListEditor from '$lib/components/TransformListEditor.svelte';
  import RoutingRuleListEditor from '$lib/components/RoutingRuleListEditor.svelte';
  import { SAMPLE_FIXTURES } from '$lib/sample-fixtures';
  import {
    Plug,
    Send,
    Filter as FilterIcon,
    Shuffle,
    Languages,
    GitFork,
    Code2,
    ShieldCheck,
    ZoomIn,
    ZoomOut,
    Maximize2,
    HelpCircle,
    CheckCircle2,
    AlertTriangle,
    Trash2,
    Plus,
    Database,
    PanelLeftClose
  } from 'lucide-svelte';

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
  // Round-trip state — when the page is opened with ?pipeline=:id we
  // load that pipeline into the canvas and update-save instead of
  // create-save. `null` means "draw a new pipeline" (legacy behaviour).
  let pipelineId: string | null = null;
  let loadingPipeline = false;
  // Pipeline-level transforms list (rename/mask/move/set/delete ops).
  // Lives at pipeline scope, not per-node — every `transform` stage in
  // the chain consumes the same list. Edited from the props panel when
  // a transform node is selected, persisted on Save & Deploy.
  let transforms: Transform[] = [];
  // Pipeline-level routing-rules list. Same model as transforms: rules
  // live at pipeline scope; a `route` stage in the chain is what makes
  // them fire at runtime. Edited in the props panel when the route
  // node is selected. Destination nodes on the canvas are visual only
  // — the rule's destination_id points at a connection.
  let rules: RoutingRule[] = [];

  // ─── Preview drawer (Phase 4) ─────────────────────────────────────
  // A pinnable drawer at the bottom of the canvas that runs the live
  // canvas state against the /v1/preview endpoint and shows what would
  // leave the pipeline. No brokers are touched — server-side dry run.
  let previewOpen = false;
  let previewing = false;
  let previewOutput = '';
  let previewFormat = '';
  let previewError = '';

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
    // Every NodeKind has an entry in `palette`, so the fallback is
    // defensive only. Use the muted-text token so even an unknown kind
    // still respects the brand palette.
    return palette.find((p) => p.kind === kind)?.tone ?? 'var(--text-muted)';
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

  /**
   * Reconstruct the canvas from an existing pipeline. Called when the
   * page is opened with ?pipeline=:id — turns the form-editor's flat
   * stage list + routing rules back into nodes + edges so the operator
   * can edit visually. The save step then updates the same pipeline
   * rather than POSTing a new one.
   */
  async function loadExistingPipeline(id: string) {
    loadingPipeline = true;
    error = '';
    try {
      const [pipe, stages, loadedRules, txs] = await Promise.all([
        api.get<Pipeline>(`/v1/pipelines/${id}`),
        api.get<Stage[]>(`/v1/pipelines/${id}/stages`).then((v) => v ?? []),
        api.get<RoutingRule[]>(`/v1/pipelines/${id}/routing-rules`).then((v) => v ?? []),
        api.get<Transform[]>(`/v1/pipelines/${id}/transforms`).then((v) => v ?? [])
      ]);
      const recon = pipelineToFlow(
        { source_id: pipe.source_id, destination_id: pipe.destination_id },
        stages.map((s) => ({
          stage_order: s.stage_order,
          stage_type: s.stage_type as NodeKind,
          stage_config: s.stage_config,
          enabled: s.enabled
        })),
        loadedRules
      );
      pipelineId = id;
      pipelineName = pipe.name;
      nodes = recon.nodes;
      edges = recon.edges;
      transforms = [...txs].sort((a, b) => a.order - b.order);
      rules = [...loadedRules].sort((a, b) => a.priority - b.priority);
      // Seed the local id counter past whatever the reconstruction
      // produced, otherwise a palette drop could collide with 'n3'.
      nextNodeIdCounter = recon.nextIdCounter;
      selectedId = null;
    } catch (e: unknown) {
      error = (e as { message?: string }).message || t($locale, 'flow.loadError');
    } finally {
      loadingPipeline = false;
    }
  }

  onMount(async () => {
    await loadBackground();
    const pid = $page.url.searchParams.get('pipeline');
    if (pid) {
      await loadExistingPipeline(pid);
    }
  });

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

  // ─── World coordinate helper ─────────────────────────────────────
  // After Phase 5 the canvas sits inside a zoomable + scrollable
  // `.canvas-world` div. Mouse events arrive in viewport coordinates,
  // but every interaction (drop / drag / connect-line) needs to land
  // in *world* coordinates — the same space `n.x` / `n.y` live in —
  // otherwise the visual drag line and drop position drift the moment
  // the operator scrolls or zooms even a little.
  //
  // Formula:
  //   worldX = (e.clientX - canvasViewport.left + canvasViewport.scrollLeft) / zoom
  //   worldY = (e.clientY - canvasViewport.top  + canvasViewport.scrollTop ) / zoom
  //
  // Returns NaN if the canvas ref isn't bound yet — callers must
  // short-circuit on that.
  function worldCoords(e: { clientX: number; clientY: number }): { x: number; y: number } {
    if (!canvasEl) return { x: NaN, y: NaN };
    const rect = canvasEl.getBoundingClientRect();
    return {
      x: (e.clientX - rect.left + canvasEl.scrollLeft) / zoom,
      y: (e.clientY - rect.top + canvasEl.scrollTop) / zoom
    };
  }

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
    const w = worldCoords(e);
    const node: FlowNode = {
      id: newNodeId(),
      kind,
      // Centre the dropped node on the cursor (NODE_W/2 = 90, head/2 ~= 24).
      x: Math.max(8, w.x - 90),
      y: Math.max(8, w.y - 24),
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
    const w = worldCoords(e);
    dragging = {
      id: node.id,
      offsetX: w.x - node.x,
      offsetY: w.y - node.y
    };
  }

  function onCanvasMouseMove(e: MouseEvent) {
    if (!canvasEl) return;
    if (dragging) {
      const w = worldCoords(e);
      const id = dragging.id;
      const x = Math.max(0, w.x - dragging.offsetX);
      const y = Math.max(0, w.y - dragging.offsetY);
      nodes = nodes.map((n) => (n.id === id ? { ...n, x, y } : n));
    } else if (connecting) {
      const w = worldCoords(e);
      connecting = {
        ...connecting,
        cursorX: w.x,
        cursorY: w.y
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
    const w = worldCoords(e);
    connecting = {
      fromId: node.id,
      cursorX: w.x,
      cursorY: w.y
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
    transforms = [];
    rules = [];
    // Clearing the canvas also drops the pipeline binding — otherwise
    // hitting Save would silently empty the loaded pipeline's stages.
    pipelineId = null;
    pipelineName = '';
    const u = new URL(window.location.href);
    if (u.searchParams.has('pipeline')) {
      u.searchParams.delete('pipeline');
      goto(u.pathname + (u.search ? u.search : ''), {
        replaceState: true,
        noScroll: true
      });
    }
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

  // Resolve edges to their (from, to) nodes + pre-computed path string.
  // We do this in a `$:` block (not inline in the {#each}) so Svelte's
  // dependency tracker sees `nodes` as a dep — otherwise the path
  // stays at the OLD node positions when the operator drags one of
  // the endpoints, because Svelte doesn't trace through the byId()
  // call to discover that the each-body depends on `nodes`.
  type ResolvedEdge = { from: string; to: string; path: string };
  let resolvedEdges: ResolvedEdge[] = [];
  $: resolvedEdges = (() => {
    const byId = new Map(nodes.map((n) => [n.id, n]));
    const out: ResolvedEdge[] = [];
    for (const e of edges) {
      const a = byId.get(e.from);
      const b = byId.get(e.to);
      if (a && b) out.push({ from: e.from, to: e.to, path: edgePath(a, b) });
    }
    return out;
  })();

  // ─── Preview (Phase 4) ────────────────────────────────────────────
  /**
   * Build the linear stage chain from current canvas nodes (the same
   * extraction Save & Deploy does, minus persistence). Throws on the
   * same validation failures so the operator sees identical messages
   * whether they preview or deploy.
   */
  function extractStagesForPreview(): {
    stages: { stage_order: number; stage_type: string; stage_config: string; enabled: boolean }[];
  } {
    const sources = nodes.filter((n) => n.kind === 'source');
    const destinations = nodes.filter((n) => n.kind === 'destination');
    if (sources.length !== 1) throw new Error(t($locale, 'flow.error.needSource'));
    if (destinations.length < 1) throw new Error(t($locale, 'flow.error.needDest'));
    const source = sources[0];

    const order = topoFromSource(source.id);
    const reachable = new Set(order);
    for (const d of destinations) {
      if (!reachable.has(d.id)) throw new Error(t($locale, 'flow.error.notConnected'));
    }

    const stageNodes: FlowNode[] = [];
    for (const id of order) {
      const n = byId(id);
      if (!n) continue;
      if (n.kind === 'source') continue;
      if (n.kind === 'destination') break;
      stageNodes.push(n);
    }
    for (const s of stageNodes) {
      try {
        JSON.parse(s.config || '{}');
      } catch {
        throw new Error(`${s.kind} (${s.label}): config is not valid JSON`);
      }
    }
    return {
      stages: stageNodes.map((n, i) => ({
        stage_order: i + 1,
        stage_type: n.kind === 'route' ? 'route' : n.kind,
        stage_config: n.config || '{}',
        enabled: true
      }))
    };
  }

  async function runPreview() {
    previewing = true;
    previewError = '';
    previewOutput = '';
    previewFormat = '';
    previewOpen = true;
    try {
      if (!flowSample.trim()) {
        throw new Error(t($locale, 'preview.error.noSample'));
      }
      const { stages } = extractStagesForPreview();
      const r = await api.post<{
        ok: boolean;
        output: string;
        format: string;
        error?: string;
      }>('/v1/preview', {
        stages,
        transforms,
        routing_rules: rules,
        // The canvas doesn't expose output_format yet (Phase 5 will
        // surface it on the destination node). 'same' matches what
        // Save & Deploy creates for a fresh pipeline.
        output_format: 'same',
        sample: flowSample
      });
      if (!r.ok) {
        previewError = r.error || t($locale, 'preview.error.failed');
        return;
      }
      previewOutput = r.output;
      previewFormat = r.format;
    } catch (e: unknown) {
      previewError = (e as { message?: string }).message || t($locale, 'preview.error.failed');
    } finally {
      previewing = false;
    }
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

      // 1. Create the pipeline, OR update the one we loaded with
      //    ?pipeline=:id. Update mode preserves output_format, filter_paths,
      //    enabled, schema_id — fields the canvas doesn't currently
      //    expose; refetching avoids clobbering them with defaults.
      const defaultDest = destinations[0];
      let pipe: { id: string };
      if (pipelineId) {
        const current = await api.get<Pipeline>(`/v1/pipelines/${pipelineId}`);
        const updated: Pipeline = {
          ...current,
          id: pipelineId,
          name: pipelineName || current.name,
          source_id: source.connection_id,
          destination_id: defaultDest.connection_id
        };
        await api.put(`/v1/pipelines/${pipelineId}`, updated);
        pipe = { id: pipelineId };
      } else {
        pipe = await api.post<{ id: string }>('/v1/pipelines', {
          name: pipelineName || 'flow-' + Date.now(),
          source_id: source.connection_id,
          destination_id: defaultDest.connection_id,
          output_format: 'same',
          filter_paths: [],
          enabled: true
        });
        pipelineId = pipe.id;
        // Reflect the new id in the URL so a refresh round-trips back
        // into edit mode instead of starting a blank canvas.
        const u = new URL(window.location.href);
        u.searchParams.set('pipeline', pipe.id);
        goto(u.pathname + u.search, { replaceState: true, noScroll: true });
      }

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

      // 2b. Pipeline-level transforms. Only persist when there's at
      // least one `transform` stage in the chain — otherwise the list
      // is unreachable and we leave it empty rather than carry stale
      // data. Re-number `order` so it matches the editor's view.
      const hasTransformStage = stageNodes.some((n) => n.kind === 'transform');
      const txsToPersist = hasTransformStage
        ? transforms.map((tr, i) => ({ ...tr, order: i + 1 }))
        : [];
      await api.put(`/v1/pipelines/${pipe.id}/transforms`, txsToPersist);

      // 3. Routing rules. The rules list is now the source of truth —
      // edited from the route node's props panel, persisted whole. If
      // there's no route stage in the chain the rules are unreachable,
      // so we wipe them rather than carry stale data forward.
      const routeNode = stageNodes.find((n) => n.kind === 'route');
      const rulesToPersist = routeNode
        ? rules
            .map((r, i) => ({ ...r, priority: r.priority || i + 1 }))
            .sort((a, b) => a.priority - b.priority)
        : [];
      await api.put(`/v1/pipelines/${pipe.id}/routing-rules`, rulesToPersist);

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

  // ─── Icon + metadata per stage kind ──────────────────────────────
  // The palette + canvas nodes both need an icon, a translated label,
  // and a one-line helper for tooltips/onboarding. Centralised here so
  // adding a new stage type is one row, not three files.
  const KIND_META: Record<NodeKind, { icon: typeof Plug; tone: string }> = {
    source: { icon: Plug, tone: 'var(--primary)' },
    destination: { icon: Send, tone: 'var(--secondary)' },
    filter: { icon: FilterIcon, tone: 'var(--qb-slate-130)' },
    transform: { icon: Shuffle, tone: 'var(--qb-olive-gold)' },
    translate: { icon: Languages, tone: 'var(--qb-sand)' },
    route: { icon: GitFork, tone: 'var(--qb-slate-120)' },
    script: { icon: Code2, tone: 'var(--qb-slate-110)' },
    validate: { icon: ShieldCheck, tone: 'var(--qb-warm-gray)' }
  };
  function iconFor(kind: NodeKind) {
    return KIND_META[kind]?.icon ?? Plug;
  }
  function kindLabel(kind: NodeKind): string {
    return t($locale, `flow.kind.${kind}`);
  }
  function kindHelp(kind: NodeKind): string {
    return t($locale, `flow.kind.help.${kind}`);
  }

  // ─── Canvas zoom + pan ───────────────────────────────────────────
  // Zoom is a CSS transform on the inner "world" layer, not on the
  // viewport itself — so the SVG edge layer + node layer scale
  // together but the dotted background stays at 1:1 (a small price
  // for crisp grid dots under zoom-out).
  let zoom = 1;
  const MIN_ZOOM = 0.5;
  const MAX_ZOOM = 1.5;
  function zoomIn() {
    zoom = Math.min(MAX_ZOOM, Math.round((zoom + 0.1) * 100) / 100);
  }
  function zoomOut() {
    zoom = Math.max(MIN_ZOOM, Math.round((zoom - 0.1) * 100) / 100);
  }
  function zoomReset() {
    zoom = 1;
  }
  function onCanvasWheel(e: WheelEvent) {
    // Cmd/Ctrl + wheel zooms; bare wheel scrolls (browser default).
    // We intentionally don't intercept bare scroll so the operator
    // can pan the canvas with the trackpad.
    if (!(e.ctrlKey || e.metaKey)) return;
    e.preventDefault();
    if (e.deltaY < 0) zoomIn();
    else zoomOut();
  }

  // ─── Dirty tracking ──────────────────────────────────────────────
  // Used by the status bar's "saved/dirty" pill. A small heuristic
  // serializer covers the cases the operator actually edits — node
  // positions and edges are deliberately excluded so a layout tweak
  // doesn't read as dirty.
  let baseline = '';
  $: signature = JSON.stringify({
    name: pipelineName,
    nodes: nodes.map((n) => ({
      id: n.id,
      kind: n.kind,
      label: n.label,
      connection_id: n.connection_id,
      config: n.config
    })),
    edges: edges.map((e) => ({ from: e.from, to: e.to })),
    transforms,
    rules
  });
  $: dirty = baseline !== '' && signature !== baseline;
  function markClean() {
    baseline = signature;
  }

  // ─── Validation summary (drives the status bar pill) ─────────────
  $: validation = (() => {
    const sources = nodes.filter((n) => n.kind === 'source');
    const dests = nodes.filter((n) => n.kind === 'destination');
    if (nodes.length === 0) return { ok: false, msg: '' };
    if (sources.length !== 1) return { ok: false, msg: t($locale, 'flow.error.needSource') };
    if (dests.length < 1) return { ok: false, msg: t($locale, 'flow.error.needDest') };
    if (!sources[0].connection_id) return { ok: false, msg: t($locale, 'flow.error.missingConn') };
    for (const d of dests) if (!d.connection_id) return { ok: false, msg: t($locale, 'flow.error.missingConn') };
    try {
      const order = topoFromSourceCore(
        nodes.map((n) => ({ id: n.id, kind: n.kind })),
        edges,
        sources[0].id
      );
      const reachable = new Set(order);
      for (const d of dests) if (!reachable.has(d.id)) {
        return { ok: false, msg: t($locale, 'flow.error.notConnected') };
      }
    } catch {
      return { ok: false, msg: t($locale, 'flow.error.cycleDetected') };
    }
    return { ok: true, msg: t($locale, 'flow.status.valid') };
  })();

  // ─── Clear-canvas confirmation dialog ────────────────────────────
  let clearDialogOpen = false;
  function askClear() {
    clearDialogOpen = true;
  }
  function confirmClear() {
    clearDialogOpen = false;
    clearAllImmediate();
  }
  function cancelClear() {
    clearDialogOpen = false;
  }
  // The old clearAll() used window.confirm() — keep it as the
  // immediate implementation so existing callers still work.
  function clearAllImmediate() {
    nodes = [];
    edges = [];
    selectedId = null;
    error = '';
    savedMsg = '';
    transforms = [];
    rules = [];
    pipelineId = null;
    pipelineName = '';
    const u = new URL(window.location.href);
    if (u.searchParams.has('pipeline')) {
      u.searchParams.delete('pipeline');
      goto(u.pathname + (u.search ? u.search : ''), {
        replaceState: true,
        noScroll: true
      });
    }
    markClean();
  }

  // Mark clean after a successful save so the pill flips back to
  // "Saved". Done in a reactive block so the existing saveAndDeploy
  // doesn't need to know about dirty tracking.
  $: if (savedMsg) markClean();
</script>

<svelte:window on:mousemove={onCanvasMouseMove} on:mouseup={onCanvasMouseUp} />

<!-- ─── Header (sticky, above the shell) ───────────────────────────── -->
<header class="flow-header">
  <div class="flow-header-title">
    <h2 class="flow-header-h">
      {#if pipelineId}
        <span class="editing-tag">{t($locale, 'flow.editing')}</span>
        {pipelineName || t($locale, 'flow.title')}
      {:else}
        {t($locale, 'flow.title')}
      {/if}
    </h2>
    <p class="flow-header-sub">
      {#if loadingPipeline}
        {t($locale, 'flow.loading')}
      {:else}
        {t($locale, 'flow.subtitle')}
      {/if}
    </p>
  </div>
  <div class="flow-header-actions">
    <div class="flow-header-name">
      <Input
        bind:value={pipelineName}
        label={t($locale, 'flow.props.name')}
        placeholder="flow-1"
      />
    </div>
    {#if pipelineId}
      <Button variant="ghost" on:click={() => goto('/flow')}>
        {t($locale, 'flow.actions.newFlow')}
      </Button>
    {/if}
    <Button variant="ghost" on:click={askClear}>
      {t($locale, 'flow.actions.clear')}
    </Button>
    <Button variant="outline" on:click={runPreview} loading={previewing}>
      {t($locale, 'preview.run')}
    </Button>
    <Button on:click={saveAndDeploy} loading={saving} disabled={!validation.ok}>
      {t($locale, 'flow.actions.save')}
    </Button>
  </div>
</header>

{#if error}
  <div class="flow-banner"><Alert variant="error">{error}</Alert></div>
{/if}
{#if savedMsg}
  <div class="flow-banner"><Alert variant="success">{savedMsg}</Alert></div>
{/if}

<div class="flow-shell" class:preview-open={previewOpen}>
  <!-- ─── Palette ────────────────────────────────────────────────── -->
  <aside class="palette" aria-label={t($locale, 'flow.section.stages')}>
    <section class="palette-section">
      <header class="palette-section-head">
        <span class="palette-section-icon" aria-hidden="true">
          <Database size={14} strokeWidth={1.75} />
        </span>
        <p class="palette-section-title">{t($locale, 'flow.section.stages')}</p>
      </header>
      <div class="palette-grid">
        {#each palette as p (p.kind)}
          {@const Icon = iconFor(p.kind)}
          <div
            class="palette-card"
            role="button"
            tabindex="0"
            draggable="true"
            on:dragstart={(e) => onPaletteDragStart(e, p.kind)}
            style:--tone={p.tone}
            title={kindHelp(p.kind)}
          >
            <span class="palette-card-icon" aria-hidden="true">
              <svelte:component this={Icon} size={16} strokeWidth={1.75} />
            </span>
            <div class="palette-card-text">
              <span class="palette-card-label">{kindLabel(p.kind)}</span>
              <span class="palette-card-help">{kindHelp(p.kind)}</span>
            </div>
          </div>
        {/each}
      </div>
    </section>

    <section class="palette-section">
      <header class="palette-section-head">
        <span class="palette-section-icon" aria-hidden="true">
          <FilterIcon size={14} strokeWidth={1.75} />
        </span>
        <p class="palette-section-title">{t($locale, 'flow.palette.sample')}</p>
      </header>
      <p class="palette-help">{t($locale, 'flow.palette.sampleHelp')}</p>
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
    </section>

    <section class="palette-section">
      <header class="palette-section-head">
        <span class="palette-section-icon" aria-hidden="true">
          <Plug size={14} strokeWidth={1.75} />
        </span>
        <p class="palette-section-title">{t($locale, 'flow.palette.connections')}</p>
      </header>
      {#if connections.length === 0}
        <p class="palette-help">{t($locale, 'flow.connections.empty')}</p>
      {:else}
        <p class="palette-help">{t($locale, 'flow.palette.connectionsHelp')}</p>
        {#each connections as c (c.id)}
          <button
            type="button"
            class="palette-conn"
            on:click={() => navigator.clipboard?.writeText(c.id || '')}
            title={c.id}
          >
            <span class="palette-conn-dot" style="background: var(--text-muted)"></span>
            <span class="palette-conn-name">{c.name}</span>
            <span class="palette-conn-type">{c.type}</span>
          </button>
        {/each}
      {/if}
    </section>
  </aside>

  <!-- ─── Canvas ─────────────────────────────────────────────────── -->
  <section
    class="canvas-wrap"
    bind:this={canvasEl}
    on:dragover={onCanvasDragOver}
    on:drop={onCanvasDrop}
    on:wheel={onCanvasWheel}
    role="application"
    aria-label="Flow canvas"
  >
    <!-- Floating canvas controls -->
    <div class="canvas-controls" aria-label="Canvas controls">
      <button
        type="button"
        class="canvas-ctl"
        on:click={zoomIn}
        aria-label={t($locale, 'flow.canvas.controls.zoomIn')}
        title={t($locale, 'flow.canvas.controls.zoomIn')}
      >
        <ZoomIn size={14} strokeWidth={1.75} />
      </button>
      <button
        type="button"
        class="canvas-ctl"
        on:click={zoomOut}
        aria-label={t($locale, 'flow.canvas.controls.zoomOut')}
        title={t($locale, 'flow.canvas.controls.zoomOut')}
      >
        <ZoomOut size={14} strokeWidth={1.75} />
      </button>
      <button
        type="button"
        class="canvas-ctl"
        on:click={zoomReset}
        aria-label={t($locale, 'flow.canvas.controls.fit')}
        title={t($locale, 'flow.canvas.controls.fit')}
      >
        <Maximize2 size={14} strokeWidth={1.75} />
      </button>
      <button
        type="button"
        class="canvas-ctl"
        aria-label={t($locale, 'flow.canvas.controls.help')}
        title={t($locale, 'flow.canvas.controls.help')}
      >
        <HelpCircle size={14} strokeWidth={1.75} />
      </button>
    </div>

    <!-- Zoomable inner layer ("world"). Both the edge SVG and the
         positioned nodes scale together. -->
    <div class="canvas-world" style:transform="scale({zoom})">
      <svg
        bind:this={svgEl}
        class="edges"
        preserveAspectRatio="xMinYMin"
        role="presentation"
      >
        <defs>
          <marker
            id="edge-arrow"
            viewBox="0 0 8 8"
            refX="6"
            refY="4"
            markerWidth="6"
            markerHeight="6"
            orient="auto-start-reverse"
          >
            <path d="M0,0 L8,4 L0,8 z" />
          </marker>
        </defs>
        {#each resolvedEdges as e (e.from + '→' + e.to)}
          <path d={e.path} class="edge" marker-end="url(#edge-arrow)" />
        {/each}
        {#if connecting}
          {@const a = byId(connecting.fromId)}
          {#if a}
            <path d={dragPath(a, connecting.cursorX, connecting.cursorY)} class="edge edge-drag" />
          {/if}
        {/if}
      </svg>

      {#each nodes as n (n.id)}
        {@const Icon = iconFor(n.kind)}
        <div
          class="flow-node flow-node-{n.kind}"
          class:selected={n.id === selectedId}
          style="left: {n.x}px; top: {n.y}px; --tone: {toneFor(n.kind)}"
          on:mousedown={(e) => onNodeMouseDown(e, n)}
          on:click={() => (selectedId = n.id)}
          role="presentation"
        >
          <div class="flow-node-head">
            <span class="flow-node-icon" aria-hidden="true">
              <svelte:component this={Icon} size={14} strokeWidth={1.75} />
            </span>
            <span class="flow-node-kind">{kindLabel(n.kind)}</span>
            {#if n.id === selectedId}
              <button
                type="button"
                class="delete-btn"
                aria-label={t($locale, 'flow.props.delete')}
                title={t($locale, 'flow.props.delete')}
                on:click|stopPropagation={() => deleteNode(n.id)}
              >
                <Trash2 size={12} strokeWidth={2} />
              </button>
            {/if}
          </div>
          <div class="flow-node-body">
            {#if n.kind === 'source' || n.kind === 'destination'}
              {#if n.connection_id}
                <span class="flow-node-connection">
                  {connections.find((c) => c.id === n.connection_id)?.name || n.connection_id}
                </span>
              {:else}
                <span class="flow-node-missing">{t($locale, 'flow.props.connection')}…</span>
              {/if}
            {:else}
              {n.label || kindLabel(n.kind)}
            {/if}
          </div>
          {#if n.kind !== 'source'}
            <span
              class="port port-in"
              role="presentation"
              aria-label="input"
              on:mouseup={(e) => onPortInMouseUp(e, n)}
            ></span>
          {/if}
          {#if n.kind !== 'destination'}
            <span
              class="port port-out"
              role="presentation"
              aria-label="output"
              on:mousedown={(e) => onPortOutMouseDown(e, n)}
            ></span>
          {/if}
        </div>
      {/each}
    </div>

    {#if nodes.length === 0}
      <div class="canvas-empty">
        <div class="canvas-empty-icon" aria-hidden="true">
          <Plus size={24} strokeWidth={1.5} />
        </div>
        <p class="canvas-empty-title">{t($locale, 'flow.canvas.empty')}</p>
        <p class="canvas-empty-sub">{t($locale, 'flow.canvas.controls.help')}</p>
      </div>
    {/if}

    <!-- ─── Canvas status bar ────────────────────────────────────── -->
    <footer class="canvas-status">
      <div class="canvas-status-validity">
        {#if validation.ok}
          <span class="status-pill status-pill-success">
            <CheckCircle2 size={12} strokeWidth={2} />
            <span>{validation.msg}</span>
          </span>
        {:else if validation.msg}
          <span class="status-pill status-pill-warning">
            <AlertTriangle size={12} strokeWidth={2} />
            <span>{validation.msg}</span>
          </span>
        {/if}
        {#if pipelineId}
          {#if dirty}
            <span class="status-pill status-pill-neutral">
              <PanelLeftClose size={12} strokeWidth={2} />
              <span>{t($locale, 'flow.status.dirty')}</span>
            </span>
          {:else}
            <span class="status-pill status-pill-muted">
              <CheckCircle2 size={12} strokeWidth={2} />
              <span>{t($locale, 'flow.status.clean')}</span>
            </span>
          {/if}
        {/if}
      </div>
      <div class="canvas-status-counts">
        <span class="status-count">
          <span class="status-count-label">{t($locale, 'flow.status.nodes')}</span>
          <span class="status-count-value">{nodes.length}</span>
        </span>
        <span class="status-count">
          <span class="status-count-label">{t($locale, 'flow.status.edges')}</span>
          <span class="status-count-value">{edges.length}</span>
        </span>
        <span class="status-count">
          <span class="status-count-label">{t($locale, 'flow.status.zoom')}</span>
          <span class="status-count-value">{Math.round(zoom * 100)}%</span>
        </span>
      </div>
    </footer>
  </section>

  <!-- ─── Inspector ─────────────────────────────────────────────── -->
  <aside class="inspector" aria-label={t($locale, 'flow.inspector.title')}>
    {#if !selected}
      <div class="inspector-empty">
        <p class="inspector-empty-title">{t($locale, 'flow.inspector.title')}</p>
        <p class="inspector-empty-body">{t($locale, 'flow.props.empty')}</p>
      </div>
    {:else}
      {@const SelIcon = iconFor(selected.kind)}
      <header class="inspector-head" style:--tone={toneFor(selected.kind)}>
        <span class="inspector-icon" aria-hidden="true">
          <svelte:component this={SelIcon} size={16} strokeWidth={1.75} />
        </span>
        <div class="inspector-head-text">
          <p class="inspector-head-kind">{kindLabel(selected.kind)}</p>
          <p class="inspector-head-help">{kindHelp(selected.kind)}</p>
        </div>
      </header>

      <div class="inspector-body">
        <Input bind:value={selected.label} label={t($locale, 'flow.props.label')} />
        {#if selected.kind === 'source' || selected.kind === 'destination'}
          <Select
            bind:value={selected.connection_id}
            options={connOptions}
            label={t($locale, 'flow.props.connection')}
          />
        {:else if selected.kind === 'transform'}
          <StageConfigForm type={selected.kind} bind:config={selected.config} {schemas} />
          <div class="mt-3">
            <TransformListEditor bind:transforms compact />
          </div>
        {:else if selected.kind === 'route'}
          <StageConfigForm type={selected.kind} bind:config={selected.config} {schemas} />
          <div class="mt-3">
            <RoutingRuleListEditor bind:rules {connections} compact />
          </div>
        {:else}
          <StageConfigForm type={selected.kind} bind:config={selected.config} {schemas} />
        {/if}
      </div>

      <footer class="inspector-foot">
        <Button variant="outline" on:click={() => deleteNode(selected.id)}>
          <Trash2 size={14} strokeWidth={1.75} />
          <span>{t($locale, 'flow.inspector.deleteNode')}</span>
        </Button>
      </footer>
    {/if}
  </aside>

  <!--
    Preview drawer — spans all 3 grid columns, lives in grid-row 2 when
    open. Collapsed by default; runPreview() also opens it.
  -->
  {#if previewOpen}
    <div class="flow-preview">
      <div class="flow-preview-head">
        <div class="flex items-center gap-2">
          <span class="section-heading">{t($locale, 'preview.output')}</span>
          {#if previewFormat}
            <Badge variant="neutral">{previewFormat}</Badge>
          {/if}
          {#if previewing}
            <Badge variant="neutral">{t($locale, 'common.loading')}</Badge>
          {/if}
        </div>
        <div class="flex items-center gap-2">
          <Button variant="ghost" on:click={runPreview} loading={previewing}>
            {t($locale, 'preview.run')}
          </Button>
          <Button variant="ghost" on:click={() => (previewOpen = false)}>
            {t($locale, 'preview.close')}
          </Button>
        </div>
      </div>
      {#if previewError}
        <div class="mt-2"><Alert variant="error">{previewError}</Alert></div>
      {:else}
        <textarea
          class="flow-preview-output"
          readonly
          value={previewOutput}
          placeholder={t($locale, 'preview.empty')}
        ></textarea>
      {/if}
    </div>
  {/if}
</div>

<Dialog
  open={clearDialogOpen}
  title={t($locale, 'flow.clear.title')}
  confirmLabel={t($locale, 'flow.actions.clear')}
  cancelLabel={t($locale, 'common.cancel')}
  on:confirm={confirmClear}
  on:cancel={cancelClear}
>
  <p>{t($locale, 'flow.clear.body')}</p>
</Dialog>

<style>
  /* The flow editor takes the full main-column width and height. The
     parent <main> in +layout.svelte has its default padding zeroed for
     pages that host .flow-shell so palette/canvas/inspector can hug
     the chrome edges. */
  :global(main:has(.flow-shell)) {
    padding: 0;
  }

  /* ─── Header strip ────────────────────────────────────────────── */
  .flow-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 16px;
    padding: 14px 20px;
    background: var(--surface);
    border-block-end: 1px solid var(--border);
    position: sticky;
    inset-block-start: 0;
    z-index: 5;
  }
  .flow-header-title {
    min-inline-size: 0;
    flex: 1;
  }
  .flow-header-h {
    margin: 0;
    font-size: 20px;
    font-weight: 600;
    color: var(--text);
    letter-spacing: -0.01em;
  }
  .flow-header-sub {
    margin-block-start: 2px;
    color: var(--text-muted);
    font-size: 12px;
    line-height: 1.4;
  }
  .flow-header-actions {
    display: flex;
    align-items: flex-end;
    gap: 8px;
    flex-wrap: wrap;
  }
  .flow-header-name {
    inline-size: 220px;
  }
  /*
   * "Editing" eyebrow on the title. Uses --section-header (gold on
   * dark, dark-gold on light) per Brand Guide §5.8. Maroon is reserved
   * for primary/destructive CTAs.
   */
  .editing-tag {
    display: inline-block;
    margin-inline-end: 8px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--section-header);
    vertical-align: middle;
  }
  .flow-banner {
    padding: 8px 20px 0;
  }

  /* ─── Shell ───────────────────────────────────────────────────── */
  /*
   * 3-column grid: palette · canvas · inspector. The canvas grows to
   * fill the available width; the side panels are fixed so the operator
   * doesn't fight reflow as they drop nodes.
   */
  .flow-shell {
    display: grid;
    grid-template-columns: 260px 1fr 320px;
    grid-template-rows: 1fr;
    block-size: calc(100vh - 96px);
    min-block-size: 520px;
    background: var(--bg);
  }
  .flow-shell.preview-open {
    grid-template-rows: 1fr 260px;
  }
  .flow-preview {
    grid-column: 1 / -1;
    grid-row: 2;
    border-block-start: 1px solid var(--border);
    background: var(--surface);
    padding: 12px 16px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    overflow: hidden;
  }
  .flow-preview-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }
  .flow-preview-output {
    flex: 1;
    inline-size: 100%;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    padding: 8px 10px;
    resize: none;
  }
  .flow-preview-output:focus {
    outline: 2px solid var(--primary);
  }
  .section-heading {
    font-size: 12px;
    font-weight: 600;
    color: var(--text);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  /* ─── Palette ─────────────────────────────────────────────────── */
  .palette {
    background: var(--surface);
    border-inline-end: 1px solid var(--border);
    overflow-y: auto;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 18px;
  }
  .palette-section {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .palette-section-head {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .palette-section-icon {
    color: var(--text-tertiary);
    display: inline-flex;
  }
  .palette-section-title {
    margin: 0;
    color: var(--text-tertiary);
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }
  .palette-help {
    color: var(--text-muted);
    font-size: 11px;
    line-height: 1.45;
    margin: 0;
  }

  .palette-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px;
  }
  .palette-card {
    display: flex;
    align-items: flex-start;
    gap: 6px;
    padding: 8px 8px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--surface-2);
    color: var(--text);
    cursor: grab;
    user-select: none;
    transition:
      border-color 120ms,
      background-color 120ms,
      transform 80ms;
  }
  .palette-card:hover,
  .palette-card:focus-visible {
    border-color: var(--tone);
    outline: none;
  }
  .palette-card:active {
    cursor: grabbing;
    transform: translateY(1px);
  }
  .palette-card-icon {
    inline-size: 22px;
    block-size: 22px;
    border-radius: 6px;
    background: color-mix(in srgb, var(--tone) 18%, transparent);
    color: var(--tone);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
  }
  .palette-card-text {
    min-inline-size: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .palette-card-label {
    font-size: 12px;
    font-weight: 600;
    color: var(--text);
    line-height: 1.1;
  }
  .palette-card-help {
    font-size: 10px;
    color: var(--text-tertiary);
    line-height: 1.3;
    overflow: hidden;
    text-overflow: ellipsis;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
  }

  .palette-file {
    display: block;
    color: var(--text-muted);
    font-size: 11px;
  }
  .palette-sample {
    inline-size: 100%;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 10px;
    color: var(--text);
    padding: 6px 8px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 11px;
    resize: vertical;
  }
  .palette-paths {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .path-chip {
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 3px 10px;
    font-size: 11px;
    color: var(--text);
    background: var(--surface);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    cursor: pointer;
    transition:
      border-color 120ms,
      background-color 120ms,
      color 120ms;
  }
  .path-chip:hover {
    border-color: var(--accent);
  }
  .path-chip.on {
    border-color: var(--accent);
    background: var(--accent);
    color: var(--bg);
    font-weight: 600;
  }
  .palette-use-all {
    inline-size: 100%;
    padding: 6px 8px;
    background: transparent;
    border: 1px solid var(--accent);
    color: var(--accent);
    border-radius: 10px;
    cursor: pointer;
    font-size: 12px;
  }
  .palette-use-all:hover {
    background: var(--accent);
    color: var(--bg);
  }

  .try-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
  }
  .try-label {
    font-size: 11px;
    color: var(--text-muted);
  }
  .try-btn {
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 2px 8px;
    font-size: 10px;
    background: var(--surface);
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    cursor: pointer;
    transition:
      border-color 120ms,
      color 120ms;
  }
  .try-btn:hover {
    border-color: var(--accent);
    color: var(--accent);
  }

  .palette-conn {
    display: flex;
    align-items: center;
    gap: 8px;
    inline-size: 100%;
    padding: 6px 8px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--surface-2);
    color: var(--text);
    cursor: pointer;
    font-size: 12px;
    text-align: start;
    transition: border-color 120ms;
  }
  .palette-conn:hover {
    border-color: var(--secondary);
  }
  .palette-conn-dot {
    inline-size: 8px;
    block-size: 8px;
    border-radius: 50%;
    flex-shrink: 0;
  }
  .palette-conn-name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .palette-conn-type {
    color: var(--text-tertiary);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  /* ─── Canvas ──────────────────────────────────────────────────── */
  .canvas-wrap {
    position: relative;
    overflow: auto;
    background-color: var(--bg);
    background-image: radial-gradient(circle, var(--surface-2) 1px, transparent 1px);
    background-size: 20px 20px;
  }
  .canvas-world {
    position: relative;
    inline-size: 4000px;
    block-size: 2400px;
    transform-origin: 0 0;
  }
  .canvas-empty {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    color: var(--text-muted);
    pointer-events: none;
    padding: 24px;
    text-align: center;
  }
  .canvas-empty-icon {
    inline-size: 56px;
    block-size: 56px;
    border-radius: 50%;
    background: color-mix(in srgb, var(--primary) 12%, transparent);
    color: var(--secondary);
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  :global([data-theme='light']) .canvas-empty-icon {
    color: var(--primary);
  }
  .canvas-empty-title {
    font-size: 14px;
    font-weight: 600;
    color: var(--text);
  }
  .canvas-empty-sub {
    font-size: 12px;
    color: var(--text-muted);
    max-inline-size: 360px;
    line-height: 1.5;
  }

  /* Floating zoom + help controls. Sits inset-block-start: 12px /
     inset-inline-end: 12px so they hug the trailing edge in RTL too. */
  .canvas-controls {
    position: absolute;
    inset-block-start: 12px;
    inset-inline-end: 12px;
    z-index: 3;
    display: inline-flex;
    background: var(--surface);
    border: 1px solid var(--card-border);
    border-radius: 10px;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.18);
    overflow: hidden;
  }
  .canvas-ctl {
    inline-size: 30px;
    block-size: 30px;
    background: transparent;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-inline-end: 1px solid var(--divider);
  }
  .canvas-ctl:last-child {
    border-inline-end: none;
  }
  .canvas-ctl:hover {
    background: var(--surface-hover);
    color: var(--text);
  }

  .edges {
    position: absolute;
    inset: 0;
    inline-size: 100%;
    block-size: 100%;
    pointer-events: none;
  }
  .edge {
    stroke: color-mix(in srgb, var(--secondary) 70%, transparent);
    stroke-width: 2;
    fill: none;
  }
  :global([data-theme='light']) .edge {
    stroke: color-mix(in srgb, var(--primary) 60%, transparent);
  }
  .edge-drag {
    stroke-dasharray: 5 4;
    opacity: 0.7;
  }
  #edge-arrow path {
    fill: color-mix(in srgb, var(--secondary) 70%, transparent);
  }
  :global([data-theme='light']) #edge-arrow path {
    fill: color-mix(in srgb, var(--primary) 60%, transparent);
  }

  /* Status bar pinned to the bottom of the canvas viewport. position:
     sticky keeps it visible even when the canvas world is scrolled. */
  .canvas-status {
    position: sticky;
    inset-block-end: 0;
    inset-inline: 0;
    z-index: 3;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 6px 12px;
    background: color-mix(in srgb, var(--surface) 92%, transparent);
    border-block-start: 1px solid var(--divider);
    backdrop-filter: blur(8px);
    -webkit-backdrop-filter: blur(8px);
    font-size: 11px;
  }
  .canvas-status-validity {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    min-inline-size: 0;
  }
  .canvas-status-counts {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    color: var(--text-muted);
  }
  .status-count {
    display: inline-flex;
    align-items: baseline;
    gap: 4px;
  }
  .status-count-label {
    color: var(--text-tertiary);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-size: 9px;
  }
  .status-count-value {
    color: var(--text);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  .status-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 8px;
    border-radius: 12px;
    font-size: 11px;
    font-weight: 500;
    border: 1px solid transparent;
  }
  .status-pill-success {
    color: var(--success-solid);
    background: color-mix(in srgb, var(--success-solid) 12%, transparent);
    border-color: color-mix(in srgb, var(--success-solid) 30%, transparent);
  }
  .status-pill-warning {
    color: var(--warning);
    background: color-mix(in srgb, var(--warning) 14%, transparent);
    border-color: color-mix(in srgb, var(--warning) 32%, transparent);
  }
  .status-pill-neutral {
    color: var(--text);
    background: var(--surface-2);
    border-color: var(--divider);
  }
  .status-pill-muted {
    color: var(--text-tertiary);
    background: transparent;
    border-color: var(--divider);
  }

  /* ─── Flow node ───────────────────────────────────────────────── */
  .flow-node {
    position: absolute;
    inline-size: 190px;
    min-block-size: 60px;
    background: var(--surface);
    border: 1px solid var(--card-border);
    border-radius: 12px;
    user-select: none;
    cursor: move;
    box-shadow: 0 2px 6px rgba(0, 0, 0, 0.16);
    transition:
      box-shadow 150ms,
      border-color 150ms;
  }
  .flow-node:hover {
    box-shadow: 0 4px 14px rgba(0, 0, 0, 0.22);
  }
  :global([data-theme='light']) .flow-node {
    box-shadow: 0 2px 4px rgba(0, 0, 0, 0.06);
  }
  :global([data-theme='light']) .flow-node:hover {
    box-shadow: 0 4px 10px rgba(0, 0, 0, 0.1);
  }
  .flow-node.selected {
    border-color: var(--tone);
    box-shadow:
      0 0 0 3px color-mix(in srgb, var(--tone) 22%, transparent),
      0 4px 14px rgba(0, 0, 0, 0.22);
  }
  /* A small coloured spine on the inline-start edge encodes the kind at
     a glance, even when the operator is panning fast. */
  .flow-node::before {
    content: '';
    position: absolute;
    inset-block: 0;
    inset-inline-start: 0;
    inline-size: 3px;
    background: var(--tone);
    border-start-start-radius: 12px;
    border-end-start-radius: 12px;
  }

  .flow-node-head {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px 8px 12px;
    border-block-end: 1px solid var(--divider);
    color: var(--text);
  }
  .flow-node-icon {
    inline-size: 22px;
    block-size: 22px;
    border-radius: 6px;
    background: color-mix(in srgb, var(--tone) 18%, transparent);
    color: var(--tone);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
  }
  .flow-node-kind {
    font-weight: 600;
    letter-spacing: 0.02em;
    font-size: 12px;
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .flow-node-body {
    padding: 8px 10px 10px 12px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .flow-node-connection {
    color: var(--text);
    font-weight: 500;
  }
  .flow-node-missing {
    color: var(--warning);
    font-style: italic;
  }

  .delete-btn {
    inline-size: 20px;
    block-size: 20px;
    border-radius: 6px;
    background: transparent;
    color: var(--text-muted);
    border: 1px solid var(--divider);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    margin-inline-start: auto;
  }
  .delete-btn:hover {
    background: var(--danger);
    color: var(--danger-on);
    border-color: var(--danger);
  }

  .port {
    position: absolute;
    inset-block-start: 50%;
    transform: translateY(-50%);
    inline-size: 12px;
    block-size: 12px;
    background: var(--tone);
    border: 2px solid var(--surface);
    border-radius: 50%;
    cursor: crosshair;
    z-index: 2;
    transition: transform 120ms;
  }
  /*
   * Invisible expanded hit-target. 12px ports were too small to grab
   * reliably — the visible dot stays the same size, but the actual
   * clickable area is 28×28 so the operator doesn't have to nail
   * pixel-perfect drops to make a connection.
   */
  .port::after {
    content: '';
    position: absolute;
    inset: -8px;
    border-radius: 50%;
  }
  .port:hover {
    transform: translateY(-50%) scale(1.2);
  }
  /*
   * Ports stay on physical right/left edges — the canvas stores absolute
   * coordinates the operator dragged; flipping ports under [dir="rtl"]
   * would break the visual mapping of every saved flow.
   */
  .port-out {
    right: -7px;
  }
  .port-in {
    left: -7px;
  }

  /* ─── Inspector ───────────────────────────────────────────────── */
  .inspector {
    background: var(--surface);
    border-inline-start: 1px solid var(--border);
    overflow-y: auto;
    display: flex;
    flex-direction: column;
  }
  .inspector-empty {
    flex: 1;
    display: flex;
    flex-direction: column;
    justify-content: center;
    align-items: stretch;
    padding: 20px;
    gap: 4px;
  }
  .inspector-empty-title {
    color: var(--text-tertiary);
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }
  .inspector-empty-body {
    color: var(--text-muted);
    font-size: 13px;
    line-height: 1.5;
  }
  .inspector-head {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 14px 16px;
    border-block-end: 1px solid var(--divider);
  }
  .inspector-icon {
    inline-size: 32px;
    block-size: 32px;
    border-radius: 8px;
    background: color-mix(in srgb, var(--tone) 18%, transparent);
    color: var(--tone);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
  }
  .inspector-head-text {
    min-inline-size: 0;
  }
  .inspector-head-kind {
    color: var(--text);
    font-size: 14px;
    font-weight: 600;
    text-transform: capitalize;
  }
  .inspector-head-help {
    color: var(--text-muted);
    font-size: 11px;
    margin-block-start: 2px;
    line-height: 1.35;
  }
  .inspector-body {
    flex: 1;
    padding: 14px 16px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .inspector-foot {
    padding: 12px 16px;
    border-block-start: 1px solid var(--divider);
    display: flex;
    justify-content: flex-end;
  }

  /* ─── Responsive ──────────────────────────────────────────────── */
  @media (max-width: 1080px) {
    .flow-shell {
      grid-template-columns: 220px 1fr 280px;
    }
    .palette-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
