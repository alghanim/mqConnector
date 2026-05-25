// ActionDrawer — behaviour tests for the right-panel actions.
//
// What we cover:
//   1. Empty state when no entry is selected.
//   2. "Run replay simulation" button dispatches runReplaySim with the
//      entry id.
//   3. The retry-confidence pill changes based on the replaySim
//      response (would_succeed true → success copy; false → unsafe).

import { describe, it, expect } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import ActionDrawer from './ActionDrawer.svelte';
import type { DLQEntry, DLQReplaySimResponse } from '$lib/api';

function makeEntry(id = 'entry-xyz'): DLQEntry {
  return {
    id,
    pipeline_id: 'pipe-1',
    source_queue: 'orders.in',
    original_msg: 'eyJoZWxsbyI6ICJ3b3JsZCJ9',
    error_reason: 'connection refused',
    retry_count: 0,
    created_at: new Date().toISOString()
  };
}

describe('ActionDrawer', () => {
  it('shows the empty-state copy when no entry is selected', () => {
    const { getByText } = render(ActionDrawer, { entry: null });
    // We don't bind the exact i18n string — just look for the visible
    // headline text from the en locale table.
    expect(getByText('Pick an entry')).toBeInTheDocument();
  });

  it('dispatches runReplaySim with the entry id on button click', async () => {
    const entry = makeEntry('abc-123');
    let received: string | null = null;
    const { getByText } = render(ActionDrawer, {
      props: { entry, otherRecentIds: [] },
      events: {
        runReplaySim: (e: CustomEvent<{ id: string }>) => {
          received = e.detail.id;
        }
      }
    });
    const btn = getByText('Run replay simulation');
    await fireEvent.click(btn);
    expect(received).toBe('abc-123');
  });

  it('flips the confidence pill copy with the replaySim result', () => {
    const entry = makeEntry();
    const safeSim: DLQReplaySimResponse = {
      entry_id: entry.id,
      pipeline_id: 'pipe-1',
      revision_number: 3,
      would_succeed: true,
      stage_runs: []
    };
    const { getByText, rerender } = render(ActionDrawer, {
      entry,
      otherRecentIds: [],
      replaySim: safeSim
    });
    expect(getByText('Retry safe — would succeed')).toBeInTheDocument();

    const unsafeSim: DLQReplaySimResponse = {
      entry_id: entry.id,
      pipeline_id: 'pipe-1',
      revision_number: 3,
      would_succeed: false,
      stage_runs: [],
      failing_stage: 'translate'
    };
    rerender({ entry, otherRecentIds: [], replaySim: unsafeSim });
    // The unsafe pill weaves the failing stage name in.
    const el = document.querySelector('[data-tone="danger"]');
    expect(el).not.toBeNull();
    expect(el?.textContent).toContain('Retry would fail again');
    expect(el?.textContent).toContain('translate');
  });
});
