// Studio store tests. The store is the single shared truth for the
// /pipelines/{id}/studio chrome — every other Wave 1 task (canvas,
// inspector, dock, version rail) reads from it. The state-machine
// transitions matter for the chip's correctness, so the tests cover
// each primitive in isolation plus the hydrate happy-path / error /
// empty-revisions paths.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import {
  emptyData,
  studio,
  type PipelineRevision,
  type StudioStateData
} from './studio';
import type { Pipeline, Stage, Transform, RoutingRule } from '$lib/api';

// ─── Test helpers ────────────────────────────────────────────────────

// stubFetch installs a recording fake fetch. Each call returns the
// Response produced by `handler` so tests can branch by URL.
function stubFetch(handler: (url: string) => Response | Promise<Response>) {
  const spy = vi.fn(async (url: string | URL | Request) =>
    handler(typeof url === 'string' ? url : (url as URL).toString())
  );
  globalThis.fetch = spy as unknown as typeof fetch;
  return spy;
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  });
}

function makePipeline(over: Partial<Pipeline> = {}): Pipeline {
  return {
    id: 'p1',
    name: 'My Pipeline',
    source_id: 'c1',
    destination_id: 'c2',
    output_format: 'same',
    filter_paths: [],
    enabled: true,
    ...over
  };
}

function makeStage(order: number, type: Stage['stage_type'] = 'filter'): Stage {
  return {
    id: `s${order}`,
    stage_order: order,
    stage_type: type,
    stage_config: '{}',
    enabled: true
  };
}

function makeRevision(num: number): PipelineRevision {
  return {
    id: `rev-${num}`,
    tenant_id: 't1',
    pipeline_id: 'p1',
    revision_number: num,
    snapshot: null,
    snapshot_hash: 'h',
    author_sub: 'u',
    author_username: 'alice',
    change_summary: '',
    created_at: '2026-01-01T00:00:00Z',
    deployed_at: null
  };
}

