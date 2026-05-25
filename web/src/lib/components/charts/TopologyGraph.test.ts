// TopologyGraph tests — keep these light because the heavy lifting is
// covered by force.test.ts. The job here is to assert:
//
//   1. One node group renders per connection.
//   2. One edge renders per pipeline.
//   3. Clicking a node fires a `select` { kind: 'connection', id }.
//   4. An open-circuit edge carries the marching-ants attribute hook
//      that the CSS keyframe targets (data-circuit + stroke-dasharray).
//   5. An empty topology renders no nodes / no edges (and no crash).
//
// We deliberately don't try to assert post-settle positions — that's
// floating-point + RNG sensitive and adds zero value over the dedicated
// simulator suite.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import TopologyGraph from './TopologyGraph.svelte';
import type { TopologyResponse } from '$lib/api';

beforeEach(() => {
  // jsdom has no matchMedia by default — the graph reads it for
  // prefers-reduced-motion detection. setup.ts polyfills it but we
  // double-check here because reduce-motion controls whether the
  // simulator settles synchronously (good for tests) vs via rAF.
  if (!('matchMedia' in window)) {
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: (q: string) => ({
        matches: q.includes('reduce'),
        media: q,
        onchange: null,
        addListener: () => {},
        removeListener: () => {},
        addEventListener: () => {},
        removeEventListener: () => {},
        dispatchEvent: () => false
      })
    });
  }
});

function sampleTopology(): TopologyResponse {
  return {
    generated_at: '2026-05-24T12:00:00Z',
    tenant_id: 't',
    connections: [
      { id: 'c1', name: 'src-rabbit', type: 'rabbitmq', topic: 'orders', depth: 12, connected: true },
      { id: 'c2', name: 'dst-kafka', type: 'kafka', topic: 'orders.out', depth: 0, connected: true }
    ],
    pipelines: [
      {
        id: 'p1',
        name: 'orders-bridge',
        source_id: 'c1',
        destination_id: 'c2',
        enabled: true,
        msg_per_min: 240,
        processed: 12345,
        failed: 0,
        avg_latency_ms: 6.2,
        dlq_depth: 0,
        circuit_state: 'closed',
        status: 'connected'
      }
    ]
  };
}

describe('TopologyGraph', () => {
  it('renders one <g.topo-node> per connection', async () => {
    const { container } = render(TopologyGraph, { props: { topology: sampleTopology() } });
    // Wait one microtask for onMount → sim creation → settle.
    await Promise.resolve();
    await new Promise((r) => setTimeout(r, 10));
    const nodes = container.querySelectorAll('.topo-node');
    expect(nodes.length).toBe(2);
  });

  it('renders one <path.topo-edge> per pipeline', async () => {
    const { container } = render(TopologyGraph, { props: { topology: sampleTopology() } });
    await Promise.resolve();
    await new Promise((r) => setTimeout(r, 10));
    const edges = container.querySelectorAll('path.topo-edge');
    expect(edges.length).toBe(1);
  });

  it('open-circuit edges carry data-circuit="open"', async () => {
    const t = sampleTopology();
    t.pipelines[0].circuit_state = 'open';
    const { container } = render(TopologyGraph, { props: { topology: t } });
    await Promise.resolve();
    await new Promise((r) => setTimeout(r, 10));
    const edge = container.querySelector('path.topo-edge');
    expect(edge?.getAttribute('data-circuit')).toBe('open');
    expect(edge?.getAttribute('data-tone')).toBe('danger');
  });

  it('clicking a node fires `select` with kind=connection', async () => {
    const onSelect = vi.fn();
    const { container } = render(TopologyGraph, {
      props: { topology: sampleTopology() },
      events: {
        select: (e: Event) => onSelect((e as CustomEvent).detail)
      }
    });
    await Promise.resolve();
    await new Promise((r) => setTimeout(r, 10));

    const node = container.querySelector('.topo-node');
    expect(node).toBeTruthy();
    // Simulate a tap: pointerdown + pointerup with no movement in
    // between. The component treats no-movement as a click.
    await fireEvent.pointerDown(node!, { pointerId: 1, clientX: 100, clientY: 100 });
    await fireEvent.pointerUp(node!, { pointerId: 1, clientX: 100, clientY: 100 });
    expect(onSelect).toHaveBeenCalled();
    const detail = onSelect.mock.calls[0][0];
    expect(detail.kind).toBe('connection');
    // The id should be one of our two seeded connections.
    expect(['c1', 'c2']).toContain(detail.id);
  });

  it('renders no nodes when topology has empty arrays', async () => {
    const { container } = render(TopologyGraph, {
      props: {
        topology: {
          generated_at: 'now',
          tenant_id: 't',
          connections: [],
          pipelines: []
        }
      }
    });
    await Promise.resolve();
    expect(container.querySelectorAll('.topo-node').length).toBe(0);
    // The empty-state message should be visible.
    expect(container.textContent).toContain('No brokers yet');
  });
});
