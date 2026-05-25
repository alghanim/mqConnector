// ClusterCard — render-time behaviour tests.
//
// What we cover:
//   1. AI title wins when ai_name is present.
//   2. Template falls back when no AI name is set.
//   3. The count Badge renders with the cluster.count value.
//   4. Click dispatches a `select` event with the cluster.

import { describe, it, expect } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import ClusterCard from './ClusterCard.svelte';
import type { DLQCluster } from '$lib/api';

function makeCluster(overrides: Partial<DLQCluster> = {}): DLQCluster {
  return {
    fingerprint: 'abcdef0123456789',
    template: 'connection refused at upstream broker',
    count: 42,
    first_seen: new Date(Date.now() - 3600 * 1000).toISOString(),
    last_seen: new Date(Date.now() - 60 * 1000).toISOString(),
    pipelines_affected: ['pipe-a', 'pipe-b'],
    failing_stages: ['translate'],
    representative_id: 'entry-1',
    recent_ids: ['entry-1', 'entry-2'],
    ...overrides
  };
}

describe('ClusterCard', () => {
  it('renders the AI title when ai_name is supplied', () => {
    const cluster = makeCluster({
      ai_name: { title: 'Broker handshake failure', summary: '', suggestion: '' },
      ai_source: 'ai'
    });
    const { getByText, container } = render(ClusterCard, { cluster });
    expect(getByText('Broker handshake failure')).toBeInTheDocument();
    // The "AI" badge should be present.
    expect(container.querySelector('[data-source="ai"]')).not.toBeNull();
  });

  it('falls back to the cluster template when no AI name is present', () => {
    const cluster = makeCluster();
    const { getByText, container } = render(ClusterCard, { cluster });
    expect(getByText('connection refused at upstream broker')).toBeInTheDocument();
    // No badge of either kind.
    expect(container.querySelector('[data-source]')).toBeNull();
  });

  it('renders the count badge with the cluster.count value', () => {
    const cluster = makeCluster({ count: 137 });
    const { getByText } = render(ClusterCard, { cluster });
    expect(getByText('137')).toBeInTheDocument();
  });

  it('dispatches a select event on click carrying the cluster', async () => {
    const cluster = makeCluster();
    let received: DLQCluster | null = null;
    const { container } = render(ClusterCard, {
      props: { cluster },
      events: {
        select: (event: CustomEvent<{ cluster: DLQCluster }>) => {
          received = event.detail.cluster;
        }
      }
    });
    const button = container.querySelector('[data-testid="cluster-card"]');
    expect(button).not.toBeNull();
    await fireEvent.click(button as Element);
    expect(received).not.toBeNull();
    expect((received as unknown as DLQCluster).fingerprint).toBe(cluster.fingerprint);
  });
});
