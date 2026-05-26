<!--
  TopologyGraph — the headline visual for the /topology page.

  Renders one SVG node per broker connection and one SVG edge per
  pipeline between source and destination connections. Layout is a
  small force-directed simulator (see $lib/charts/force.ts) — no third-
  party graph library, no d3-force, no cytoscape.

  Anatomy:
    <svg>
      <defs>                  drop-shadow + per-tone arrow markers
      <g.edges>               edges first so they sit behind nodes
        <path.hit/>           invisible thick path for easier hit-test
        <path.topo-edge/>     visible stroke, tinted by circuit state
        <text/>               pipeline name pill (hover only)
      <g.nodes>
        <g.topo-node/>        circle + glyph + name + online dot

  Interaction:
    • click a node → emits `select` { kind: 'connection', id }
    • click an edge → emits `select` { kind: 'pipeline', id }
    • click the empty SVG background → emits `clear`
    • drag a node by pointer → pins it under the pointer; on release,
      the sim takes over again

  Animation:
    • on mount: 30 settle iterations synchronously, then 1 step per rAF
      for ~2 s so the graph visibly relaxes into place
    • prefers-reduced-motion: all iterations synchronous, no rAF
    • open-circuit edges get marching-ants via CSS keyframes (also
      respects prefers-reduced-motion)

  Empty / loading:
    • topology === null               → centred pulsing skeleton dot
    • connections.length === 0        → centred EmptyState message
