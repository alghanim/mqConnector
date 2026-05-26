// Tiny force-directed layout simulator for the live topology graph.
//
// Hand-rolled to keep the dependency footprint at zero — the Topology
// page only needs to settle ~12 broker nodes and ~24 edges, so a 200-
// line Euler stepper is plenty. NOT a d3-force replacement: no quadtree,
// no charge decay, no link constraints — just three forces blended per
// step and a velocity dampener so the system actually stops.
//
// Forces applied per step:
//   1. Repulsion between every pair of nodes (k_repel / d²)
//   2. Spring attraction along each edge toward rest_length (Hooke)
//   3. Mild centering pull toward viewport centre
//
// Numerical integration is a plain explicit Euler step with a velocity-
// based damping factor — fine for this short-lived settle phase.
//
// Public API:
//   • createSimulation(opts)  — returns a Sim object holding nodes/edges
//   • sim.setNodes(...) / setEdges(...) — diff-and-merge against current
//     state, preserving previously-laid-out positions so adding/removing
//     a node doesn't blow up the whole layout
//   • sim.step()               — one Euler iteration
//   • sim.settle(iterations)   — run N iterations synchronously
//   • sim.pin(id, x, y)        — fix a node's position; subsequent steps
//     won't move it. Used for drag.
//   • sim.unpin(id)            — release a previously-pinned node
//   • sim.bounds(w, h)         — update viewport size (re-centres pull)

export interface SimNodeInput {
  id: string;
  /** Optional seed position. Defaults to random near the centre. */
  x?: number;
  y?: number;
}

export interface SimEdgeInput {
  /** Identifier of this edge — used by the renderer to key the path. */
  id: string;
  source: string;
  target: string;
}

export interface SimNode {
  id: string;
  x: number;
  y: number;
  vx: number;
  vy: number;
  /** When true, x/y is fixed and the integrator skips this node. */
  pinned: boolean;
}

export interface SimOptions {
  width: number;
  height: number;
  /** Repulsion constant. Higher = nodes push apart harder. */
  kRepel?: number;
  /** Spring constant for edges. */
  kSpring?: number;
  /** Centering pull strength. */
  kCenter?: number;
  /** Resting edge length in pixels. */
  restLength?: number;
  /** Per-step velocity damping (0..1). 1 = no damping. */
  damping?: number;
  /** Cap on per-step displacement so the system can't explode. */
  maxStep?: number;
}

export interface Simulation {
  readonly nodes: SimNode[];
  readonly edges: SimEdgeInput[];
  bounds(width: number, height: number): void;
  setNodes(next: SimNodeInput[]): void;
  setEdges(next: SimEdgeInput[]): void;
  pin(id: string, x: number, y: number): void;
  unpin(id: string): void;
  step(): void;
  settle(iterations: number): void;
}

const DEFAULTS: Required<Omit<SimOptions, 'width' | 'height'>> = {
  kRepel: 8000,
  kSpring: 0.02,
  kCenter: 0.01,
  restLength: 180,
  damping: 0.78,
  maxStep: 40
};

