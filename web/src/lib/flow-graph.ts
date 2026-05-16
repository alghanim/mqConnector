/**
 * Pure-data helpers behind the flow builder's Save & Deploy. Extracted out
 * of the Svelte component so the deploy gate (cycle detection, source-to-
 * destination reachability, linear stage extraction) is unit-testable.
 *
 * The shapes mirror what the canvas uses but stay deliberately minimal —
 * just `id` + `kind`, no positions or labels. Edges are id pairs.
 */

export type FlowNodeKind =
  | 'source'
  | 'destination'
  | 'filter'
  | 'transform'
  | 'translate'
  | 'route'
  | 'script'
  | 'validate';

export interface FlowGraphNode {
  id: string;
  kind: FlowNodeKind;
}
export interface FlowGraphEdge {
  from: string;
  to: string;
}

/**
 * Kahn's algorithm starting from `sourceId`. Returns visited ids in
 * topological order. Throws when:
 *   - sourceId isn't in nodes
 *   - any cycle is detected among nodes reachable from sourceId
 */
export function topoFromSource(
  nodes: FlowGraphNode[],
  edges: FlowGraphEdge[],
  sourceId: string
): string[] {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  if (!byId.has(sourceId)) {
    throw new Error('source node not found');
  }

  // Restrict to the subgraph reachable from sourceId so an unrelated
  // cycle elsewhere on the canvas doesn't block deploys.
  const reachable = new Set<string>();
  const stack = [sourceId];
  const adjFwd = new Map<string, string[]>();
  edges.forEach((e) => {
    const list = adjFwd.get(e.from) ?? [];
    list.push(e.to);
    adjFwd.set(e.from, list);
  });
  while (stack.length) {
    const id = stack.pop()!;
    if (reachable.has(id)) continue;
    reachable.add(id);
    for (const next of adjFwd.get(id) ?? []) {
      if (!reachable.has(next)) stack.push(next);
    }
  }

  // In-degree only counting edges entirely inside the reachable subgraph.
  const inDeg = new Map<string, number>();
  reachable.forEach((id) => inDeg.set(id, 0));
  for (const e of edges) {
    if (reachable.has(e.from) && reachable.has(e.to)) {
      inDeg.set(e.to, (inDeg.get(e.to) ?? 0) + 1);
    }
  }

  const queue: string[] = [];
  for (const [id, d] of inDeg) {
    if (d === 0) queue.push(id);
  }
  const order: string[] = [];
  while (queue.length) {
    const id = queue.shift()!;
    order.push(id);
    for (const next of adjFwd.get(id) ?? []) {
      if (!reachable.has(next)) continue;
      inDeg.set(next, (inDeg.get(next) ?? 0) - 1);
      if ((inDeg.get(next) ?? 0) === 0) queue.push(next);
    }
  }
  if (order.length !== reachable.size) {
    throw new Error('cycle detected — flows must be acyclic');
  }
  return order;
}

/**
 * Validate the canvas before deploy. Returns the resolved chain in the
 * order it will be persisted (source → stages → first destination). Throws
 * with a human-readable message for every failure case the editor wants
 * to surface inline.
 */
export interface ValidationResult {
  order: string[];
  source: FlowGraphNode;
  destinations: FlowGraphNode[];
  stages: FlowGraphNode[];
}

// ─── Phase 1: round-trip reconstruction ─────────────────────────────
// The canvas was build-only; pipelineToFlow inverts the deploy step so
// /flow?pipeline=:id can load an existing pipeline back into nodes+edges
// the operator can edit visually. Pure data → data so we can pin the
// behaviour with vitest without standing the Svelte component up.

export interface PipelineSummary {
  source_id: string;
  destination_id: string;
}

export interface StageSummary {
  stage_order: number;
  stage_type: FlowNodeKind;
  stage_config: string;
  enabled: boolean;
}

export interface RuleSummary {
  destination_id: string;
  priority: number;
  condition_path: string;
  condition_operator: string;
  condition_value: string;
}

export interface FlowReconstructionNode {
  id: string;
  kind: FlowNodeKind;
  x: number;
  y: number;
  label: string;
  connection_id: string;
  config: string;
}

export interface FlowReconstruction {
  nodes: FlowReconstructionNode[];
  edges: FlowGraphEdge[];
  nextIdCounter: number;
}

// Layout constants — kept here (not in the component) so the tests can
// assert exact positions without screen-scraping the DOM.
const RECON_NODE_SPACING = 220;
const RECON_ROW_Y = 120;
const RECON_DEST_STACK_GAP = 100;
const RECON_X_ORIGIN = 60;

