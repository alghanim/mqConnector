// VersionRail tests — five cases cover the rail's contract:
//
//   1. Renders the revision list with the right Live/Draft badges.
//   2. Empty state when revisions is [].
//   3. Single-click selects; Cmd-click multi-selects (secondary).
//   4. Compare button fires the diff fetch and dispatches `compare`.
//   5. Collapsed state persists to localStorage across remounts.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import VersionRail from './VersionRail.svelte';
import { studio, type PipelineRevision } from '$lib/stores/studio';

function makeRev(over: Partial<PipelineRevision> = {}): PipelineRevision {
  return {
    id: 'rev-' + (over.revision_number ?? 1),
    tenant_id: 't1',
    pipeline_id: 'p1',
    revision_number: over.revision_number ?? 1,
    snapshot: null,
    snapshot_hash: 'h',
    author_sub: over.author_sub ?? 'alice',
    author_username: over.author_username ?? 'alice',
    change_summary: over.change_summary ?? 'change',
    created_at: over.created_at ?? new Date(Date.now() - 5 * 60_000).toISOString(),
    deployed_at: over.deployed_at ?? null,
    ...over
  };
}

function primeRevs(revisions: PipelineRevision[], deployedRev?: PipelineRevision | null) {
  // Mutate the snapshot directly — this is the convention the other
  // studio tests use (see DryRunDock.test.ts). markDirty + resetDraft
  // forces a notify cycle so the rail subscribes correctly.
  const cur = studio.snapshot();
  cur.pipelineId = 'p1';
  cur.revisions = revisions;
  cur.latestRev = revisions[0] ?? null;
  cur.deployedRev = deployedRev ?? null;
  cur.baseline = {
    pipeline: {
      id: 'p1',
      name: 'P',
      source_id: 's',
      destination_id: 'd',
      output_format: 'same',
      filter_paths: [],
      enabled: true
    },
    stages: [],
    transforms: [],
    routingRules: []
  };
  cur.draft = {
    pipeline: { ...cur.baseline.pipeline },
    stages: [],
    transforms: [],
    routingRules: []
  };
  cur.state = 'building';
  studio.markDirty();
  studio.resetDraft();
}

beforeEach(() => {
  studio.reset();
  try {
    localStorage.clear();
  } catch {
    /* no-op */
  }
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

describe('VersionRail', () => {
  it('renders revisions with Live and Draft badges', () => {
    const live = makeRev({ revision_number: 5 });
    const draft = makeRev({ revision_number: 7, change_summary: 'newer' });
    primeRevs([draft, live], live);
    const { getByText, getAllByText } = render(VersionRail);
    expect(getByText('#7')).toBeInTheDocument();
    expect(getByText('#5')).toBeInTheDocument();
    // The latest (#7) is Draft because it isn't the deployed one.
    expect(getAllByText(/Draft/i).length).toBeGreaterThanOrEqual(1);
    // The deployed (#5) is Live.
    expect(getAllByText(/Live/i).length).toBeGreaterThanOrEqual(1);
  });

  it('renders the empty state when revisions is []', () => {
    primeRevs([], null);
    const { getByText } = render(VersionRail);
    expect(getByText(/No revisions yet/i)).toBeInTheDocument();
  });

  it('single-click selects a row; Cmd-click toggles the secondary', async () => {
    const live = makeRev({ revision_number: 1 });
    const next = makeRev({ revision_number: 2 });
    primeRevs([next, live], live);
    const { container } = render(VersionRail);
    const hits = container.querySelectorAll('.row-hit') as NodeListOf<HTMLElement>;
    expect(hits.length).toBe(2);
    // Click row #2 — becomes primary.
    await fireEvent.click(hits[0]);
    await waitFor(() => {
      expect(hits[0].closest('.row')!.classList.contains('is-primary')).toBe(true);
    });
    // Cmd-click row #1 — becomes secondary.
    await fireEvent.click(hits[1], { metaKey: true });
    await waitFor(() => {
      expect(hits[1].closest('.row')!.classList.contains('is-secondary')).toBe(true);
    });
  });

  it('Compare button fires `compare` after a successful diff fetch', async () => {
    const live = makeRev({ revision_number: 3 });
    const next = makeRev({ revision_number: 4 });
    primeRevs([next, live], live);
    const fetchSpy = vi.fn(async (url: string | URL | Request) => {
      const u = typeof url === 'string' ? url : (url as URL).toString();
      if (u.includes('/revisions/4/diff?against=3')) {
        return new Response(
          JSON.stringify({
            from: 4,
            to: 3,
            diff: {
              pipeline_fields: [],
              stages: { added: [], removed: [], modified: [] },
              transforms: { added: [], removed: [], modified: [] },
              routing_rules: { added: [], removed: [], modified: [] }
            }
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        );
      }
      return new Response('nf', { status: 404 });
    });
    globalThis.fetch = fetchSpy as unknown as typeof fetch;

    const events: Array<{ from: number; to: number; diff: unknown }> = [];
    const { container, getByText } = render(VersionRail, {
      events: {
        compare: (e: CustomEvent<{ from: number; to: number; diff: unknown }>) =>
          events.push(e.detail)
      }
    });
    // Pick the primary row first (#4 — the newest, non-live).
    const hits = container.querySelectorAll('.row-hit') as NodeListOf<HTMLElement>;
    await fireEvent.click(hits[0]);
    // Now click Compare in the toolbar.
    await fireEvent.click(getByText('Compare'));
    await waitFor(() => {
      expect(events.length).toBe(1);
      expect(events[0].from).toBe(4);
      expect(events[0].to).toBe(3);
    });
  });

  it('collapse persists to localStorage and rehydrates on remount', async () => {
    primeRevs([makeRev({ revision_number: 1 })], null);
    const { getByLabelText, unmount } = render(VersionRail);
    const toggle = getByLabelText(/Toggle revisions panel/i);
    await fireEvent.click(toggle);
    expect(localStorage.getItem('mqc.studio.versionrail.collapsed')).toBe('1');
    unmount();
    // Remount — rail-body should be hidden.
    const { container } = render(VersionRail);
    await waitFor(() => {
      expect(container.querySelector('#rail-body')).toBeNull();
    });
  });
});