export function createSimulation(opts: SimOptions): Simulation {
  let width = Math.max(1, opts.width);
  let height = Math.max(1, opts.height);
  const cfg = { ...DEFAULTS, ...opts };

  // Internal node store, keyed by id for fast lookup during edge force.
  const byId = new Map<string, SimNode>();
  const nodeList: SimNode[] = [];
  let edgeList: SimEdgeInput[] = [];

  function seedPosition(): { x: number; y: number } {
    // Seed nodes inside a small disc around the viewport centre so the
    // first iterations have meaningful gradients (a true zero gradient
    // is rare with finite-precision floats, but a clustered start makes
    // the relax-out animation read as intentional rather than random).
    const cx = width / 2;
    const cy = height / 2;
    const r = Math.min(width, height) * 0.18;
    const theta = Math.random() * Math.PI * 2;
    const radius = Math.sqrt(Math.random()) * r;
    return { x: cx + Math.cos(theta) * radius, y: cy + Math.sin(theta) * radius };
  }

  function bounds(w: number, h: number): void {
    width = Math.max(1, w);
    height = Math.max(1, h);
  }

  function setNodes(next: SimNodeInput[]): void {
    const keep = new Set<string>();
    for (const n of next) {
      keep.add(n.id);
      const existing = byId.get(n.id);
      if (existing) {
        // Honour an explicit seed if the caller supplied one, otherwise
        // preserve the laid-out position so re-renders don't reset the
        // graph on every diff.
        if (n.x !== undefined) existing.x = n.x;
        if (n.y !== undefined) existing.y = n.y;
        continue;
      }
      const seed = n.x !== undefined && n.y !== undefined ? { x: n.x, y: n.y } : seedPosition();
      const node: SimNode = { id: n.id, x: seed.x, y: seed.y, vx: 0, vy: 0, pinned: false };
      byId.set(n.id, node);
      nodeList.push(node);
    }
    // Drop nodes no longer present, preserving array order otherwise so
    // the renderer's `{#each}` keyed loop doesn't churn extra DOM.
    for (let i = nodeList.length - 1; i >= 0; i--) {
      if (!keep.has(nodeList[i].id)) {
        byId.delete(nodeList[i].id);
        nodeList.splice(i, 1);
      }
    }
  }

  function setEdges(next: SimEdgeInput[]): void {
    // Edges are stateless from the simulator's POV — replace wholesale.
    // Skip any edge whose endpoint isn't in the node set (defensive: the
    // backend should never send orphans but caching/race conditions can
    // briefly produce them).
    edgeList = next.filter((e) => byId.has(e.source) && byId.has(e.target));
  }

  function pin(id: string, x: number, y: number): void {
    const n = byId.get(id);
    if (!n) return;
    n.x = x;
    n.y = y;
    n.vx = 0;
    n.vy = 0;
    n.pinned = true;
  }

  function unpin(id: string): void {
    const n = byId.get(id);
    if (n) n.pinned = false;
  }

  function step(): void {
    const cx = width / 2;
    const cy = height / 2;

    // Reset force accumulators on each node. We re-use vx/vy as the
    // velocity carrier and accumulate forces into a fresh ax/ay; storing
    // them in a parallel array avoids extra allocations per step.
    const ax = new Array<number>(nodeList.length).fill(0);
    const ay = new Array<number>(nodeList.length).fill(0);

    // 1. Pairwise repulsion. O(n²) — fine at n ≤ ~50.
    for (let i = 0; i < nodeList.length; i++) {
      const a = nodeList[i];
      for (let j = i + 1; j < nodeList.length; j++) {
        const b = nodeList[j];
        let dx = a.x - b.x;
        let dy = a.y - b.y;
        let d2 = dx * dx + dy * dy;
        if (d2 < 0.01) {
          // Coincident — nudge apart deterministically so the integrator
          // can pick this up next iteration. The 0.5 offset is arbitrary;
          // any non-zero perturbation is fine.
          dx = 0.5;
          dy = 0.5;
          d2 = 0.5;
        }
        const dist = Math.sqrt(d2);
        const force = cfg.kRepel / d2;
        const fx = (dx / dist) * force;
        const fy = (dy / dist) * force;
        ax[i] += fx;
        ay[i] += fy;
        ax[j] -= fx;
        ay[j] -= fy;
      }
    }

    // 2. Spring attraction along edges.
    for (const e of edgeList) {
      const a = byId.get(e.source);
      const b = byId.get(e.target);
      if (!a || !b) continue;
      const dx = b.x - a.x;
      const dy = b.y - a.y;
      const dist = Math.sqrt(dx * dx + dy * dy) || 0.01;
      const stretch = dist - cfg.restLength;
      const force = cfg.kSpring * stretch;
      const fx = (dx / dist) * force;
      const fy = (dy / dist) * force;
      const ia = nodeList.indexOf(a);
      const ib = nodeList.indexOf(b);
      if (ia >= 0) {
        ax[ia] += fx;
        ay[ia] += fy;
      }
      if (ib >= 0) {
        ax[ib] -= fx;
        ay[ib] -= fy;
      }
    }

    // 3. Centering pull — proportional to distance from centre.
    for (let i = 0; i < nodeList.length; i++) {
      const n = nodeList[i];
      ax[i] += (cx - n.x) * cfg.kCenter;
      ay[i] += (cy - n.y) * cfg.kCenter;
    }

    // Integrate. Pinned nodes ignore forces; their velocity stays at 0.
    for (let i = 0; i < nodeList.length; i++) {
      const n = nodeList[i];
      if (n.pinned) {
        n.vx = 0;
        n.vy = 0;
        continue;
      }
      n.vx = (n.vx + ax[i]) * cfg.damping;
      n.vy = (n.vy + ay[i]) * cfg.damping;
      // Cap displacement so a tiny start cluster can't fly apart in one
      // step — important for the reduced-motion path which runs all
      // iterations synchronously without a chance to visually settle.
      const stepX = Math.max(-cfg.maxStep, Math.min(cfg.maxStep, n.vx));
      const stepY = Math.max(-cfg.maxStep, Math.min(cfg.maxStep, n.vy));
      n.x += stepX;
      n.y += stepY;
      // Soft viewport clamp so wandering nodes don't escape the SVG.
      const pad = 40;
      if (n.x < pad) n.x = pad;
      if (n.x > width - pad) n.x = width - pad;
      if (n.y < pad) n.y = pad;
      if (n.y > height - pad) n.y = height - pad;
    }
  }

  function settle(iterations: number): void {
    for (let i = 0; i < iterations; i++) step();
  }

  return {
    get nodes(): SimNode[] {
      return nodeList;
    },
    get edges(): SimEdgeInput[] {
      return edgeList;
    },
    bounds,
    setNodes,
    setEdges,
    pin,
    unpin,
    step,
    settle
  };
}
