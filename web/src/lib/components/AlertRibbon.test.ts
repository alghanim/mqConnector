import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import AlertRibbon from './AlertRibbon.svelte';

// Stub global fetch with a recorder so the component's onMount poll
// hits a controllable response. AlertRibbon goes through the thin
// $lib/api client which internally calls fetch.
function stubFetch(payload: unknown, status = 200) {
  globalThis.fetch = vi.fn(async () =>
    new Response(JSON.stringify(payload), {
      status,
      headers: { 'Content-Type': 'application/json' }
    })
  ) as unknown as typeof fetch;
}

beforeEach(() => {
  // Real timers — we await the onMount poll directly via waitFor;
  // fake timers would require advancing manually for every promise
  // resolution and the readability loss isn't worth it.
});
afterEach(() => {
  vi.restoreAllMocks();
});

describe('AlertRibbon', () => {
  it('renders nothing when there are no firing alerts', async () => {
    stubFetch({
      generated_at: '2026-05-26T00:00:00Z',
      total: 0,
      alerts: [],
      evaluator_enabled: true
    });
    const { queryByTestId } = render(AlertRibbon);
    // Brief delay for onMount fetch to resolve.
    await new Promise((r) => setTimeout(r, 5));
    expect(queryByTestId('alert-ribbon')).toBeNull();
  });

  it('renders nothing when the SLO evaluator is disabled', async () => {
    stubFetch({
      generated_at: '2026-05-26T00:00:00Z',
      total: 0,
      alerts: [],
      evaluator_enabled: false
    });
    const { queryByTestId } = render(AlertRibbon);
    await new Promise((r) => setTimeout(r, 5));
    expect(queryByTestId('alert-ribbon')).toBeNull();
  });

  it('renders a warning ribbon for one warning alert', async () => {
    stubFetch({
      generated_at: '2026-05-26T00:00:00Z',
      total: 1,
      evaluator_enabled: true,
      alerts: [
        {
          name: 'FailRate',
          severity: 'warning',
          value: 0.12,
          started_at: '2026-05-26T00:00:00Z',
          annotations: { summary: 'high failure rate' }
        }
      ]
    });
    const { findByTestId, getByText } = render(AlertRibbon);
    const el = await findByTestId('alert-ribbon');
    expect(el.className).toMatch(/ribbon-warning/);
    expect(getByText('high failure rate')).toBeInTheDocument();
  });

  it('renders a danger ribbon when ANY alert is critical', async () => {
    stubFetch({
      generated_at: '2026-05-26T00:00:00Z',
      total: 2,
      evaluator_enabled: true,
      alerts: [
        {
          name: 'PipelineDown',
          severity: 'critical',
          value: 0,
          started_at: '2026-05-26T00:00:00Z',
          annotations: { summary: 'pipeline down' }
        },
        {
          name: 'FailRate',
          severity: 'warning',
          value: 0.1,
          started_at: '2026-05-26T00:00:00Z'
        }
      ]
    });
    const { findByTestId } = render(AlertRibbon);
    const el = await findByTestId('alert-ribbon');
    expect(el.className).toMatch(/ribbon-danger/);
  });

  it('disappears after the user dismisses it (session-only)', async () => {
    stubFetch({
      generated_at: '2026-05-26T00:00:00Z',
      total: 1,
      evaluator_enabled: true,
      alerts: [
        {
          name: 'X',
          severity: 'warning',
          value: 1,
          started_at: '2026-05-26T00:00:00Z',
          annotations: { summary: 'hi' }
        }
      ]
    });
    const { findByTestId, queryByTestId, getByLabelText } = render(AlertRibbon);
    await findByTestId('alert-ribbon');
    const close = getByLabelText('Dismiss alerts banner');
    await fireEvent.click(close);
    // After dismiss, the ribbon vanishes.
    await waitFor(() => expect(queryByTestId('alert-ribbon')).toBeNull());
  });
});
