import { describe, it, expect } from 'vitest';
import {
  topoFromSource,
  validateFlow,
  pipelineToFlow,
  type FlowGraphNode,
  type FlowGraphEdge,
  type StageSummary,
  type RuleSummary
} from './flow-graph';

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

describe('pipelineToFlow', () => {
  const stage = (
    order: number,
    type: StageSummary['stage_type'],
    config = '{}'
  ): StageSummary => ({
    stage_order: order,
    stage_type: type,
    stage_config: config,
    enabled: true
  });

  it('source-only (no stages) wires source directly to destination', () => {
    const r = pipelineToFlow(
      { source_id: 'conn-A', destination_id: 'conn-B' },
      [],
      []
    );
    expect(r.nodes).toHaveLength(2);
    expect(r.nodes[0].kind).toBe('source');
    expect(r.nodes[0].connection_id).toBe('conn-A');
    expect(r.nodes[1].kind).toBe('destination');
    expect(r.nodes[1].connection_id).toBe('conn-B');
    expect(r.edges).toEqual([{ from: r.nodes[0].id, to: r.nodes[1].id }]);
  });

  it('emits stages in stage_order even when given unsorted', () => {
    const r = pipelineToFlow(
      { source_id: 'A', destination_id: 'B' },
      [stage(3, 'translate'), stage(1, 'filter'), stage(2, 'transform')],
      []
    );
    const kinds = r.nodes.map((n) => n.kind);
    expect(kinds).toEqual([
      'source',
      'filter',
      'transform',
      'translate',
      'destination'
    ]);
    // Linear chain
    expect(r.edges).toEqual([
      { from: r.nodes[0].id, to: r.nodes[1].id },
      { from: r.nodes[1].id, to: r.nodes[2].id },
      { from: r.nodes[2].id, to: r.nodes[3].id },
      { from: r.nodes[3].id, to: r.nodes[4].id }
    ]);
  });

  it('preserves each stage config verbatim onto its node', () => {
    const cfg = '{"paths":["a.b","c"]}';
    const r = pipelineToFlow(
      { source_id: 'A', destination_id: 'B' },
      [stage(1, 'filter', cfg)],
      []
    );
    const filter = r.nodes.find((n) => n.kind === 'filter')!;
    expect(filter.config).toBe(cfg);
  });

  it('positions nodes left-to-right with consistent spacing', () => {
    const r = pipelineToFlow(
      { source_id: 'A', destination_id: 'B' },
      [stage(1, 'filter'), stage(2, 'transform')],
      []
    );
    const xs = r.nodes.map((n) => n.x);
    expect(xs[1]).toBeGreaterThan(xs[0]);
    expect(xs[2]).toBeGreaterThan(xs[1]);
    expect(xs[3]).toBeGreaterThan(xs[2]);
  });

  it('with a route stage, every rule becomes an extra destination node', () => {
    const rules: RuleSummary[] = [
      {
        destination_id: 'D-EUR',
        priority: 1,
        condition_path: 'region',
        condition_operator: 'eq',
        condition_value: 'eu'
      },
      {
        destination_id: 'D-APAC',
        priority: 2,
        condition_path: 'region',
        condition_operator: 'eq',
        condition_value: 'apac'
      }
    ];
    const r = pipelineToFlow(
      { source_id: 'src', destination_id: 'D-DEFAULT' },
      [stage(1, 'route')],
      rules
    );
    const dests = r.nodes.filter((n) => n.kind === 'destination');
    // Default destination + 2 rule destinations
    expect(dests).toHaveLength(3);
    const conns = dests.map((d) => d.connection_id).sort();
    expect(conns).toEqual(['D-APAC', 'D-DEFAULT', 'D-EUR']);

    // Route node has 3 outgoing edges: one in the linear chain to the
    // default destination, plus one per rule destination.
    const route = r.nodes.find((n) => n.kind === 'route')!;
    const routeEdges = r.edges.filter((e) => e.from === route.id);
    expect(routeEdges).toHaveLength(3);
    const targets = routeEdges
      .map((e) => r.nodes.find((n) => n.id === e.to)?.connection_id)
      .sort();
    expect(targets).toEqual(['D-APAC', 'D-DEFAULT', 'D-EUR']);
  });

  it('rule whose destination matches default is collapsed (no duplicate)', () => {
    // A rule that targets the same connection as the pipeline's default
    // destination is already represented by the source→…→default edge;
    // duplicating it as a separate node would clutter the canvas.
    const r = pipelineToFlow(
      { source_id: 'src', destination_id: 'D-DEFAULT' },
      [stage(1, 'route')],
      [
        {
          destination_id: 'D-DEFAULT',
          priority: 1,
          condition_path: 'x',
          condition_operator: 'eq',
          condition_value: 'y'
        }
      ]
    );
    const dests = r.nodes.filter((n) => n.kind === 'destination');
    expect(dests).toHaveLength(1);
    expect(dests[0].connection_id).toBe('D-DEFAULT');
  });

  it('rules are ignored when no route stage is present', () => {
    // Without a `route` stage, routing rules cannot fire — drop them.
    const r = pipelineToFlow(
      { source_id: 'src', destination_id: 'D-DEFAULT' },
      [stage(1, 'filter')],
      [
        {
          destination_id: 'D-OTHER',
          priority: 1,
          condition_path: 'x',
          condition_operator: 'eq',
          condition_value: 'y'
        }
      ]
    );
    const dests = r.nodes.filter((n) => n.kind === 'destination');
    expect(dests).toHaveLength(1);
    expect(dests[0].connection_id).toBe('D-DEFAULT');
  });

  it('encodes rule predicate into the destination node config JSON', () => {
    const r = pipelineToFlow(
      { source_id: 'src', destination_id: 'D-DEFAULT' },
      [stage(1, 'route')],
      [
        {
          destination_id: 'D-EUR',
          priority: 5,
          condition_path: 'region',
          condition_operator: 'eq',
          condition_value: 'eu'
        }
      ]
    );
    const ruleDest = r.nodes.find(
      (n) => n.kind === 'destination' && n.connection_id === 'D-EUR'
    )!;
    const cfg = JSON.parse(ruleDest.config);
    expect(cfg.condition_path).toBe('region');
    expect(cfg.condition_operator).toBe('eq');
    expect(cfg.condition_value).toBe('eu');
    expect(cfg.priority).toBe(5);
  });

  it('reconstruction passes validateFlow without changes', () => {
    // The whole point — what we draw must validate so Save & Deploy
    // works against it the moment a pipeline loads.
    const r = pipelineToFlow(
      { source_id: 'src', destination_id: 'dst' },
      [stage(1, 'filter'), stage(2, 'transform'), stage(3, 'translate')],
      []
    );
    const graph: FlowGraphNode[] = r.nodes.map((n) => ({ id: n.id, kind: n.kind }));
    expect(() => validateFlow(graph, r.edges)).not.toThrow();
  });

  it('nextIdCounter equals the count of generated nodes', () => {
    // Callers need this to seed their own id counter so subsequent
    // palette-drops don't collide with reconstructed ids.
    const r = pipelineToFlow(
      { source_id: 'A', destination_id: 'B' },
      [stage(1, 'filter')],
      []
    );
    expect(r.nextIdCounter).toBe(r.nodes.length);
  });
});