/**
 * Reconstruct a canvas layout from a saved pipeline.
 *
 * Layout: a horizontal chain placed left → right
 *   source → stage1 → stage2 → … → destination
 *
 * Stages are emitted in stage_order. The default destination always
 * receives an edge from the last stage (or directly from the source
 * when there are no stages). If the chain contains a `route` stage,
 * each routing rule becomes an additional destination node stacked
 * vertically to the right of the route node, with the rule's predicate
 * stored in the destination node's `config` JSON.
 */
export function pipelineToFlow(
  pipeline: PipelineSummary,
  stages: StageSummary[],
  rules: RuleSummary[]
): FlowReconstruction {
  const ordered = [...stages].sort((a, b) => a.stage_order - b.stage_order);
  const nodes: FlowReconstructionNode[] = [];
  const edges: FlowGraphEdge[] = [];

  let counter = 0;
  const mkId = () => `n${++counter}`;

  const sourceId = mkId();
  nodes.push({
    id: sourceId,
    kind: 'source',
    x: RECON_X_ORIGIN,
    y: RECON_ROW_Y,
    label: 'source',
    connection_id: pipeline.source_id || '',
    config: '{}'
  });

  let prevId = sourceId;
  let routeId: string | null = null;
  ordered.forEach((s, i) => {
    const id = mkId();
    const x = RECON_X_ORIGIN + (i + 1) * RECON_NODE_SPACING;
    nodes.push({
      id,
      kind: s.stage_type,
      x,
      y: RECON_ROW_Y,
      label: s.stage_type,
      connection_id: '',
      config: s.stage_config || '{}'
    });
    edges.push({ from: prevId, to: id });
    if (s.stage_type === 'route') routeId = id;
    prevId = id;
  });

  const destX = RECON_X_ORIGIN + (ordered.length + 1) * RECON_NODE_SPACING;
  const defaultDestId = mkId();
  nodes.push({
    id: defaultDestId,
    kind: 'destination',
    x: destX,
    y: RECON_ROW_Y,
    label: 'destination',
    connection_id: pipeline.destination_id || '',
    config: '{}'
  });
  edges.push({ from: prevId, to: defaultDestId });

  if (routeId !== null) {
    const sortedRules = [...rules].sort((a, b) => a.priority - b.priority);
    sortedRules.forEach((r, i) => {
      // Skip the rule that targets the default destination — that edge
      // already exists as the linear chain output. Duplicating it would
      // produce two destination nodes wired to the same connection.
      if (r.destination_id && r.destination_id === pipeline.destination_id) {
        return;
      }
      const id = mkId();
      nodes.push({
        id,
        kind: 'destination',
        x: destX,
        y: RECON_ROW_Y + (i + 1) * RECON_DEST_STACK_GAP,
        label: 'destination',
        connection_id: r.destination_id || '',
        config: JSON.stringify({
          condition_path: r.condition_path || '',
          condition_operator: r.condition_operator || 'eq',
          condition_value: r.condition_value || '',
          priority: r.priority
        })
      });
      edges.push({ from: routeId as string, to: id });
    });
  }

  return { nodes, edges, nextIdCounter: counter };
}

export function validateFlow(
  nodes: FlowGraphNode[],
  edges: FlowGraphEdge[]
): ValidationResult {
  const sources = nodes.filter((n) => n.kind === 'source');
  const destinations = nodes.filter((n) => n.kind === 'destination');
  if (sources.length !== 1) {
    throw new Error('exactly one Source node is required');
  }
  if (destinations.length < 1) {
    throw new Error('at least one Destination node is required');
  }
  const source = sources[0];

  const order = topoFromSource(nodes, edges, source.id);
  const reachable = new Set(order);
  for (const d of destinations) {
    if (!reachable.has(d.id)) {
      throw new Error('every Destination must be reachable from the Source');
    }
  }

  // Stages: everything between source (exclusive) and the first
  // destination encountered in the topo order (exclusive). Subsequent
  // destinations are still represented in the resolved structure but
  // not in the linear stage chain.
  const stages: FlowGraphNode[] = [];
  const byId = new Map(nodes.map((n) => [n.id, n]));
  for (const id of order) {
    if (id === source.id) continue;
    const n = byId.get(id)!;
    if (n.kind === 'destination') break;
    stages.push(n);
  }

  return { order, source, destinations, stages };
}
