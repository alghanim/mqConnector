// ExplanationCard — wire-shape rendering tests.
//
//   1. Facts list renders one row per fact.
//   2. A `stages` section drives the embedded WaterfallStages.
//   3. AI summary chip appears only when aiSource === 'ai'.

import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import type { Explanation } from '$lib/api';
import ExplanationCard from './ExplanationCard.svelte';

function sample(): Explanation {
  return {
    subject: 'latency',
    id: 'pipe-1',
    headline: 'transform dominates p99',
    severity: 'warning',
    facts: [
      { label: 'Avg latency', value: '12.30 ms', source: 'mqconnector_avg_latency_ms' },
      { label: 'Overall p99', value: '42.10 ms' }
    ],
    sections: [
      {
        kind: 'stages',
        title: 'Per-stage latency waterfall',
        data: {
          stages: [
            { name: 'validate', p50_ms: 1, p95_ms: 2, p99_ms: 3 },
            { name: 'transform', p50_ms: 4, p95_ms: 9, p99_ms: 15 }
          ],
          total_p99: 18
        }
      }
    ],
    as_of: '2026-05-24T12:00:00Z',
    sources: ['metrics.Snapshot']
  };
}

describe('ExplanationCard', () => {
  it('renders one row per fact', () => {
    const { container } = render(ExplanationCard, {
      explanation: sample()
    });
    const rows = container.querySelectorAll('.exp-fact');
    expect(rows.length).toBe(2);
    expect(rows[0].textContent).toContain('Avg latency');
    expect(rows[0].textContent).toContain('12.30 ms');
  });

  it('renders a stages section via WaterfallStages', () => {
    const { container } = render(ExplanationCard, {
      explanation: sample()
    });
    const section = container.querySelector('.exp-section[data-kind="stages"]');
    expect(section).not.toBeNull();
    // The WaterfallStages primitive emits one .wf-row per stage.
    const rows = container.querySelectorAll('.wf-row');
    expect(rows.length).toBe(2);
  });

  it('shows AI summary chip only when aiSource is "ai"', () => {
    const exp = sample();
    const { container: with_ai } = render(ExplanationCard, {
      explanation: exp,
      aiSummary: 'The transform stage is responsible for most of the tail.',
      aiSource: 'ai'
    });
    expect(with_ai.querySelector('[data-testid="explanation-ai"]')).not.toBeNull();
    expect(with_ai.textContent).toContain('transform stage is responsible');

    const { container: without_ai } = render(ExplanationCard, {
      explanation: exp,
      aiSummary: '',
      aiSource: 'deterministic'
    });
    expect(without_ai.querySelector('[data-testid="explanation-ai"]')).toBeNull();
  });
});
