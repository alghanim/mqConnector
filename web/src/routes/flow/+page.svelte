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
  import StageConfigForm from '$lib/components/StageConfigForm.svelte';
  import TransformListEditor from '$lib/components/TransformListEditor.svelte';
  import RoutingRuleListEditor from '$lib/components/RoutingRuleListEditor.svelte';
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
</script>

<svelte:window on:mousemove={onCanvasMouseMove} on:mouseup={onCanvasMouseUp} />

<div class="flow-shell" class:preview-open={previewOpen}>
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
      {:else if selected.kind === 'transform'}
        <!--
          A transform node is a position marker; the ops list lives at
          pipeline scope, so the props panel edits the shared
          `transforms` state rather than this node's config blob. The
          StageConfigForm equivalent for `transform` only shows an
          explanatory help block — we surface it inline above the
          editor for context.
        -->
        <StageConfigForm type={selected.kind} bind:config={selected.config} {schemas} />
        <div class="mt-3">
          <TransformListEditor bind:transforms compact />
        </div>
      {:else if selected.kind === 'route'}
        <!--
          Same pipeline-scope pattern as transform: rules live in shared
          state, edited from this panel, persisted whole. Destination
          nodes elsewhere on the canvas are visual only — the rule's
          destination_id points at a connection, not a node id.
        -->
        <StageConfigForm type={selected.kind} bind:config={selected.config} {schemas} />
        <div class="mt-3">
          <RoutingRuleListEditor bind:rules {connections} compact />
        </div>
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

  <!--
    Preview drawer — spans all 3 grid columns, lives in grid-row 2 when
    open. Collapsed by default; runPreview() also opens it. The status
    badge + close button stay on the right; the output fills the rest.
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

<!-- ─── Header (above the shell) ────────────────────────────────── -->
<div class="flow-header">
  <div>
    <h2 class="text-2xl font-semibold" style="color: var(--text)">
      {#if pipelineId}
        <span class="editing-tag">{t($locale, 'flow.editing')}</span>
        {pipelineName || t($locale, 'flow.title')}
      {:else}
        {t($locale, 'flow.title')}
      {/if}
    </h2>
    <p class="text-sm mt-1" style="color: var(--text-muted)">
      {#if loadingPipeline}
        {t($locale, 'flow.loading')}
      {:else}
        {t($locale, 'flow.help')}
      {/if}
    </p>
  </div>
  <div class="flex items-center gap-2">
    <Input bind:value={pipelineName} label={t($locale, 'flow.props.name')} placeholder="flow-1" />
    {#if pipelineId}
      <Button variant="ghost" on:click={() => goto('/flow')}>
        {t($locale, 'flow.actions.newFlow')}
      </Button>
    {/if}
    <Button variant="ghost" on:click={clearAll}>{t($locale, 'flow.actions.clear')}</Button>
    <Button variant="outline" on:click={runPreview} loading={previewing}>
      {t($locale, 'preview.run')}
    </Button>
    <Button on:click={saveAndDeploy} loading={saving}>{t($locale, 'flow.actions.save')}</Button>
  </div>
</div>

{#if error}
  <div class="mt-2"><Alert variant="error">{error}</Alert></div>
{/if}
{#if savedMsg}
  <div class="mt-2"><Alert variant="success">{savedMsg}</Alert></div>
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
  /*
   * Editing-mode prefix on the page title — small uppercase eyebrow
   * inline with the pipeline name. Uses --section-header (gold on dark,
   * dark-gold on light) so it reads as a Brand Guide §5.8 eyebrow, not
   * a CTA. Maroon is reserved for actual destructive/primary actions
   * per Rule 16 of brand-tokens.css.
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

  .flow-shell {
    display: grid;
    grid-template-columns: 240px 1fr 300px;
    grid-template-rows: 1fr;
    height: calc(100vh - 140px);
    min-height: 480px;
    background: var(--bg);
  }
  /*
   * When the preview drawer is open we split the shell into two rows so
   * the existing 3-col layout shrinks vertically and the drawer spans
   * the full width below it. The drawer height (260dp) is fixed so the
   * canvas keeps a predictable working area.
   */
  .flow-shell.preview-open {
    grid-template-rows: 1fr 260px;
  }
  .flow-preview {
    grid-column: 1 / -1;
    grid-row: 2;
    border-top: 1px solid var(--border);
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
    width: 100%;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    padding: 8px 10px;
    resize: none;
  }
  .flow-preview-output:focus { outline: 2px solid var(--primary); }

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
    background: var(--danger); color: var(--danger-on);
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
