// Studio shell tests — verify the three default branches in the
// template plus the two Task-12 branches:
//   1. hydrated → header + placeholder regions visible
//   2. pre-hydrate → loading affordance
//   3. error during hydrate (no draft) → error card
//   4. state='version-comparing' → DiffViewer replaces the canvas
//   5. rail emits `rollback` → DeployDialog opens with kind='rollback'
//
// We don't drive a real hydrate here (that's covered in
// studio.test.ts); these tests poke the store directly to put it in
// each branch and assert on the rendered DOM.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import Studio from './Studio.svelte';
import { studio } from '$lib/stores/studio';
import type { Pipeline } from '$lib/api';

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

beforeEach(() => {
  studio.reset();
});
afterEach(() => {
  studio.reset();
});

describe('Studio shell', () => {
  it('renders header + every region when state=building and pipeline present', () => {
    // Build a minimal hydrated state in-store. Easier than calling
    // hydrate() (which would require fetch stubs) and asserts the
    // shell renders given any valid draft, regardless of hydrate path.
    studio.setState('building');
    // Direct store priming via the snapshot — we don't expose a
    // setBaseline API yet (Tasks 9-12 will), so the test sets it via
    // reset + manual mutation through the underlying store. The whole
    // point is to put a draft on the screen.
    primeWithPipeline();

    const { container, getByText } = render(Studio, { props: { pipelineId: 'p1' } });

    // Header: pipeline name in the heading.
    expect(container.querySelector('.studio-header')).not.toBeNull();
    expect(getByText('My Pipeline')).toBeInTheDocument();

    // Tasks 9/11/12 replaced every stub. Verify the live components
    // mount: palette ("Stages"), canvas, inspector, dock, version rail.
    expect(container.querySelector('.studio-palette')).not.toBeNull();
    expect(container.querySelector('.studio-canvas')).not.toBeNull();
    expect(container.querySelector('.studio-inspector')).not.toBeNull();
    expect(container.querySelector('.dock')).not.toBeNull();
    expect(container.querySelector('.rail')).not.toBeNull();
  });

  it('renders the loading affordance when no draft is present yet', () => {
    // Default reset state — no draft, state='empty'.
    const { container } = render(Studio, { props: { pipelineId: 'p1' } });
    // The shell falls back to the inline loading text.
    expect(container.textContent || '').toMatch(/Loading Studio/);
    // No header in this branch.
    expect(container.querySelector('.studio-header')).toBeNull();
  });

  it('renders the error card when state=error and no draft is present', () => {
    studio.setError('500 server error');
    const { getByText } = render(Studio, { props: { pipelineId: 'p1' } });
    expect(getByText(/Failed to load Studio/)).toBeInTheDocument();
    expect(getByText(/500 server error/)).toBeInTheDocument();
    expect(getByText(/Retry/)).toBeInTheDocument();
  });

  it("state='version-comparing' replaces the canvas with the DiffViewer", () => {
    primeWithPipeline();
    studio.setComparison(2, 1, {
      pipeline_fields: [{ path: 'name', before: 'a', after: 'b' }],
      stages: { added: [], removed: [], modified: [] },
      transforms: { added: [], removed: [], modified: [] },
      routing_rules: { added: [], removed: [], modified: [] }
    });
    const { getByText, container } = render(Studio, { props: { pipelineId: 'p1' } });
    // Compare strip header + DiffViewer body render in place of the
    // regular canvas.
    expect(getByText(/Comparing #2 → #1/)).toBeInTheDocument();
    expect(container.querySelector('.studio-compare-overlay')).not.toBeNull();
    expect(getByText(/Pipeline fields/i)).toBeInTheDocument();
  });

  it('rollback request from the VersionRail opens the DeployDialog', async () => {
    // Stub fetch so the DeployDialog's diff fetch (kicked on mount)
    // doesn't throw — we only care that the dialog opens.
    globalThis.fetch = vi.fn(async () =>
      new Response('{}', { status: 404 })
    ) as unknown as typeof fetch;
    primeWithPipeline();
    // Prime a revision so the rail renders rows we can kebab.
    const cur = studio.snapshot();
    cur.pipelineId = 'p1';
    cur.revisions = [
      {
        id: 'r1',
        tenant_id: 't',
        pipeline_id: 'p1',
        revision_number: 7,
        snapshot: null,
        snapshot_hash: 'h',
        author_sub: 'alice',
        author_username: 'alice',
        change_summary: 'x',
        created_at: new Date().toISOString(),
        deployed_at: null
      }
    ];
    cur.latestRev = cur.revisions[0];
    studio.markDirty();
    studio.resetDraft();

    const { container, getByText } = render(Studio, { props: { pipelineId: 'p1' } });
    // Open the kebab on the row.
    const kebab = container.querySelector('.row-kebab') as HTMLButtonElement;
    expect(kebab).not.toBeNull();
    await fireEvent.click(kebab);
    // The menu item "Rollback to this" dispatches up to Studio, which
    // opens the DeployDialog.
    await fireEvent.click(getByText(/Rollback to this/i));
    await waitFor(() => {
      // The dialog renders a "Rollback to revision #7" title.
      expect(getByText(/Rollback to revision #7/i)).toBeInTheDocument();
    });
    vi.restoreAllMocks();
  });
});

// primeWithPipeline mutates the live store using the internals exposed
// by `studio.snapshot()`. We can't `set` directly because the store is
// private — but the snapshot returns a live reference we can mutate,
// then we trigger a re-publish by calling a benign mutator
// (markDirty + resetDraft together: bump then reset means no
// dirty-count drift but a re-publish does happen).
function primeWithPipeline() {
  // We bypass the public API by reading the snapshot, replacing
  // baseline/draft, and re-publishing through markDirty/resetDraft.
  // This is a test-only hack — production code never does this and
  // Task 9+ will introduce proper seed mutators.
  const cur = studio.snapshot();
  const p = makePipeline();
  const baseline = {
    pipeline: p,
    stages: [],
    transforms: [],
    routingRules: []
  };
  // mutate in place
  cur.baseline = baseline;
  cur.draft = {
    pipeline: { ...p },
    stages: [],
    transforms: [],
    routingRules: []
  };
  cur.state = 'building';
  // Re-publish: markDirty + resetDraft brings dirtyCount back to 0 and
  // fires a notify cycle so subscribed components pick up the seed.
  studio.markDirty();
  studio.resetDraft();
}