-->
<script lang="ts">
  import { onMount, onDestroy, createEventDispatcher, tick } from 'svelte';
  import type { TopologyResponse, CircuitState } from '$lib/api';
  import ConnectionTypeIcon from '$lib/components/ConnectionTypeIcon.svelte';
  import { createSimulation, type Simulation } from '$lib/charts/force';

  export let topology: TopologyResponse | null = null;
  /** Currently-selected entity id — two-way bound from parent. */
  export let selectedId = '';

  const dispatch = createEventDispatcher<{
    select: { kind: 'connection' | 'pipeline'; id: string };
    clear: void;
  }>();

  // Hovered edge id so we can pop the name pill at midpoint without
  // re-running layout. Empty string = nothing hovered.
  let hoveredEdgeId = '';

  // Container sizing — driven by ResizeObserver so a parent column
  // resize re-runs layout without a page reload.
  let containerEl: HTMLDivElement;
  let svgEl: SVGSVGElement;
  let width = 800;
  let height = 600;

  // Reduce-motion detection so settle can skip rAF and run synchronously.
  let reduceMotion = false;
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  }

  // The simulator itself. Recreated on first mount once we know the
  // container's real dimensions (so the seed positions cluster at the
  // true centre, not the placeholder).
  let sim: Simulation | null = null;
  // Version counter bumped each time sim.step() advances; Svelte's
  // reactivity walks this so the template re-renders new node positions
  // without us having to clone the node array.
  let tick$ = 0;
  let frame: number | undefined;
  let animUntil = 0;

  // Drag state. We only track one pointer at a time — multi-touch falls
  // through to the browser's native pinch-zoom on the parent surface.
  // movedPx accumulates absolute pointer movement so the up handler can
  // distinguish a tap (movedPx ~= 0) from a drop (movedPx > threshold).
  let drag: {
    pointerId: number;
    nodeId: string;
    offsetX: number;
    offsetY: number;
    movedPx: number;
    lastX: number;
    lastY: number;
  } | null = null;
  const CLICK_THRESHOLD_PX = 3;

  // Resize observer reference so we can disconnect on destroy.
  let resizeObs: ResizeObserver | null = null;

  // Derived: (re-)build sim nodes + edges whenever the topology changes.
  // We diff-and-merge so previously-laid-out positions are preserved
  // when a single pipeline changes — the whole graph doesn't reflow.
  $: if (sim && topology) {
    sim.setNodes(topology.connections.map((c) => ({ id: c.id })));
    sim.setEdges(
      topology.pipelines
        // Skip pipelines whose source/destination aren't in the connection
        // list — happens briefly during a connection delete. The simulator
        // also defends against this, but skipping here keeps the renderer
        // tidy.
        .filter(
          (p) =>
            topology!.connections.some((c) => c.id === p.source_id) &&
            topology!.connections.some((c) => c.id === p.destination_id)
        )
        .map((p) => ({ id: p.id, source: p.source_id, target: p.destination_id }))
    );
    kickAnimation();
  }

  function kickAnimation(): void {
    if (!sim) return;
    // Run a chunk synchronously first so the visible layout isn't a
    // flock of nodes flying apart from the centre — they land roughly
    // where they belong, then the rAF loop polishes for 2 s.
    sim.settle(30);
    tick$++;
    if (reduceMotion) {
      sim.settle(40); // one more burst to fully converge
      tick$++;
      return;
    }
    animUntil = performance.now() + 2000;
    if (frame === undefined) {
      frame = requestAnimationFrame(loop);
    }
  }

  function loop(t: number): void {
    if (!sim) return;
    sim.step();
    tick$++;
    if (t < animUntil || drag) {
      frame = requestAnimationFrame(loop);
    } else {
      frame = undefined;
    }
  }

  function stopAnimation(): void {
    if (frame !== undefined) {
      cancelAnimationFrame(frame);
      frame = undefined;
    }
  }

  // Edge stroke-width mapping. Log-scaled per the spec — 1 msg/min ≈ 1px,
  // 100 ≈ ~3.4px, 10k ≈ ~5.8px, capped at 5px.
  function edgeStrokeWidth(mpm: number): number {
    if (mpm <= 0) return 1;
    return Math.min(5, 1 + Math.log10(Math.max(1, mpm)) * 1.2);
  }

  // Map circuit_state + pipeline.status to one of four CSS tones the
  // stylesheet below knows how to colour. Returning a single string
  // keeps the template clean — `data-tone={t}` drives both stroke and
  // arrowhead via the marker map.
  function edgeTone(circuit: CircuitState, status: string, enabled: boolean): string {
    if (circuit === 'open') return 'danger';
    if (circuit === 'half-open') return 'warning';
    if (!enabled || status === 'disabled') return 'dim';
    if (status === 'error') return 'danger';
    if (status === 'idle') return 'idle';
    if (circuit === 'unknown' && status === 'connected') return 'healthy';
    return 'healthy';
  }

  // Pointer / interaction handlers -----------------------------------

  function handleSvgClick(ev: MouseEvent): void {
    // Only fire the clear event if the click landed on the SVG itself
    // (background) — clicks on a node/edge stopPropagation so they
    // never reach here.
    if (ev.target === svgEl) {
      selectedId = '';
      dispatch('clear');
    }
  }

  function handleNodePointerDown(ev: PointerEvent, nodeId: string): void {
    if (!sim) return;
    ev.stopPropagation();
    const node = sim.nodes.find((n) => n.id === nodeId);
    if (!node) return;
    // Capture so subsequent move/up events come to us even when the
    // pointer leaves the node bounds (a real drag). jsdom doesn't
    // implement setPointerCapture — guard so unit tests can still
    // exercise the drag/click code path.
    const target = ev.currentTarget as Element & {
      setPointerCapture?: (pointerId: number) => void;
    };
    if (typeof target.setPointerCapture === 'function') {
      target.setPointerCapture(ev.pointerId);
    }
    const point = svgPoint(ev);
    drag = {
      pointerId: ev.pointerId,
      nodeId,
      offsetX: point.x - node.x,
      offsetY: point.y - node.y,
      movedPx: 0,
      lastX: point.x,
      lastY: point.y
    };
    sim.pin(nodeId, node.x, node.y);
    // Run the loop while dragging so the rest of the graph reacts to
    // the pinned position in real time.
    animUntil = performance.now() + 1_000_000;
    if (frame === undefined && !reduceMotion) {
      frame = requestAnimationFrame(loop);
    }
  }

  function handleNodePointerMove(ev: PointerEvent): void {
    if (!sim || !drag || ev.pointerId !== drag.pointerId) return;
    const p = svgPoint(ev);
    drag.movedPx += Math.abs(p.x - drag.lastX) + Math.abs(p.y - drag.lastY);
    drag.lastX = p.x;
    drag.lastY = p.y;
    sim.pin(drag.nodeId, p.x - drag.offsetX, p.y - drag.offsetY);
    tick$++;
  }

  function handleNodePointerUp(ev: PointerEvent, nodeId: string): void {
    if (!sim) return;
    const matched = drag && drag.pointerId === ev.pointerId;
    const moved = matched ? drag!.movedPx : 0;
    if (matched) {
      sim.unpin(drag!.nodeId);
      drag = null;
      // Let the system settle after a drop. Two seconds matches the
      // first-mount animation budget.
      animUntil = performance.now() + 2000;
      if (frame === undefined && !reduceMotion) {
        frame = requestAnimationFrame(loop);
      } else if (reduceMotion) {
        sim.settle(20);
        tick$++;
      }
    }
    // Treat anything under the threshold as a click — including the
    // common tap-with-no-move on touch and a no-op mouse click. A real
    // drag-and-drop (cumulative movement > threshold) does NOT fire
    // select; the operator's intent there was to reposition, not pick.
    if (!matched || moved < CLICK_THRESHOLD_PX) {
      selectedId = nodeId;
      dispatch('select', { kind: 'connection', id: nodeId });
    }
  }

  // Convert a pointer event into the SVG's user-space coordinate. Uses
  // the SVGGraphicsElement → DOMMatrix path because the simulator works
  // in unit-less SVG coordinates, not screen pixels.
  //
  // jsdom doesn't implement createSVGPoint / getScreenCTM; falling back
  // to the raw client coordinates is fine for unit tests (the click
  // assertion only cares that the event reached us, not the resolved
  // SVG coords).
  function svgPoint(ev: PointerEvent): { x: number; y: number } {
    if (!svgEl || typeof svgEl.createSVGPoint !== 'function') {
      return { x: ev.clientX, y: ev.clientY };
    }
    const pt = svgEl.createSVGPoint();
    pt.x = ev.clientX;
    pt.y = ev.clientY;
    const ctm = typeof svgEl.getScreenCTM === 'function' ? svgEl.getScreenCTM() : null;
    if (!ctm) return { x: ev.clientX, y: ev.clientY };
    const local = pt.matrixTransform(ctm.inverse());
    return { x: local.x, y: local.y };
  }

  function handleEdgeClick(ev: MouseEvent, pipelineId: string): void {
    ev.stopPropagation();
    selectedId = pipelineId;
    dispatch('select', { kind: 'pipeline', id: pipelineId });
  }

  // Lifecycle --------------------------------------------------------

  onMount(async () => {
    // Wait one tick so the container has its real bounding rect (avoids
    // the layout running against the 800x600 placeholder size).
    await tick();
    if (containerEl) {
      const rect = containerEl.getBoundingClientRect();
      if (rect.width > 0) width = Math.round(rect.width);
      if (rect.height > 0) height = Math.round(rect.height);
    }
    sim = createSimulation({ width, height });
    if (topology) {
      sim.setNodes(topology.connections.map((c) => ({ id: c.id })));
      sim.setEdges(
        topology.pipelines
          .filter(
            (p) =>
              topology!.connections.some((c) => c.id === p.source_id) &&
              topology!.connections.some((c) => c.id === p.destination_id)
          )
          .map((p) => ({ id: p.id, source: p.source_id, target: p.destination_id }))
      );
      kickAnimation();
    }
    // Watch the container — column drag / nav collapse will fire.
    if (typeof ResizeObserver !== 'undefined' && containerEl) {
      resizeObs = new ResizeObserver((entries) => {
        for (const e of entries) {
          const cr = e.contentRect;
          if (cr.width > 0 && cr.height > 0) {
            width = Math.round(cr.width);
            height = Math.round(cr.height);
            if (sim) {
              sim.bounds(width, height);
              kickAnimation();
            }
          }
        }
      });
      resizeObs.observe(containerEl);
    }
  });

  onDestroy(() => {
    stopAnimation();
    if (resizeObs) {
      resizeObs.disconnect();
      resizeObs = null;
    }
  });

  // Re-derive lookups whenever inputs change. Keeps the template free
  // of repeated .find() calls inside #each blocks.
  $: connById = new Map(topology?.connections.map((c) => [c.id, c]) ?? []);
  $: pipelineById = new Map(topology?.pipelines.map((p) => [p.id, p]) ?? []);

  // Highlight set: when a pipeline edge is selected, both endpoint
  // nodes pick up the secondary "endpoint" ring. When a connection is
  // selected, no edge gets highlighted (the selected ring lives on the
  // node itself).
  $: highlightedNodes = (() => {
    const s = new Set<string>();
    if (selectedId && pipelineById.has(selectedId)) {
      const p = pipelineById.get(selectedId)!;
      s.add(p.source_id);
      s.add(p.destination_id);
    }
    return s;
  })();
