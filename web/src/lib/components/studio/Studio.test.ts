// Studio shell tests — verify the three branches in the template:
//   1. hydrated → header + placeholder regions visible
//   2. pre-hydrate → loading affordance
//   3. error during hydrate (no draft) → error card
//
// We don't drive a real hydrate here (that's covered in
// studio.test.ts); these tests poke the store directly to put it in
// each branch and assert on the rendered DOM.
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { render } from '@testing-library/svelte';
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
  it('renders header + placeholder regions when state=building and pipeline present', () => {
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

    // Task 9 replaced the Palette / Canvas / Inspector stubs with real
    // components; Task 11 replaced the Dock stub. The Version Rail stub
    // remains (Task 12).
    expect(getByText(/Version Rail \(Task 12\)/)).toBeInTheDocument();
    // The palette renders its own heading ("Stages"), the dock renders
    // its "Dry-run" heading, and the inspector shows the "Nothing
    // selected" empty state on a fresh hydrate.
    expect(container.querySelector('.studio-palette')).not.toBeNull();
    expect(container.querySelector('.studio-canvas')).not.toBeNull();
    expect(container.querySelector('.studio-inspector')).not.toBeNull();
    expect(container.querySelector('.dock')).not.toBeNull();
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
