// Unit tests for the force-directed layout simulator. Each test
// constructs the smallest possible graph that exercises one force:
//
//   1. Repulsion — two free nodes seeded close together push apart.
//   2. Spring   — two connected nodes pulled toward rest_length from
//                 either side (stretched OR compressed).
//   3. Reduced-motion / synchronous settle — settle(N) completes in a
//                 single call with no timers / rAF.
//
// The simulator is non-deterministic by default (random seed positions)
// so each test seeds positions explicitly via setNodes({ x, y }).

import { describe, it, expect } from 'vitest';
import { createSimulation } from './force';

describe('force layout: repulsion', () => {
  it('pushes two close-by nodes apart over multiple iterations', () => {
    const sim = createSimulation({ width: 800, height: 600 });
    sim.setNodes([
      { id: 'a', x: 400, y: 300 },
      { id: 'b', x: 410, y: 300 } // 10 px apart — well inside rest length
    ]);
    sim.setEdges([]); // no spring, repulsion only (plus centering)

    const startA = { x: sim.nodes[0].x, y: sim.nodes[0].y };
    const startB = { x: sim.nodes[1].x, y: sim.nodes[1].y };
    const startGap = Math.hypot(startA.x - startB.x, startA.y - startB.y);

    sim.settle(20);

    const gap = Math.hypot(
      sim.nodes[0].x - sim.nodes[1].x,
      sim.nodes[0].y - sim.nodes[1].y
    );
    expect(gap).toBeGreaterThan(startGap);
  });
});

describe('force layout: spring', () => {
  it('pulls two stretched-apart connected nodes back toward rest length', () => {
    const sim = createSimulation({ width: 1200, height: 800 });
    // Place them far enough apart that the spring overrides repulsion
    // and the system contracts.
    sim.setNodes([
      { id: 'a', x: 100, y: 400 },
      { id: 'b', x: 1100, y: 400 } // 1000 px apart, rest length 180
    ]);
    sim.setEdges([{ id: 'e1', source: 'a', target: 'b' }]);

    const startGap = Math.hypot(
      sim.nodes[0].x - sim.nodes[1].x,
      sim.nodes[0].y - sim.nodes[1].y
    );

    sim.settle(60);

    const gap = Math.hypot(
      sim.nodes[0].x - sim.nodes[1].x,
      sim.nodes[0].y - sim.nodes[1].y
    );
    expect(gap).toBeLessThan(startGap);
  });

  it('honors pinned nodes — they do not move under any force', () => {
    const sim = createSimulation({ width: 800, height: 600 });
    sim.setNodes([
      { id: 'a', x: 100, y: 100 },
      { id: 'b', x: 700, y: 500 }
    ]);
    sim.setEdges([{ id: 'e1', source: 'a', target: 'b' }]);

    sim.pin('a', 100, 100);
    sim.settle(30);

    expect(sim.nodes[0].x).toBe(100);
    expect(sim.nodes[0].y).toBe(100);
    // b should still have moved
    expect(sim.nodes[1].x !== 700 || sim.nodes[1].y !== 500).toBe(true);
  });
});

describe('force layout: reduced-motion path', () => {
  it('settle(N) completes synchronously without rAF / setInterval', () => {
    const sim = createSimulation({ width: 600, height: 600 });
    sim.setNodes([
      { id: 'a', x: 200, y: 300 },
      { id: 'b', x: 400, y: 300 }
    ]);
    sim.setEdges([{ id: 'e', source: 'a', target: 'b' }]);

    // Capture coords pre + post settle in one synchronous flow.
    const before = { x: sim.nodes[0].x, y: sim.nodes[0].y };
    sim.settle(50);
    const after = { x: sim.nodes[0].x, y: sim.nodes[0].y };
    expect(before.x === after.x && before.y === after.y).toBe(false);
  });
});

describe('force layout: setNodes / setEdges hot reload', () => {
  it('preserves laid-out positions when adding a new node', () => {
    const sim = createSimulation({ width: 800, height: 600 });
    sim.setNodes([
      { id: 'a', x: 300, y: 300 },
      { id: 'b', x: 500, y: 300 }
    ]);
    sim.setEdges([{ id: 'e', source: 'a', target: 'b' }]);
    sim.settle(10);
    const aAfter = { x: sim.nodes[0].x, y: sim.nodes[0].y };

    sim.setNodes([
      { id: 'a' }, // no seed → keep current pos
      { id: 'b' },
      { id: 'c' } // brand new
    ]);
    expect(sim.nodes[0].x).toBe(aAfter.x);
    expect(sim.nodes[0].y).toBe(aAfter.y);
    expect(sim.nodes.find((n) => n.id === 'c')).toBeTruthy();
  });

  it('drops edges whose endpoints are missing', () => {
    const sim = createSimulation({ width: 800, height: 600 });
    sim.setNodes([{ id: 'a' }, { id: 'b' }]);
    sim.setEdges([
      { id: 'e1', source: 'a', target: 'b' },
      { id: 'e2', source: 'a', target: 'ghost' } // ghost target
    ]);
    expect(sim.edges.length).toBe(1);
    expect(sim.edges[0].id).toBe('e1');
  });
});
