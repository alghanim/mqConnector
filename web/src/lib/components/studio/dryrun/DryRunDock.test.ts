// DryRunDock tests — five cases cover the dock's full contract:
//   1. Renders the heading + Run/Clear buttons.
//   2. Run posts to /api/v1/preview with the inline draft stages +
//      sample, and stores the response on studio.dryRun.
//   3. Preview error → store.dockError populated, store.error UNCHANGED.
//   4. Collapsed state persists to localStorage and rehydrates.
//   5. runSingleStage (public method, the inspector test wire) posts
//      a single-stage preview request.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import { get } from 'svelte/store';
import DryRunDock from './DryRunDock.svelte';
import { studio } from '$lib/stores/studio';
import type { Pipeline, Stage } from '$lib/api';

function makePipeline(over: Partial<Pipeline> = {}): Pipeline {
  return {
    id: 'p1',
    name: 'P',
    source_id: 'src',
    destination_id: 'dst',
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
});

afterEach(() => {
  studio.reset();
  vi.restoreAllMocks();
  try {
    localStorage.clear();
  } catch {
    /* no-op */
  }
});

describe('DryRunDock', () => {
  it('renders Run and Clear buttons with the heading', () => {
    primeStore([]);
    const { getByText } = render(DryRunDock);
    expect(getByText(/Dry-run/i)).toBeInTheDocument();
    expect(getByText('Run')).toBeInTheDocument();
    expect(getByText('Clear')).toBeInTheDocument();
  });

  it('Run posts /v1/preview with draft stages + sample and stores the result', async () => {
    primeStore([makeStage(1, 'filter', 's-1')]);
    // Picker starts empty; we set value by switching to Paste then
    // typing. Simpler: stub fetch and click a Saved fixture first so
    // the dock has a sample.
    const fetchSpy = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
      const u = typeof url === 'string' ? url : (url as URL).toString();
      if (u.endsWith('/api/v1/preview')) {
        const body = init?.body ? JSON.parse(init.body as string) : {};
        // Echo back the stages so the assertion can inspect them.
        return new Response(
          JSON.stringify({
            ok: true,
            output: '{"k":"v"}',
            format: 'json',
            stage_runs: [
              { name: 'filter', duration_ns: 1_000_000, failed: false, body: '{"k":"v"}', format: 'json' }
            ],
            echo: body
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        );
      }
      return new Response('not found', { status: 404 });
    });
    globalThis.fetch = fetchSpy as unknown as typeof fetch;
    const { getAllByText, getByText } = render(DryRunDock);
    // Pick the first saved sample so the dock has a value.
    const useButtons = await waitFor(() => getAllByText(/use this/i));
    await fireEvent.click(useButtons[0]);
    // Now Run.
    await fireEvent.click(getByText('Run'));
    await waitFor(() => {
      const dry = get(studio).dryRun as { ok?: boolean; stage_runs?: unknown[] } | null;
      expect(dry?.ok).toBe(true);
      expect(Array.isArray(dry?.stage_runs)).toBe(true);
    });
    // The preview call must have included the draft stages.
    const previewCall = fetchSpy.mock.calls.find(([u]) =>
      String(u).endsWith('/api/v1/preview')
    );
    expect(previewCall).toBeDefined();
    const sentBody = JSON.parse((previewCall![1] as RequestInit).body as string);
    expect(Array.isArray(sentBody.stages)).toBe(true);
    expect(sentBody.stages[0].id).toBe('s-1');
  });

  it('preview error populates dockError but NOT the build-error channel', async () => {
    primeStore([makeStage(1, 'filter', 's-1')]);
    // Fetch returns a 500 so api.post throws an ApiError.
    globalThis.fetch = vi.fn(async () =>
      new Response(JSON.stringify({ error: 'kaboom' }), {
        status: 500,
        headers: { 'Content-Type': 'application/json' }
      })
    ) as unknown as typeof fetch;
    const { getAllByText, getByText } = render(DryRunDock);
    const useButtons = await waitFor(() => getAllByText(/use this/i));
    await fireEvent.click(useButtons[0]);
    await fireEvent.click(getByText('Run'));
    await waitFor(() => {
      const s = get(studio);
      expect(s.dockError).toBe('kaboom');
      // Build-error channel must be UNCHANGED.
      expect(s.error).toBe(null);
    });
  });

  it('collapse persists to localStorage and rehydrates on remount', async () => {
    primeStore([]);
    const { getByText, unmount } = render(DryRunDock);
    // Click the chevron toggle — its accessible name is the heading.
    const toggle = getByText(/Dry-run/i).closest('button')!;
    await fireEvent.click(toggle);
    expect(localStorage.getItem('mqc.studio.dryrun.collapsed')).toBe('1');
    unmount();
    // Remount — dock body should be hidden (collapsed).
    const { container } = render(DryRunDock);
    await waitFor(() => {
      expect(container.querySelector('#dock-body')).toBeNull();
    });
  });

  it('runSingleStage posts a single-stage preview with the current sample', async () => {
    primeStore([makeStage(1, 'filter', 's-1'), makeStage(2, 'route', 's-2')]);
    const fetchSpy = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
      const u = typeof url === 'string' ? url : (url as URL).toString();
      if (u.endsWith('/api/v1/preview')) {
        return new Response(
          JSON.stringify({
            ok: true,
            output: 'x',
            format: 'json',
            stage_runs: [
              { name: 'filter', duration_ns: 500_000, failed: false, body: 'x', format: 'json' }
            ]
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        );
      }
      return new Response('nf', { status: 404 });
    });
    globalThis.fetch = fetchSpy as unknown as typeof fetch;
    const { getAllByText, component } = render(DryRunDock);
    // Seed the sample by picking a fixture.
    const useButtons = await waitFor(() => getAllByText(/use this/i));
    await fireEvent.click(useButtons[0]);
    // Invoke the public method directly.
    await (
      component as unknown as {
        runSingleStage: (s: Stage, p?: unknown) => Promise<void>;
      }
    ).runSingleStage(makeStage(1, 'filter', 's-1'));
    const previewCall = fetchSpy.mock.calls.find(([u]) =>
      String(u).endsWith('/api/v1/preview')
    );
    expect(previewCall).toBeDefined();
    const sentBody = JSON.parse((previewCall![1] as RequestInit).body as string);
    // The single-stage request must contain ONLY one stage.
    expect(sentBody.stages.length).toBe(1);
    expect(sentBody.stages[0].id).toBe('s-1');
  });
});
