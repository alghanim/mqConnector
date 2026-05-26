// AnomalyMarker — primitive overlay tests.
//
// AnomalyMarker emits an <svg><g></g></svg>-shaped fragment; testing-
// library renders it under a wrapper <div>. Browsers + jsdom both
// keep stray <g> inside a non-SVG container valid for queries, so we
// can still introspect by class.
//
//   1. One <polygon> per marker.
//   2. data-severity attributes flow through (drives colour at CSS).

import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import AnomalyMarker from './AnomalyMarker.svelte';

describe('AnomalyMarker', () => {
  const markers = [
    { t: 0, label: 'circuit opened', severity: 'critical' as const },
    { t: 5, label: 'drift spike', severity: 'warning' as const },
    { t: 9, label: 'normal probe', severity: 'info' as const }
  ];

  it('renders one triangle per marker', () => {
    const { container } = render(AnomalyMarker, {
      markers,
      xScale: (t: number) => t * 10,
      y: 4
    });
    const polys = container.querySelectorAll('polygon');
    expect(polys.length).toBe(3);
  });

  it('annotates each marker with its severity', () => {
    const { container } = render(AnomalyMarker, {
      markers,
      xScale: (t: number) => t * 10
    });
    const groups = container.querySelectorAll<SVGGElement>('.anomaly-mark');
    expect(groups.length).toBe(3);
    const sevs = Array.from(groups).map((g) => g.getAttribute('data-severity'));
    expect(sevs).toEqual(['critical', 'warning', 'info']);
  });
});
