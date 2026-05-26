// WaterfallStages — primitive chart tests.
//
//   1. One row per stage rendered.
//   2. The largest-p99 row gets the dominant attribute / outline.
//   3. Clicking a row dispatches a `select` event with the stage name.

import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import WaterfallStages from './WaterfallStages.svelte';

describe('WaterfallStages', () => {
  const stages = [
    { name: 'validate', p50_ms: 1, p95_ms: 2, p99_ms: 3 },
    { name: 'transform', p50_ms: 4, p95_ms: 8, p99_ms: 12 },
    { name: 'route', p50_ms: 2, p95_ms: 5, p99_ms: 7 }
  ];

  it('renders one row per stage', () => {
    const { container } = render(WaterfallStages, {
      stages,
      total_p99_ms: 22
    });
    const rows = container.querySelectorAll('.wf-row');
    expect(rows.length).toBe(3);
  });

  it('marks the stage with the largest p99 as dominant', () => {
    const { container } = render(WaterfallStages, {
      stages,
      total_p99_ms: 22,
      highlightDominantStage: true
    });
    const rows = container.querySelectorAll<HTMLButtonElement>('.wf-row');
    // transform has p99=12, the max.
    const dominant = Array.from(rows).find(
      (r) => r.getAttribute('data-stage') === 'transform'
    );
    expect(dominant?.getAttribute('data-dominant')).toBe('true');
    const nondominant = Array.from(rows).find(
      (r) => r.getAttribute('data-stage') === 'validate'
    );
    expect(nondominant?.getAttribute('data-dominant')).toBe('false');
    // Dominant badge text.
    expect(dominant?.querySelector('.wf-dom-badge')).not.toBeNull();
  });

  it('emits select with stageName on row click', async () => {
    const onSelect = vi.fn();
    const { container } = render(WaterfallStages, {
      props: { stages, total_p99_ms: 22 },
      events: {
        select: (e: Event) => onSelect((e as CustomEvent<{ stageName: string }>).detail)
      }
    });
    const row = container.querySelector<HTMLButtonElement>(
      '.wf-row[data-stage="route"]'
    );
    expect(row).not.toBeNull();
    if (row) await fireEvent.click(row);
    expect(onSelect).toHaveBeenCalledWith({ stageName: 'route' });
  });
});
