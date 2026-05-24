<!--
  StudioCanvas — the SVG graph for the Pipeline Studio. The canvas reads
  the studio store (draft + selectedNodeId + state + dryRun) and writes
  back via `studio.addStage`, `studio.removeStage`, `studio.selectNode`.
  It does NOT receive the pipeline as a prop — that keeps the canvas in
  sync with the inspector, the version rail, and the dry-run dock
  without a prop-passing dance.

  Layout — horizontal flow, LTR even under RTL operators:

      Source ──► Stage 1 ──► Stage 2 ──► … ──► Destination

  Why LTR-only under RTL: the studio is a directed graph, and operators
  read graphs left-to-right regardless of script direction. Flipping the
  whole canvas under RTL would also invert every arrowhead, which would
  read as wrong. We mirror the inspector/palette layout (the SHELL is
  RTL-aware) but the canvas keeps its compass.

  Coordinates live inside a viewBox so the SVG scales to its container
  via preserveAspectRatio. Nodes have fixed positions inside the viewBox
  — we don't need world coordinates here because there's no zoom or pan.

  Drag + drop from palette uses the same `application/x-mqc-stage-type`
  mime as StudioPalette. Dropping anywhere on the canvas appends the
  stage (the canvas is a chain, not a free graph — drop position doesn't
  matter).

  Visual states (driven by `state` from the store):
    - empty            → overlay EmptyState ("Drop a stage to begin")
    - error            → ring the selected node in --danger
    - version-comparing → read-only, "comparing" hint shown on click
    - simulating       → dry-run dots/badges per stage (Task 11 fills the
                         dry-run shape — for Task 9 we render passive
                         overlays based on whatever the store carries)

  Task 9 / Wave 1.
