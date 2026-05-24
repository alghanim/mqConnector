// StudioInspector tests — verify each of the three render branches and
// the delete button's wiring. The inspector is a thin shell around the
// studio store; the four tests cover its full surface for Task 9
// (per-stage editors land in Task 10 and are covered there).
//
// We bypass `studio.hydrate` and seed the store via direct snapshot
// mutation — same pattern Studio.test.ts uses. A fetch stub answers
// the inspector's onMount /v1/connections call with an empty list so
// the source/destination card still renders (falling back to the raw
// id as label).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import { get } from 'svelte/store';
import StudioInspector from './StudioInspector.svelte';
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

function primeStore(stages: Stage[] = []) {
  const cur = studio.snapshot();
  cur.baseline = {
    pipeline: makePipeline(),
    stages,
    transforms: [],
    routingRules: []
  };
  cur.draft = {
    pipeline: { ...makePipeline() },
    stages: stages.map((s) => ({ ...s })),
    transforms: [],
    routingRules: []
  };
  cur.state = 'building';
  studio.markDirty();
  studio.resetDraft();
}

beforeEach(() => {
  studio.reset();
  globalThis.fetch = vi.fn(async () =>
    new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } })
  ) as unknown as typeof fetch;
});

afterEach(() => {
  studio.reset();
  vi.restoreAllMocks();
});

describe('StudioInspector', () => {
  it('renders the empty state when nothing is selected', () => {
    primeStore([]);
    const { container, getByText } = render(StudioInspector);
    // EmptyState renders our title verbatim.
    expect(getByText(/Nothing selected/i)).toBeInTheDocument();
    // Stage card markers are absent.
    expect(container.querySelector('.studio-inspector-meta')).toBeNull();
  });

  it('renders the stage card when a stage is selected', async () => {
    primeStore([makeStage(1, 'filter', 'sel-stage')]);
    studio.selectNode('sel-stage');
    const { container, getByText } = render(StudioInspector);
    await waitFor(() => {
      // The "Stage" heading + the stage type ("filter") are both in
      // the card. We assert on both to pin the layout.
      expect(getByText('Stage')).toBeInTheDocument();
      expect(getByText('filter')).toBeInTheDocument();
      expect(container.querySelector('.studio-inspector-meta')).not.toBeNull();
    });
  });

  it('renders the connection card when the source node is selected', async () => {
    primeStore([]);
    studio.selectNode('source-src-id');
    const { container, getByText } = render(StudioInspector);
    await waitFor(() => {
      expect(getByText('Connection')).toBeInTheDocument();
      // No connections were returned by the stub; the inspector falls
      // back to the raw id as the connection name.
      expect(getByText('src-id')).toBeInTheDocument();
      expect(container.querySelector('.studio-inspector-meta')).not.toBeNull();
    });
  });

  it('Delete button calls studio.removeStage', async () => {
    primeStore([makeStage(1, 'filter', 'doomed')]);
    studio.selectNode('doomed');
    const { getByText } = render(StudioInspector);
    await waitFor(() => expect(getByText('Delete stage')).toBeInTheDocument());
    expect(get(studio).draft!.stages.length).toBe(1);
    await fireEvent.click(getByText('Delete stage'));
    expect(get(studio).draft!.stages.length).toBe(0);
    // Selection clears as a side-effect of removeStage.
    expect(get(studio).selectedNodeId).toBe(null);
  });
});
