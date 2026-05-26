// Heatmap — rendering + tier-classification tests.
//
// What we assert:
//   1. Given a 168-entry array, we render exactly 168 <rect> cells.
//   2. A non-zero value lands at a non-zero tier, and the max value
//      lands at tier 4 (the hottest tone).
//   3. An empty bucket (value 0) renders as tier 0 (background tone).
//   4. The `label` prop, when provided, renders as a caption.
//
// We don't assert exact colours — those are CSS tokens and theming is
// the brand-tokens test's job. The data-tier attribute is the contract:
// styles flow from it. Same pattern Sparkline + TopologyGraph use.

import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import Heatmap from './Heatmap.svelte';

function makeBuckets(fill: (i: number) => number): number[] {
  const out = new Array<number>(168);
  for (let i = 0; i < 168; i++) out[i] = fill(i);
  return out;
}

describe('Heatmap', () => {
  it('renders 168 cells given a full week of buckets', () => {
    const { container } = render(Heatmap, {
      buckets: makeBuckets(() => 0)
    });
    const cells = container.querySelectorAll('rect.heatmap-cell');
    expect(cells.length).toBe(168);
  });

  it('maps higher values to higher tier buckets', () => {
    // One bucket at the max, rest empty — that bucket should land at
    // tier 4 (top quintile) and the empties at tier 0.
    const buckets = makeBuckets((i) => (i === 100 ? 50 : 0));
    const { container } = render(Heatmap, { buckets });
    const cells = container.querySelectorAll<SVGRectElement>('rect.heatmap-cell');
    const peak = cells[100];
    expect(peak?.getAttribute('data-tier')).toBe('4');
    expect(peak?.getAttribute('data-value')).toBe('50');
    // Spot-check a known-empty cell stays at tier 0.
    const cold = cells[0];
    expect(cold?.getAttribute('data-tier')).toBe('0');
  });

  it('uses tier 0 (background tone) for empty buckets', () => {
    // All zero → every cell at tier 0.
    const { container } = render(Heatmap, {
      buckets: makeBuckets(() => 0)
    });
    const cells = container.querySelectorAll<SVGRectElement>('rect.heatmap-cell');
    for (const c of cells) {
      expect(c.getAttribute('data-tier')).toBe('0');
    }
  });

  it('renders the label when one is provided', () => {
    const { container } = render(Heatmap, {
      buckets: makeBuckets(() => 0),
      label: 'Last 7 days'
    });
    const caption = container.querySelector('.heatmap-label');
    expect(caption?.textContent).toBe('Last 7 days');
  });
});
