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