</script>

<div bind:this={containerEl} class="topo-wrap">
  {#if topology === null}
    <div class="topo-loading" role="status" aria-live="polite" aria-label="Loading topology">
      <span class="topo-pulse" aria-hidden="true"></span>
    </div>
  {:else if topology.connections.length === 0}
    <div class="topo-empty">
      <p class="topo-empty-title">No brokers yet</p>
      <p class="topo-empty-body">
        Add a connection to start visualising the flow between brokers.
      </p>
    </div>
  {:else}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <svg
      bind:this={svgEl}
      class="topo-svg"
      viewBox="0 0 {width} {height}"
      preserveAspectRatio="xMidYMid meet"
      role="img"
      aria-label="Live topology of brokers and pipelines"
      on:click={handleSvgClick}
    >
      <defs>
        <!-- Soft node shadow — one filter shared by every node. -->
        <filter id="topo-node-shadow" x="-30%" y="-30%" width="160%" height="160%">
          <feGaussianBlur in="SourceAlpha" stdDeviation="2.5" />
          <feOffset dx="0" dy="1.5" result="off" />
          <feComponentTransfer><feFuncA type="linear" slope="0.45" /></feComponentTransfer>
          <feMerge>
            <feMergeNode />
            <feMergeNode in="SourceGraphic" />
          </feMerge>
        </filter>

        <!-- Arrow markers — one per stroke tone so the head matches the
             body without inheriting via currentColor (markers don't get
             reliable currentColor inheritance across all browsers when
             referenced from a <path stroke="…">). -->
        <marker id="topo-arrow-healthy" viewBox="0 0 10 10" refX="9" refY="5"
                markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" class="topo-arrow topo-arrow-healthy" />
        </marker>
        <marker id="topo-arrow-warning" viewBox="0 0 10 10" refX="9" refY="5"
                markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" class="topo-arrow topo-arrow-warning" />
        </marker>
        <marker id="topo-arrow-danger" viewBox="0 0 10 10" refX="9" refY="5"
                markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" class="topo-arrow topo-arrow-danger" />
        </marker>
        <marker id="topo-arrow-dim" viewBox="0 0 10 10" refX="9" refY="5"
                markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" class="topo-arrow topo-arrow-dim" />
        </marker>
        <marker id="topo-arrow-idle" viewBox="0 0 10 10" refX="9" refY="5"
                markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" class="topo-arrow topo-arrow-idle" />
        </marker>
      </defs>

      {#if sim}
        {#key tick$}
          <!-- Edges first so nodes sit on top. -->
          <g class="topo-edges">
            {#each topology.pipelines as p (p.id)}
              {@const src = sim.nodes.find((n) => n.id === p.source_id)}
              {@const dst = sim.nodes.find((n) => n.id === p.destination_id)}
              {#if src && dst}
                {@const tone = edgeTone(p.circuit_state, p.status, p.enabled)}
                {@const sw = edgeStrokeWidth(p.msg_per_min)}
                {@const mx = (src.x + dst.x) / 2}
                {@const my = (src.y + dst.y) / 2}
                <g class="topo-edge-group" data-edge-id={p.id}>
                  <!-- Invisible hit-test path — fat stroke, easier to click. -->
                  <!-- svelte-ignore a11y_click_events_have_key_events -->
                  <!-- svelte-ignore a11y_no_static_element_interactions -->
                  <path
                    class="topo-edge-hit"
                    role="button"
                    tabindex="0"
                    aria-label="Pipeline {p.name}"
                    d="M{src.x},{src.y} L{dst.x},{dst.y}"
                    on:click={(e) => handleEdgeClick(e, p.id)}
                    on:keydown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        selectedId = p.id;
                        dispatch('select', { kind: 'pipeline', id: p.id });
                      }
                    }}
                    on:pointerenter={() => (hoveredEdgeId = p.id)}
                    on:pointerleave={() => {
                      if (hoveredEdgeId === p.id) hoveredEdgeId = '';
                    }}
                  ></path>
                  <!-- Visible stroke. -->
                  <path
                    class="topo-edge"
                    data-tone={tone}
                    data-circuit={p.circuit_state}
                    data-selected={selectedId === p.id ? 'true' : 'false'}
                    d="M{src.x},{src.y} L{dst.x},{dst.y}"
                    stroke-width={sw}
                    marker-end="url(#topo-arrow-{tone})"
                  ></path>
                  <!-- Name pill on hover. -->
                  {#if hoveredEdgeId === p.id || selectedId === p.id}
                    <g class="topo-edge-label" transform="translate({mx},{my})">
                      <rect
                        x={-(p.name.length * 3.5 + 8)}
                        y="-9"
                        width={p.name.length * 7 + 16}
                        height="18"
                        rx="9"
                      ></rect>
                      <text text-anchor="middle" dy="3.5">{p.name}</text>
                    </g>
                  {/if}
                </g>
              {/if}
            {/each}
          </g>

          <!-- Nodes -->
          <g class="topo-nodes">
            {#each sim.nodes as node (node.id)}
              {@const c = connById.get(node.id)}
              {#if c}
                {@const isSelected = selectedId === node.id}
                {@const isEndpoint = highlightedNodes.has(node.id)}
                <g
                  class="topo-node"
                  data-selected={isSelected ? 'true' : 'false'}
                  data-endpoint={isEndpoint ? 'true' : 'false'}
                  data-connected={c.connected ? 'true' : 'false'}
                  transform="translate({node.x},{node.y})"
                  on:pointerdown={(e) => handleNodePointerDown(e, node.id)}
                  on:pointermove={handleNodePointerMove}
                  on:pointerup={(e) => handleNodePointerUp(e, node.id)}
                  role="button"
                  tabindex="0"
                  aria-label="Broker {c.name}"
                  on:keydown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault();
                      selectedId = node.id;
                      dispatch('select', { kind: 'connection', id: node.id });
                    }
                  }}
                >
                  <circle class="topo-node-bg" r="28" filter="url(#topo-node-shadow)"></circle>
                  <g class="topo-node-icon"><ConnectionTypeIcon type={c.type} size={20} /></g>
                  <circle class="topo-node-dot" cx="19" cy="-19" r="4"></circle>
                  <text class="topo-node-label" text-anchor="middle" dy="46">{c.name}</text>
                </g>
              {/if}
            {/each}
          </g>
        {/key}
      {/if}
    </svg>
  {/if}
</div>

<style>
  .topo-wrap {
    position: relative;
    inline-size: 100%;
    block-size: 100%;
    min-block-size: 360px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 16px;
    overflow: hidden;
  }

  /* SVG fills the wrap so the simulator + ResizeObserver share one
     authoritative size. */
  .topo-svg {
    display: block;
    inline-size: 100%;
    block-size: 100%;
    cursor: default;
    touch-action: none;
  }

  /* ── Empty / loading ──────────────────────────────────────────── */
  .topo-loading {
    display: flex;
    align-items: center;
    justify-content: center;
    inline-size: 100%;
    block-size: 100%;
    min-block-size: 360px;
  }
  .topo-pulse {
    inline-size: 18px;
    block-size: 18px;
    border-radius: 999px;
    background: var(--primary);
    opacity: 0.6;
  }
  @media (prefers-reduced-motion: no-preference) {
    .topo-pulse { animation: topo-pulse 1.4s ease-in-out infinite; }
  }
  @keyframes topo-pulse {
    0%, 100% { transform: scale(1); opacity: 0.4; }
    50%      { transform: scale(1.4); opacity: 0.9; }
  }

  .topo-empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    text-align: center;
    inline-size: 100%;
    block-size: 100%;
    min-block-size: 360px;
    padding-inline: 1.5rem;
    color: var(--text-muted);
  }
  .topo-empty-title {
    color: var(--text);
    font-weight: 600;
    margin: 0 0 0.25rem;
  }
  .topo-empty-body {
    margin: 0;
    max-inline-size: 32rem;
    font-size: 0.875rem;
    line-height: 1.55;
  }

  /* ── Nodes ────────────────────────────────────────────────────── */
  .topo-node {
    cursor: grab;
    transition: filter 200ms;
  }
  .topo-node:active { cursor: grabbing; }
  .topo-node:focus { outline: none; }
  .topo-node:focus-visible .topo-node-bg {
    stroke: var(--primary);
    stroke-width: 2;
  }

  .topo-node-bg {
    fill: var(--surface-2);
    stroke: var(--border);
    stroke-width: 1;
    transition: stroke 200ms, stroke-width 200ms;
  }
  .topo-node[data-selected='true'] .topo-node-bg {
    stroke: var(--primary);
    stroke-width: 2.5;
  }
  .topo-node[data-endpoint='true']:not([data-selected='true']) .topo-node-bg {
    stroke: var(--primary);
    stroke-width: 1.5;
  }

  /* Centre the icon glyph inside the node circle. The ConnectionTypeIcon
     ships as a 16-or-larger SVG anchored at 0,0; offset by half its
     size so the visual centre lines up with the circle centre. */
  .topo-node-icon {
    color: var(--text);
    transform: translate(-10px, -10px);
    pointer-events: none;
  }

  /* "Online" dot — green when connected, dim grey otherwise. */
  .topo-node-dot {
    fill: var(--text-tertiary);
    stroke: var(--surface-2);
    stroke-width: 1.5;
  }
  .topo-node[data-connected='true'] .topo-node-dot {
    fill: var(--success);
  }

  .topo-node-label {
    fill: var(--text);
    font-size: 11px;
    font-weight: 500;
    pointer-events: none;
    /* SVG text needs an explicit family so it doesn't fall back to the
       browser default that ignores our app font. */
    font-family: inherit;
  }

  /* ── Edges ────────────────────────────────────────────────────── */
  .topo-edge-hit {
    stroke: transparent;
    stroke-width: 14;
    fill: none;
    cursor: pointer;
    pointer-events: visibleStroke;
  }
  .topo-edge {
    fill: none;
    pointer-events: none;
    transition: stroke 200ms, opacity 200ms;
  }
  .topo-edge[data-tone='healthy'] { stroke: var(--success); opacity: 0.85; }
  .topo-edge[data-tone='warning'] { stroke: var(--warning); }
  .topo-edge[data-tone='danger']  { stroke: var(--danger); }
  .topo-edge[data-tone='idle']    { stroke: var(--border-strong); opacity: 0.8; }
  .topo-edge[data-tone='dim']     { stroke: var(--border); stroke-dasharray: 3 4; opacity: 0.7; }
  .topo-edge[data-circuit='unknown'][data-tone='healthy'] {
    stroke-dasharray: 2 3;
    opacity: 0.6;
  }
  .topo-edge[data-selected='true'] {
    opacity: 1;
    filter: drop-shadow(0 0 4px var(--primary));
  }

  .topo-arrow            { fill: var(--text-tertiary); }
  .topo-arrow-healthy    { fill: var(--success); }
  .topo-arrow-warning    { fill: var(--warning); }
  .topo-arrow-danger     { fill: var(--danger); }
  .topo-arrow-idle       { fill: var(--border-strong); }
  .topo-arrow-dim        { fill: var(--border); }

  /* Marching ants for OPEN circuits only — the alarm signal. */
  @media (prefers-reduced-motion: no-preference) {
    .topo-edge[data-circuit='open'] {
      stroke-dasharray: 5 4;
      animation: topo-ants 0.8s linear infinite;
    }
  }
  @keyframes topo-ants {
    to { stroke-dashoffset: -9; }
  }

  /* Edge name pill — opaque background so the label reads clearly on
     top of any underlying edge / node. */
  .topo-edge-label rect {
    fill: var(--surface-2);
    stroke: var(--border);
    stroke-width: 1;
  }
  .topo-edge-label text {
    fill: var(--text);
    font-size: 10.5px;
    font-weight: 500;
    pointer-events: none;
    font-family: inherit;
  }
</style>