-->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { studio, type StudioStateData, type StudioStageType } from '$lib/stores/studio';
  import { api, type Connection } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import EmptyState from '$lib/components/EmptyState.svelte';

  // Forward-compat prop — Task 11 will pass dry-run overlay data in. For
  // Task 9 we accept it but also fall back to `studio.dryRun` so the
  // store stays the single source of truth.
  export let dryRunOverlays: { stageId: string; failed: boolean; durationMs: number }[] = [];

  // mimeForDrag must match StudioPalette.STAGE_DRAG_MIME — both sides
  // of the drag contract live here so the canvas test can use the
  // string without depending on the palette being on screen.
  const STAGE_DRAG_MIME = 'application/x-mqc-stage-type';

  let s: StudioStateData;
  const unsub = studio.subscribe((v) => (s = v));
  onDestroy(unsub);

  // We resolve source + destination connection names from /v1/connections.
  // The studio store carries the connection ids only (Pipeline.source_id /
  // destination_id); the canvas needs the human label. We fetch on mount
  // and cache for the page lifetime. A failure is non-fatal — the canvas
  // falls back to the id.
  let connections: Connection[] = [];
  void (async () => {
    try {
      connections = (await api.get<Connection[]>('/v1/connections')) ?? [];
    } catch {
      connections = [];
    }
  })();

  // Look-up helpers. Both default to `null` so the template can fall
  // back to a friendlier "no source connection" string.
  $: sourceConn = (() => {
    const id = s?.draft?.pipeline?.source_id;
    if (!id) return null;
    return connections.find((c) => c.id === id) ?? null;
  })();
  $: destConn = (() => {
    const id = s?.draft?.pipeline?.destination_id;
    if (!id) return null;
    return connections.find((c) => c.id === id) ?? null;
  })();

  // ─── Layout maths ───────────────────────────────────────────────────
  //
  // We compute node positions in a single reactive block. The chain is
  // always laid out horizontally — source on the far left, stages in
  // order, destination(s) on the far right. Routes branch from the last
  // stage to additional destinations stacked vertically next to the
  // primary destination.
  //
  // viewBox coordinates: 1000 wide, height grows with the number of
  // routes to keep alt-destinations on screen. preserveAspectRatio in
  // the <svg> tag scales the whole thing to its container.

  const NODE_W = 140;
  const NODE_H = 60;
  const COL_GAP = 36; // spacing between successive columns
  const ROW_GAP = 24; // spacing between stacked destinations
  const ROW_BASE = 80; // top padding before the first row
  const COL_BASE = 24; // left padding before the source column

  type CanvasNode = {
    id: string;
    kind: 'source' | 'destination' | 'stage';
    label: string;
    sub: string;
    stageType?: StudioStageType;
    x: number;
    y: number;
    deletable: boolean;
  };

  type CanvasEdge = {
    from: string;
    to: string;
    routeLabel?: string;
  };

  // resolvedLayout: derived from store + connections. Each render
  // produces a fresh array of nodes + edges with computed positions.
  $: layout = (() => {
    const nodes: CanvasNode[] = [];
    const edges: CanvasEdge[] = [];

    if (!s?.draft) {
      return { nodes, edges, viewW: 1000, viewH: 240 };
    }

    const stages = s.draft.stages;
    const rules = s.draft.routingRules;

    // Source — always at column 0.
    const sourceId = s.draft.pipeline.source_id || 'source';
    const sourceLabel =
      sourceConn?.name ?? (sourceId ? sourceId : t($locale, 'studio.canvas.noSource'));
    const sourceSub = sourceConn?.type ?? '';
    nodes.push({
      id: `source-${sourceId}`,
      kind: 'source',
      label: t($locale, 'studio.canvas.source'),
      sub: sourceLabel + (sourceSub ? ` · ${sourceSub}` : ''),
      x: COL_BASE,
      y: ROW_BASE,
      deletable: false
    });

    // Stages in order — columns 1..N.
    let lastNodeId = `source-${sourceId}`;
    let col = 1;
    for (const st of stages) {
      const id = st.id ?? `stage-${st.stage_order}`;
      nodes.push({
        id,
        kind: 'stage',
        label: st.stage_type,
        sub: st.enabled ? '' : 'disabled',
        stageType: st.stage_type as StudioStageType,
        x: COL_BASE + col * (NODE_W + COL_GAP),
        y: ROW_BASE,
        deletable: true
      });
      edges.push({ from: lastNodeId, to: id });
      lastNodeId = id;
      col++;
    }

    // Destination — column N+1.
    const destId = s.draft.pipeline.destination_id || 'destination';
    const destLabel =
      destConn?.name ?? (destId ? destId : t($locale, 'studio.canvas.noDestination'));
    const destSub = destConn?.type ?? '';
    const destNodeId = `dest-${destId}`;
    nodes.push({
      id: destNodeId,
      kind: 'destination',
      label: t($locale, 'studio.canvas.destination'),
      sub: destLabel + (destSub ? ` · ${destSub}` : ''),
      x: COL_BASE + col * (NODE_W + COL_GAP),
      y: ROW_BASE,
      deletable: false
    });
    edges.push({ from: lastNodeId, to: destNodeId });

    // Routing rules — alternate destinations branch from the last stage
    // (lastNodeId) down to stacked destination nodes. Each unique
    // destination_id gets one extra node below the primary dest.
    const seenDestIds = new Set([destId]);
    let altRow = 1;
    for (const rule of rules) {
      if (!rule.destination_id) continue;
      if (seenDestIds.has(rule.destination_id)) continue;
      seenDestIds.add(rule.destination_id);
      const conn = connections.find((c) => c.id === rule.destination_id) ?? null;
      const label = conn?.name ?? rule.destination_id;
      const id = `route-dest-${rule.destination_id}`;
      nodes.push({
        id,
        kind: 'destination',
        label: t($locale, 'studio.canvas.destination'),
        sub: label + (conn?.type ? ` · ${conn.type}` : ''),
        x: COL_BASE + col * (NODE_W + COL_GAP),
        y: ROW_BASE + altRow * (NODE_H + ROW_GAP),
        deletable: false
      });
      edges.push({
        from: lastNodeId,
        to: id,
        routeLabel: t($locale, 'studio.canvas.routeBadge')
      });
      altRow++;
    }

    const viewW = COL_BASE * 2 + (col + 1) * (NODE_W + COL_GAP);
    const viewH = ROW_BASE + (altRow + 1) * (NODE_H + ROW_GAP);
    return { nodes, edges, viewW, viewH };
  })();

  // ─── Overlay data ──────────────────────────────────────────────────
  //
  // Task 11 will hand us real dry-run results. For Task 9, accept
  // whatever the store carries (passive). The store's `dryRun` shape is
  // `unknown` so we narrow defensively — anything we don't recognise
  // gets ignored without throwing.
  type Overlay = { stageId: string; failed: boolean; durationMs: number };
  $: overlays = (() => {
    if (dryRunOverlays.length > 0) return dryRunOverlays;
    const dry = s?.dryRun as { stages?: Overlay[] } | null;
    return Array.isArray(dry?.stages) ? (dry?.stages as Overlay[]) : [];
  })();

  function overlayFor(stageId: string): Overlay | null {
    return overlays.find((o) => o.stageId === stageId) ?? null;
  }

  // ─── Interactions ───────────────────────────────────────────────────

  $: readOnly = s?.state === 'version-comparing';

  function onNodeClick(node: CanvasNode) {
    if (readOnly) return;
    studio.selectNode(node.id);
  }

  function onNodeKeyDown(e: KeyboardEvent, node: CanvasNode) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onNodeClick(node);
    } else if ((e.key === 'Delete' || e.key === 'Backspace') && node.deletable) {
      e.preventDefault();
      if (!readOnly) studio.removeStage(node.id);
    }
  }

  function onRemoveClick(e: Event, node: CanvasNode) {
    e.stopPropagation();
    if (readOnly) return;
    if (!node.deletable) return;
    studio.removeStage(node.id);
  }

  function onNodeContextMenu(e: MouseEvent, node: CanvasNode) {
    if (!node.deletable || readOnly) return;
    e.preventDefault();
    studio.removeStage(node.id);
  }

  function onCanvasDragOver(e: DragEvent) {
    if (readOnly) return;
    // We need to call preventDefault on dragover for the drop event to
    // fire — standard HTML5 drag-and-drop quirk.
    if (e.dataTransfer?.types?.includes(STAGE_DRAG_MIME)) {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'copy';
    }
  }

  function onCanvasDrop(e: DragEvent) {
    if (readOnly) return;
    const stageType = e.dataTransfer?.getData(STAGE_DRAG_MIME) as StudioStageType | '';
    if (!stageType) return;
    e.preventDefault();
    const newId = studio.addStage(stageType);
    studio.selectNode(newId);
  }

  // edgePath produces a smooth cubic between two node centres on the
  // right-of-from-node and left-of-to-node ports. Branches downward also
  // look good with this single helper.
  function edgePath(from: CanvasNode, to: CanvasNode): string {
    const ax = from.x + NODE_W;
    const ay = from.y + NODE_H / 2;
    const bx = to.x;
    const by = to.y + NODE_H / 2;
    const cx = (ax + bx) / 2;
    return `M${ax},${ay} C${cx},${ay} ${cx},${by} ${bx},${by}`;
  }

  // Look up a node by id for the edge-render loop. Pre-computing in a
  // map keeps the {#each} O(n).
  $: nodesById = new Map(layout.nodes.map((n) => [n.id, n]));

  // Whether the canvas should render the empty-state overlay. We treat
  // "no stages" as empty even after hydrate; the source + destination
  // nodes still render so the operator sees the shape they're filling.
  $: showEmpty = (s?.draft?.stages.length ?? 0) === 0;

  // The chip shown when an operator tries to interact while comparing.
  let comparingHint = false;
  function onCanvasClick() {
    if (!readOnly) return;
    comparingHint = true;
    setTimeout(() => (comparingHint = false), 1500);
  }
