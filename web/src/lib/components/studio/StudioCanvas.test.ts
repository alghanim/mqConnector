// StudioCanvas tests — the SVG graph reads from the studio store and
// writes back via studio.addStage / studio.removeStage /
// studio.selectNode. The six tests cover:
//
//   1. Source + destination nodes render once the draft is seeded.
//   2. The stage chain renders one node per stage in draft.stages.
//   3. Clicking a stage node selects it (calls studio.selectNode).
//   4. Dropping a palette payload onto the canvas appends a stage.
//   5. The empty-state overlay renders when there are no stages.
//   6. version-comparing state makes the canvas read-only — clicks
//      don't mutate the selection.
//
// The canvas pulls connections from /v1/connections on mount; we stub
// fetch to return an empty list so the source/destination cards render
// with the connection id as their fallback label.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import { get } from 'svelte/store';
import StudioCanvas from './StudioCanvas.svelte';
import { studio } from '$lib/stores/studio';
import type { Pipeline, Stage } from '$lib/api';

function makePipeline(over: Partial<Pipeline> = {}): Pipeline {
  return {
    id: 'p1',
    name: 'P',
    source_id: 'src-id',
    destination_id: 'dst-id',
    output_format: 'same',
    filter_paths: [],
    enabled: true,
    ...over
  };
}

function makeStage(order: number, type: Stage['stage_type'] = 'filter', id?: string): Stage {
  return {
    id: id ?? `s${order}`,
    stage_order: order,
    stage_type: type,
    stage_config: '{}',
    enabled: true
  };
}

// primeStore mutates the live studio store with a draft. We can't use
// `studio.hydrate` here because that would trigger five fetch calls;
// instead we follow the same pattern Studio.test.ts uses — read the
// snapshot, replace fields in place, then nudge the store via
// markDirty + resetDraft so subscribers re-render against the seed.
function primeStore(stages: Stage[] = []) {
  const cur = studio.snapshot();
  const baseline = {
    pipeline: makePipeline(),
    stages,
    transforms: [],
    routingRules: []
  };
  cur.baseline = baseline;
  cur.draft = {
    pipeline: { ...makePipeline() },
    stages: stages.map((s) => ({ ...s })),
    transforms: [],
    routingRules: []
  };
  cur.state = 'building';
  // Nudge subscribers — markDirty + resetDraft re-publishes the seed
  // without leaving the dirty counter at >0.
  studio.markDirty();
  studio.resetDraft();
}

beforeEach(() => {
  studio.reset();
  // Stub fetch so the canvas's onMount connections fetch resolves to
  // an empty list without hitting the network.
  globalThis.fetch = vi.fn(async () =>
    new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } })
  ) as unknown as typeof fetch;
});

afterEach(() => {
  studio.reset();
  vi.restoreAllMocks();
});

describe('StudioCanvas', () => {
  it('renders the source and destination nodes', async () => {
    primeStore([]);
    const { container } = render(StudioCanvas);
    await waitFor(() => {
      expect(container.querySelector('[data-node-kind="source"]')).not.toBeNull();
      expect(container.querySelector('[data-node-kind="destination"]')).not.toBeNull();
    });
  });

  it('renders one stage node per draft stage', async () => {
    primeStore([makeStage(1, 'filter'), makeStage(2, 'transform'), makeStage(3, 'route')]);
    const { container } = render(StudioCanvas);
    await waitFor(() => {
      const stageNodes = container.querySelectorAll('[data-node-kind="stage"]');
      expect(stageNodes.length).toBe(3);
    });
  });

  it('clicking a stage node calls studio.selectNode', async () => {
    primeStore([makeStage(1, 'filter', 's-filter-1')]);
    const { container } = render(StudioCanvas);
    const node = await waitFor(() => {
      const n = container.querySelector('[data-node-id="s-filter-1"]') as Element | null;
      expect(n).not.toBeNull();
      return n!;
    });
    expect(get(studio).selectedNodeId).toBe(null);
    await fireEvent.click(node);
    expect(get(studio).selectedNodeId).toBe('s-filter-1');
  });

  it('drop with the stage-type mime appends a stage to the draft', async () => {
    primeStore([]);
    const { container } = render(StudioCanvas);
    const canvas = container.querySelector('.studio-canvas') as HTMLElement;
    expect(canvas).not.toBeNull();
    expect(get(studio).draft!.stages.length).toBe(0);

    // Build a fake DataTransfer that carries the mime the canvas
    // listens for. The canvas's onDrop reads via dataTransfer.getData.
    const fakeDT = {
      types: ['application/x-mqc-stage-type'],
      dropEffect: '',
      getData(type: string) {
        return type === 'application/x-mqc-stage-type' ? 'transform' : '';
      }
    };

    const dragOver = new Event('dragover', { bubbles: true, cancelable: true });
    Object.defineProperty(dragOver, 'dataTransfer', { value: fakeDT });
    canvas.dispatchEvent(dragOver);

    const drop = new Event('drop', { bubbles: true, cancelable: true });
    Object.defineProperty(drop, 'dataTransfer', { value: fakeDT });
    canvas.dispatchEvent(drop);

    await waitFor(() => {
      const stages = get(studio).draft!.stages;
      expect(stages.length).toBe(1);
      expect(stages[0].stage_type).toBe('transform');
    });
  });

  it('renders the empty-state overlay when the draft has no stages', async () => {
    primeStore([]);
    const { container } = render(StudioCanvas);
    await waitFor(() => {
      expect(container.querySelector('.studio-canvas-empty')).not.toBeNull();
    });
  });

  it("state='version-comparing' makes the canvas read-only — clicks don't change selection", async () => {
    primeStore([makeStage(1, 'filter', 'ro-1')]);
    studio.setComparison(1, 2, null); // flips state to version-comparing
    const { container } = render(StudioCanvas);
    const node = await waitFor(() => {
      const n = container.querySelector('[data-node-id="ro-1"]') as Element | null;
      expect(n).not.toBeNull();
      return n!;
    });
    expect(get(studio).selectedNodeId).toBe(null);
    await fireEvent.click(node);
    // Selection MUST remain null — the canvas refused the click.
    expect(get(studio).selectedNodeId).toBe(null);
    // And the canvas is marked read-only via the data attribute the
    // CSS uses to style the cursor.
    const canvas = container.querySelector('.studio-canvas') as HTMLElement;
    expect(canvas.getAttribute('data-readonly')).toBe('true');
  });
});
