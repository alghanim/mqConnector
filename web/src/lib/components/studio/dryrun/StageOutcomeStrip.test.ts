// StageOutcomeStrip tests — the horizontal per-stage outcome row.
// Five cases cover the contract:
//   1. Renders one cell per run.
//   2. Duration tiers (fast/normal/slow/very-slow) map to the right
//      pill class based on the spec thresholds.
//   3. A failed cell renders the error inline AND greys downstream
//      cells.
//   4. Clicking a non-first cell opens the PayloadDiffView modal.
//   5. Dispatches `overlays` with stageIds zipped from the stages
//      prop (positional zip — executor walks stages in stage_order).
import { afterEach, describe, expect, it } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import StageOutcomeStrip from './StageOutcomeStrip.svelte';
import type { StageRun, CanvasOverlay } from './types';
import type { Stage } from '$lib/api';

function makeRun(over: Partial<StageRun> = {}): StageRun {
  return {
    name: 'filter',
    duration_ns: 5_000_000, // 5 ms — 'normal' tier
    failed: false,
    body: '{"k":"v"}',
    format: 'json',
    err: '',
    ...over
  };
}

function makeStage(id: string, order: number, type: Stage['stage_type'] = 'filter'): Stage {
  return { id, stage_order: order, stage_type: type, stage_config: '{}', enabled: true };
}

afterEach(() => {
  // Diff modal may have been opened; reset overflow guard set by Dialog.
  document.documentElement.style.overflow = '';
});

describe('StageOutcomeStrip', () => {
  it('renders one cell per run', () => {
    const runs = [makeRun({ name: 'a' }), makeRun({ name: 'b' }), makeRun({ name: 'c' })];
    const { container } = render(StageOutcomeStrip, { runs });
    expect(container.querySelectorAll('.strip-cell').length).toBe(3);
  });

  it('duration pill class reflects the latency tier', () => {
    const runs = [
      makeRun({ name: 'fast', duration_ns: 1_000_000 }), // 1 ms → fast
      makeRun({ name: 'normal', duration_ns: 25_000_000 }), // 25 ms → normal
      makeRun({ name: 'slow', duration_ns: 120_000_000 }), // 120 ms → slow
      makeRun({ name: 'very-slow', duration_ns: 400_000_000 }) // 400 ms → very-slow
    ];
    const { container } = render(StageOutcomeStrip, { runs });
    const cells = container.querySelectorAll('.strip-cell');
    expect(cells[0].getAttribute('data-tier')).toBe('fast');
    expect(cells[1].getAttribute('data-tier')).toBe('normal');
    expect(cells[2].getAttribute('data-tier')).toBe('slow');
    expect(cells[3].getAttribute('data-tier')).toBe('very-slow');
  });

  it('failed cell renders the error and greys downstream cells', () => {
    const runs = [
      makeRun({ name: 'a' }),
      makeRun({ name: 'b', failed: true, err: 'boom' }),
      makeRun({ name: 'c' })
    ];
    const { container, getByText } = render(StageOutcomeStrip, { runs });
    // err is rendered inline on the failed cell
    expect(getByText('boom')).toBeInTheDocument();
    const cells = container.querySelectorAll('.strip-cell');
    expect(cells[1].classList.contains('is-failed')).toBe(true);
    // downstream (idx > 1) cells are greyed
    expect(cells[2].classList.contains('is-downstream')).toBe(true);
    // earlier (idx < 1) cells are NOT greyed
    expect(cells[0].classList.contains('is-downstream')).toBe(false);
  });

  it('clicking a non-first cell opens the diff modal', async () => {
    const runs = [
      makeRun({ name: 'a', body: 'before' }),
      makeRun({ name: 'b', body: 'after' })
    ];
    const { container } = render(StageOutcomeStrip, { runs });
    const cells = container.querySelectorAll('.strip-cell');
    // Initially no dialog mounted by PayloadDiffView (open=false).
    expect(document.querySelector('.dialog')).toBeNull();
    await fireEvent.click(cells[1]);
    // The diff modal renders Dialog with role="dialog" once open.
    await waitFor(() => expect(document.querySelector('[role="dialog"]')).not.toBeNull());
  });

  it('dispatches overlays with stageIds zipped from stages by index', async () => {
    const runs = [
      makeRun({ name: 'filter', duration_ns: 2_000_000, failed: false }),
      makeRun({ name: 'route', duration_ns: 30_000_000, failed: true, err: 'x' })
    ];
    const stages = [makeStage('s-id-1', 1, 'filter'), makeStage('s-id-2', 2, 'route')];
    let received: CanvasOverlay[] | null = null;
    // The reactive $: in the component fires on mount with whatever
    // props we pass, so register the handler via `events` so it's wired
    // before the first dispatch.
    render(StageOutcomeStrip, {
      props: { runs, stages },
      events: {
        overlays: (e: CustomEvent<CanvasOverlay[]>) => (received = e.detail)
      }
    });
    await waitFor(() => expect(received).not.toBeNull());
    expect(received).toEqual([
      { stageId: 's-id-1', failed: false, durationMs: 2 },
      { stageId: 's-id-2', failed: true, durationMs: 30 }
    ]);
  });
});