</script>

<!--
  The canvas is a graph editor — clicks land on either a node (selects)
  or the empty background (shows the comparing hint in read-only mode).
  Keyboard equivalents live on the nodes themselves; the background
  click is a non-essential affordance, so we suppress the
  a11y_click_events_have_key_events warning at this single boundary.
-->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
  class="studio-canvas"
  data-state={s?.state ?? 'empty'}
  data-readonly={readOnly ? 'true' : 'false'}
  on:dragover={onCanvasDragOver}
  on:drop={onCanvasDrop}
  on:click={onCanvasClick}
  role="application"
  aria-label="Pipeline canvas"
>
  <svg
    class="studio-canvas-svg"
    viewBox="0 0 {layout.viewW} {layout.viewH}"
    preserveAspectRatio="xMidYMid meet"
    role="presentation"
  >
    <defs>
      <marker
        id="studio-edge-arrow"
        viewBox="0 0 8 8"
        refX="6"
        refY="4"
        markerWidth="6"
        markerHeight="6"
        orient="auto-start-reverse"
      >
        <path d="M0,0 L8,4 L0,8 z" fill="var(--text-tertiary)" />
      </marker>
    </defs>

    {#each layout.edges as edge (edge.from + '→' + edge.to)}
      {@const from = nodesById.get(edge.from)}
      {@const to = nodesById.get(edge.to)}
      {#if from && to}
        <path
          d={edgePath(from, to)}
          class="studio-canvas-edge"
          marker-end="url(#studio-edge-arrow)"
        />
        {#if edge.routeLabel}
          <!-- Route badge at the midpoint of the edge. -->
          {@const mx = (from.x + NODE_W + to.x) / 2}
          {@const my = (from.y + NODE_H / 2 + (to.y + NODE_H / 2)) / 2}
          <g class="studio-canvas-route-badge" transform="translate({mx},{my})">
            <rect x="-22" y="-9" width="44" height="18" rx="9" />
            <text x="0" y="3" text-anchor="middle">{edge.routeLabel}</text>
          </g>
        {/if}
      {/if}
    {/each}

    {#each layout.nodes as node (node.id)}
      {@const isSelected = s?.selectedNodeId === node.id}
      {@const isError = s?.state === 'error' && isSelected}
      {@const overlay = overlayFor(node.id)}
      <g
        class="studio-canvas-node"
        class:is-selected={isSelected}
        class:is-error={isError}
        class:is-source={node.kind === 'source'}
        class:is-destination={node.kind === 'destination'}
        class:is-stage={node.kind === 'stage'}
        data-node-id={node.id}
        data-node-kind={node.kind}
        transform="translate({node.x},{node.y})"
        tabindex="0"
        role="button"
        aria-label={node.label}
        aria-pressed={isSelected ? 'true' : 'false'}
        on:click|stopPropagation={() => onNodeClick(node)}
        on:keydown={(e) => onNodeKeyDown(e, node)}
        on:contextmenu={(e) => onNodeContextMenu(e, node)}
      >
        <rect class="studio-canvas-node-bg" width={NODE_W} height={NODE_H} rx="10" />
        <text x="14" y="22" class="studio-canvas-node-label">{node.label}</text>
        {#if node.sub}
          <text x="14" y="42" class="studio-canvas-node-sub">{node.sub}</text>
        {/if}

        {#if node.deletable && isSelected && !readOnly}
          <g
            class="studio-canvas-node-remove"
            transform="translate({NODE_W - 18},{8})"
            tabindex="0"
            role="button"
            aria-label={t($locale, 'studio.canvas.removeStage')}
            on:click={(e) => onRemoveClick(e, node)}
            on:keydown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                onRemoveClick(e, node);
              }
            }}
          >
            <circle r="9" />
            <path d="M-4,-4 L4,4 M4,-4 L-4,4" />
          </g>
        {/if}

        {#if overlay}
          <!-- Dry-run overlay — small dot + ms badge. Task 11 will give
               this its proper visual; Task 9 just plumbs the data. -->
          <circle
            cx={NODE_W - 12}
            cy={NODE_H - 12}
            r="5"
            class:overlay-failed={overlay.failed}
            class:overlay-ok={!overlay.failed}
            class="studio-canvas-overlay-dot"
          />
          <text x={NODE_W - 24} y={NODE_H - 6} text-anchor="end" class="studio-canvas-overlay-ms">
            {overlay.durationMs}ms
          </text>
        {/if}
      </g>
    {/each}
  </svg>

  {#if showEmpty}
    <div class="studio-canvas-empty">
      <EmptyState
        illustration="pipelines"
        title={t($locale, 'studio.canvas.empty.title')}
        body={t($locale, 'studio.canvas.empty.body')}
      />
    </div>
  {/if}

  {#if comparingHint}
    <div class="studio-canvas-readonly-hint" role="status">
      {t($locale, 'studio.canvas.readOnly')}
    </div>
  {/if}
</div>

<style>
  /*
   * Canvas container fills the .studio-canvas slot in Studio.svelte and
   * resizes with it. The inner SVG uses viewBox so the graph stays
   * legible across container sizes without us doing any zoom maths.
   */
  .studio-canvas {
    position: relative;
    inline-size: 100%;
    block-size: 100%;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    overflow: hidden;
    /* Subtle dotted grid to give the canvas spatial cues. The dots are
       1×1 with var(--border-strong) so they read identically in light
       + dark themes. */
    background-image: radial-gradient(circle, var(--border) 1px, transparent 1px);
    background-size: 16px 16px;
  }
  .studio-canvas[data-readonly='true'] {
    cursor: not-allowed;
  }
  .studio-canvas-svg {
    inline-size: 100%;
    block-size: 100%;
    display: block;
  }

  /* Edges — thin slate stroke; selected node's incoming/outgoing
     edges keep the same colour. Arrowheads inherit fill via the marker
     definition above (--text-tertiary). */
  .studio-canvas-edge {
    fill: none;
    stroke: var(--text-tertiary);
    stroke-width: 1.5;
  }

  /* Route badge — a small pill on the edge midpoint. */
  .studio-canvas-route-badge rect {
    fill: var(--surface-2);
    stroke: var(--border);
    stroke-width: 1;
  }
  .studio-canvas-route-badge text {
    fill: var(--text-muted);
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  /* Nodes — common card affordance. Source + destination get a slight
     tint via the brand secondary/primary tokens; stages stay neutral
     so the chain reads as "two endpoints + stages between them". */
  .studio-canvas-node {
    cursor: pointer;
    outline: none;
  }
  .studio-canvas-node-bg {
    fill: var(--surface-2);
    stroke: var(--border);
    stroke-width: 1;
  }
  .studio-canvas-node.is-source .studio-canvas-node-bg {
    stroke: var(--primary);
  }
  .studio-canvas-node.is-destination .studio-canvas-node-bg {
    stroke: var(--primary);
  }
  .studio-canvas-node.is-selected .studio-canvas-node-bg {
    stroke: var(--accent);
    stroke-width: 2;
  }
  .studio-canvas-node.is-error .studio-canvas-node-bg {
    stroke: var(--danger);
    stroke-width: 2;
  }
  .studio-canvas-node:focus-visible .studio-canvas-node-bg {
    stroke: var(--accent);
    stroke-width: 2;
  }
  .studio-canvas-node-label {
    fill: var(--text);
    font-size: 12px;
    font-weight: 600;
    text-transform: capitalize;
  }
  .studio-canvas-node-sub {
    fill: var(--text-muted);
    font-size: 10px;
  }

  /* Remove (X) button on selected stage nodes. */
  .studio-canvas-node-remove {
    cursor: pointer;
    outline: none;
  }
  .studio-canvas-node-remove circle {
    fill: var(--surface-high);
    stroke: var(--border);
    stroke-width: 1;
  }
  .studio-canvas-node-remove path {
    stroke: var(--text);
    stroke-width: 1.5;
    stroke-linecap: round;
  }
  .studio-canvas-node-remove:hover circle,
  .studio-canvas-node-remove:focus-visible circle {
    fill: var(--danger);
  }
  .studio-canvas-node-remove:hover path,
  .studio-canvas-node-remove:focus-visible path {
    stroke: var(--danger-on);
  }

  /* Dry-run overlays (Task 11 will polish; Task 9 plumbs only). */
  .studio-canvas-overlay-dot.overlay-ok {
    fill: var(--success-solid);
  }
  .studio-canvas-overlay-dot.overlay-failed {
    fill: var(--danger);
  }
  .studio-canvas-overlay-ms {
    fill: var(--text-tertiary);
    font-size: 9px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
  }

  /* Empty state — overlay centred above the SVG so the operator sees a
     prompt the moment there are no stages, but the source + dest still
     show in the background as scaffolding. */
  .studio-canvas-empty {
    position: absolute;
    inset-inline-start: 50%;
    inset-block-start: 50%;
    transform: translate(-50%, -50%);
    background: color-mix(in srgb, var(--surface) 92%, transparent);
    border-radius: 12px;
    padding: 0.5rem 1rem;
    pointer-events: none;
  }

  .studio-canvas-readonly-hint {
    position: absolute;
    inset-block-start: 0.75rem;
    inset-inline-end: 0.75rem;
    padding: 0.375rem 0.625rem;
    background: var(--surface-highest);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 8px;
    font-size: 0.75rem;
    pointer-events: none;
  }

  /* Error state — extra danger tint on the canvas frame so the
     operator notices something's wrong even without focusing a node. */
  .studio-canvas[data-state='error'] {
    border-color: var(--danger);
  }
</style>
