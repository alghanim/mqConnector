// PercentileBand — primitive chart tests.
//
// We assert structural contracts, not pixel-perfect rendering:
//   1. Overtime mode renders three <polyline> elements (one per tier)
//      when given a series.
//   2. Snapshot mode renders three tick <line> elements with the right
//      data-tier attributes.
//   3. An empty input renders the "no data" placeholder rather than
//      crashing or producing degenerate SVG.

import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import PercentileBand from './PercentileBand.svelte';

describe('PercentileBand', () => {
  it('renders three percentile polylines in overtime mode', () => {
    const series = [
      { p50: 5, p95: 12, p99: 18 },
      { p50: 6, p95: 15, p99: 22 },
      { p50: 7, p95: 14, p99: 20 }
    ];
    const { container } = render(PercentileBand, {
      mode: 'overtime',
      series,
      width: 320,
      height: 80
    });
    const lines = container.querySelectorAll('polyline.pband-line');
    expect(lines.length).toBe(3);
    const tiers = Array.from(lines).map((l) => l.getAttribute('data-tier'));
    expect(tiers).toEqual(expect.arrayContaining(['p50', 'p95', 'p99']));
  });

  it('renders three tick lines in snapshot mode', () => {
    const { container } = render(PercentileBand, {
      mode: 'snapshot',
      point: { p50: 4, p95: 11, p99: 20 },
      width: 240,
      height: 50
    });
    const ticks = container.querySelectorAll('line.pband-tick');
    expect(ticks.length).toBe(3);
    const tiers = Array.from(ticks).map((l) => l.getAttribute('data-tier'));
    expect(tiers).toEqual(['p50', 'p95', 'p99']);
  });

  it('renders an empty placeholder for empty input', () => {
    const { container } = render(PercentileBand, {
      mode: 'overtime',
      series: [],
      width: 240,
      height: 60
    });
    expect(container.querySelector('.pband-empty')).not.toBeNull();
    expect(container.querySelectorAll('polyline.pband-line').length).toBe(0);
  });
});
