import { describe, it, expect } from 'vitest';
import { topoFromSource, validateFlow, type FlowGraphNode, type FlowGraphEdge } from './flow-graph';

const n = (id: string, kind: FlowGraphNode['kind']): FlowGraphNode => ({ id, kind });

describe('topoFromSource', () => {
  it('linear chain returns nodes in order', () => {
    const nodes = [n('s', 'source'), n('f', 'filter'), n('d', 'destination')];
    const edges: FlowGraphEdge[] = [
      { from: 's', to: 'f' },
      { from: 'f', to: 'd' }
    ];
    expect(topoFromSource(nodes, edges, 's')).toEqual(['s', 'f', 'd']);
  });

  it('throws on a cycle in the reachable subgraph', () => {
    const nodes = [n('s', 'source'), n('a', 'filter'), n('b', 'filter')];
    const edges: FlowGraphEdge[] = [
      { from: 's', to: 'a' },
      { from: 'a', to: 'b' },
      { from: 'b', to: 'a' } // ← cycle
    ];
    expect(() => topoFromSource(nodes, edges, 's')).toThrow(/cycle/i);
  });

  it('ignores cycles in unreachable parts of the canvas', () => {
    // A stranded sub-flow shouldn't block deploy of the real one.
    const nodes = [
      n('s', 'source'),
      n('d', 'destination'),
      n('x', 'transform'),
      n('y', 'transform')
    ];
    const edges: FlowGraphEdge[] = [
      { from: 's', to: 'd' },
      { from: 'x', to: 'y' },
      { from: 'y', to: 'x' } // unreachable cycle
    ];
    expect(topoFromSource(nodes, edges, 's')).toEqual(['s', 'd']);
  });

  it('throws when source id is not in the node list', () => {
    expect(() => topoFromSource([], [], 'missing')).toThrow();
  });
});

describe('validateFlow', () => {
  it('linear filter chain validates and returns the stage list', () => {
    const r = validateFlow(
      [n('s', 'source'), n('f', 'filter'), n('t', 'translate'), n('d', 'destination')],
      [
        { from: 's', to: 'f' },
        { from: 'f', to: 't' },
        { from: 't', to: 'd' }
      ]
    );
    expect(r.source.id).toBe('s');
    expect(r.destinations.map((x) => x.id)).toEqual(['d']);
    expect(r.stages.map((x) => x.kind)).toEqual(['filter', 'translate']);
  });

  it('requires exactly one source', () => {
    expect(() =>
      validateFlow(
        [n('s1', 'source'), n('s2', 'source'), n('d', 'destination')],
        [
          { from: 's1', to: 'd' },
          { from: 's2', to: 'd' }
        ]
      )
    ).toThrow(/source/i);
  });

  it('requires at least one destination', () => {
    expect(() => validateFlow([n('s', 'source')], [])).toThrow(/destination/i);
  });

  it('rejects when a destination is unreachable from the source', () => {
    const nodes = [n('s', 'source'), n('d1', 'destination'), n('d2', 'destination')];
    const edges: FlowGraphEdge[] = [{ from: 's', to: 'd1' }];
    expect(() => validateFlow(nodes, edges)).toThrow(/reachable/i);
  });

  it('rejects cycles inside the reachable chain', () => {
    const nodes = [n('s', 'source'), n('a', 'filter'), n('b', 'filter'), n('d', 'destination')];
    const edges: FlowGraphEdge[] = [
      { from: 's', to: 'a' },
      { from: 'a', to: 'b' },
      { from: 'b', to: 'a' },
      { from: 'a', to: 'd' }
    ];
    expect(() => validateFlow(nodes, edges)).toThrow(/cycle/i);
  });

  it('stage list stops at the first destination encountered', () => {
    // Two destinations, both reachable; the stages list still
    // terminates at the first one in topo order. The second one is
    // recorded in destinations[] for routing-rule derivation upstream.
    const r = validateFlow(
      [
        n('s', 'source'),
        n('f', 'filter'),
        n('d1', 'destination'),
        n('d2', 'destination')
      ],
      [
        { from: 's', to: 'f' },
        { from: 'f', to: 'd1' },
        { from: 'f', to: 'd2' }
      ]
    );
    expect(r.stages.map((x) => x.kind)).toEqual(['filter']);
    expect(r.destinations.map((x) => x.id).sort()).toEqual(['d1', 'd2']);
  });
});