beforeEach(() => {
  studio.reset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('emptyData', () => {
  it('produces the documented initial state', () => {
    const d = emptyData('pid-xyz');
    expect(d).toEqual<StudioStateData>({
      pipelineId: 'pid-xyz',
      state: 'empty',
      baseline: null,
      draft: null,
      deployedRev: null,
      latestRev: null,
      revisions: [],
      error: null,
      dirtyCount: 0,
      selectedNodeId: null,
      comparison: null,
      dryRun: null
    });
  });
});

describe('markDirty', () => {
  it('increments dirtyCount and flips state to dirty', () => {
    // Need a draft for markDirty to be meaningful — seed by setting
    // state manually first.
    studio.setState('building');
    studio.markDirty();
    let s = get(studio);
    expect(s.dirtyCount).toBe(1);
    expect(s.state).toBe('dirty');

    studio.markDirty();
    s = get(studio);
    expect(s.dirtyCount).toBe(2);
    expect(s.state).toBe('dirty');
  });

  it('does not flip out of empty state on dirty', () => {
    // Pre-hydrate: state is 'empty'. markDirty shouldn't bump us to
    // 'dirty' because there's no baseline to be dirty against.
    studio.markDirty();
    const s = get(studio);
    expect(s.state).toBe('empty');
    expect(s.dirtyCount).toBe(1);
  });
});

describe('resetDraft', () => {
  it('restores draft to baseline and zeros dirtyCount', async () => {
    // Hydrate via real machinery so we have a baseline + draft.
    await seedHydratedState();
    // Mutate the draft directly + bump the dirty counter to simulate a
    // user edit.
    studio.snapshot().draft!.stages.push(makeStage(2));
    studio.markDirty();
    expect(get(studio).dirtyCount).toBe(1);
    expect(get(studio).draft!.stages.length).toBe(2);

    studio.resetDraft();
    const after = get(studio);
    expect(after.dirtyCount).toBe(0);
    expect(after.state).toBe('building');
    // baseline had a single stage — draft should match again after reset
    expect(after.draft!.stages.length).toBe(after.baseline!.stages.length);
    expect(after.draft!.stages.length).toBe(1);
    // Critically: mutating the draft must NOT mutate the baseline
    // (cloneSnapshot deep-clones).
    after.draft!.stages.push(makeStage(2));
    expect(after.baseline!.stages.length).toBe(1);
  });

  it('is a no-op when there is no baseline', () => {
    studio.resetDraft();
    expect(get(studio).state).toBe('empty');
  });
});

describe('setError / clearError', () => {
  it('setError stores the message and flips to error', () => {
    studio.setError('boom');
    const s = get(studio);
    expect(s.state).toBe('error');
    expect(s.error).toBe('boom');
  });

  it('clearError clears the message and flips back to building', () => {
    studio.setError('boom');
    studio.clearError();
    const s = get(studio);
    expect(s.error).toBe(null);
    expect(s.state).toBe('building');
  });
});

describe('setComparison / clearComparison', () => {
  it('setComparison stores the pair and flips state', () => {
    studio.setComparison(7, 9, { stages: [] });
    const s = get(studio);
    expect(s.state).toBe('version-comparing');
    expect(s.comparison).toEqual({ from: 7, to: 9, diff: { stages: [] } });
  });

  it('clearComparison drops the pair and returns to building', () => {
    studio.setComparison(7, 9, null);
    studio.clearComparison();
    const s = get(studio);
    expect(s.comparison).toBe(null);
    expect(s.state).toBe('building');
  });

  it('clearComparison returns to dirty when there are pending edits', () => {
    studio.setState('building');
    studio.markDirty();
    studio.setComparison(7, 9, null);
    studio.clearComparison();
    expect(get(studio).state).toBe('dirty');
  });
});

describe('setDryRun / clearDryRun', () => {
  it('setDryRun stores the result and flips to simulating', () => {
    studio.setDryRun({ ok: true });
    const s = get(studio);
    expect(s.state).toBe('simulating');
    expect(s.dryRun).toEqual({ ok: true });
  });

  it('clearDryRun drops the result and returns to building', () => {
    studio.setDryRun({ ok: true });
    studio.clearDryRun();
    const s = get(studio);
    expect(s.dryRun).toBe(null);
    expect(s.state).toBe('building');
  });
});

// ─── hydrate ─────────────────────────────────────────────────────────

async function seedHydratedState() {
  stubFetch(makeHappyHandler());
  await studio.hydrate('p1');
}

function makeHappyHandler() {
  return (url: string): Response => {
    if (url.endsWith('/api/v1/pipelines/p1')) return jsonResponse(makePipeline());
    if (url.endsWith('/api/v1/pipelines/p1/stages')) return jsonResponse([makeStage(1)]);
    if (url.endsWith('/api/v1/pipelines/p1/transforms')) return jsonResponse([] as Transform[]);
    if (url.endsWith('/api/v1/pipelines/p1/routing-rules')) return jsonResponse([] as RoutingRule[]);
    if (url.includes('/api/v1/pipelines/p1/revisions?limit=')) {
      return jsonResponse({
        revisions: [makeRevision(2), makeRevision(1)],
        total: 2,
        limit: 25,
        offset: 0
      });
    }
    if (url.endsWith('/api/v1/pipelines/p1/revisions/current')) return jsonResponse(makeRevision(2));
    return new Response('not found', { status: 404 });
  };
}

describe('hydrate', () => {
  it('runs the five parallel fetches and assembles the studio state', async () => {
    const spy = stubFetch(makeHappyHandler());

    await studio.hydrate('p1');

    // 6 parallel calls — pipeline, stages, transforms, rules, revisions
    // list, revisions/current. The spec calls it "five" because revisions
    // list + current count as one logical "revision rail load"; the
    // implementation pulls them as separate endpoints.
    expect(spy).toHaveBeenCalledTimes(6);

    const s = get(studio);
    expect(s.state).toBe('building');
    expect(s.error).toBe(null);
    expect(s.pipelineId).toBe('p1');
    expect(s.baseline?.pipeline?.name).toBe('My Pipeline');
    expect(s.draft?.pipeline?.name).toBe('My Pipeline');
    expect(s.draft?.stages.length).toBe(1);
    expect(s.revisions.length).toBe(2);
    expect(s.latestRev?.revision_number).toBe(2);
    expect(s.deployedRev?.revision_number).toBe(2);
  });

  it('flips to error when a required endpoint fails', async () => {
    stubFetch((url) => {
      if (url.endsWith('/api/v1/pipelines/p1')) return jsonResponse(makePipeline());
      if (url.endsWith('/api/v1/pipelines/p1/stages')) {
        return jsonResponse({ error: 'db down' }, 500);
      }
      if (url.endsWith('/api/v1/pipelines/p1/transforms')) return jsonResponse([]);
      if (url.endsWith('/api/v1/pipelines/p1/routing-rules')) return jsonResponse([]);
      if (url.includes('/api/v1/pipelines/p1/revisions?limit=')) {
        return jsonResponse({ revisions: [], total: 0, limit: 25, offset: 0 });
      }
      if (url.endsWith('/api/v1/pipelines/p1/revisions/current')) return jsonResponse(makeRevision(1));
      return new Response('not found', { status: 404 });
    });

    await studio.hydrate('p1');

    const s = get(studio);
    expect(s.state).toBe('error');
    expect(s.error).toBe('db down');
    expect(s.draft).toBe(null);
  });

  it('treats an empty revisions list + 404 on /current as a successful hydrate', async () => {
    stubFetch((url) => {
      if (url.endsWith('/api/v1/pipelines/p1')) return jsonResponse(makePipeline());
      if (url.endsWith('/api/v1/pipelines/p1/stages')) return jsonResponse([]);
      if (url.endsWith('/api/v1/pipelines/p1/transforms')) return jsonResponse([]);
      if (url.endsWith('/api/v1/pipelines/p1/routing-rules')) return jsonResponse([]);
      if (url.includes('/api/v1/pipelines/p1/revisions?limit=')) {
        return jsonResponse({ revisions: [], total: 0, limit: 25, offset: 0 });
      }
      if (url.endsWith('/api/v1/pipelines/p1/revisions/current')) {
        return jsonResponse({ error: 'no deployed revision for this pipeline' }, 404);
      }
      return new Response('not found', { status: 404 });
    });

    await studio.hydrate('p1');

    const s = get(studio);
    expect(s.state).toBe('building');
    expect(s.error).toBe(null);
    expect(s.revisions).toEqual([]);
    expect(s.deployedRev).toBe(null);
    expect(s.latestRev).toBe(null);
    expect(s.draft).not.toBe(null);
  });
});
