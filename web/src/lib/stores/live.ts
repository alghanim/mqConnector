// Single shared SSE connection for the whole tab.
//
// Multiple pages need the same event stream — the DLQ badge in the
// layout, the operations dashboard at `/`, future tenant/metric views.
// Opening one EventSource per consumer wastes a TCP slot and forces
// the server to compute the same snapshot twice. This store opens at
// most one stream and exposes Svelte stores for each event type.
//
// Lifecycle:
//   • The first subscriber to any derived store triggers connect().
//   • The stream stays open until disconnect() is called explicitly
//     (we tie that to the auth user dropping out — no point streaming
//     when there's nobody logged in).
//   • If SSE gives up, `liveMode` flips to false and consumers can
//     decide whether to fall back to polling locally.

import { writable, type Writable } from 'svelte/store';
import type { PipelineMetric, Health } from '$lib/api';
import {
  openEventsStream,
  type SSEClient,
  type SSEMetricsPayload,
  type SSEDLQPayload,
  type SSEHealthPayload
} from '$lib/sse';

export interface MetricsSnapshot {
  uptime: string;
  pipelines: PipelineMetric[];
  active: number;
  receivedAt: number;
}

export const metrics: Writable<MetricsSnapshot | null> = writable(null);
export const dlqTotal: Writable<number> = writable(0);
export const health: Writable<Health | null> = writable(null);
/** True while SSE is delivering frames. Flips false on hard fallback. */
export const liveMode: Writable<boolean> = writable(false);

let client: SSEClient | null = null;

/**
 * Open the shared stream. Safe to call repeatedly — only one underlying
 * SSE connection is kept. `intervalMs` is forwarded to the server.
 */
export function connect(intervalMs = 2000) {
  if (client) return;

  client = openEventsStream(intervalMs, {
    onFallback: () => liveMode.set(false),
    onResume: () => liveMode.set(true)
  })
    .on<SSEMetricsPayload<PipelineMetric>>('metrics', (m) => {
      metrics.set({
        uptime: m.uptime,
        pipelines: Object.values(m.pipelines || {}),
        active: m.active,
        receivedAt: Date.now()
      });
      liveMode.set(true);
    })
    .on<SSEDLQPayload>('dlq_total', (d) => {
      dlqTotal.set(d.total);
    })
    .on<SSEHealthPayload>('health', (h) => {
      health.set({
        status: h.status,
        version: h.version,
        db_status: 'ok',
        uptime: h.uptime,
        active_pipelines: h.active_pipelines,
        connections: h.connections ?? []
      } as Health);
    });

  client.start();
}

/** Tear the stream down. Call on logout. */
export function disconnect() {
  client?.stop();
  client = null;
  liveMode.set(false);
}
