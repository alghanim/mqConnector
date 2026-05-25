// Shared catalogue helpers — pipelines + connections lookup maps.
//
// Several read-only surfaces (overview dashboard, /metrics, /dlq, and
// the per-pipeline drilldown panels) all need to render the friendly
// pipeline NAME (and source / destination broker metadata) for a row
// that the live API hands them keyed only by pipeline_id UUID.
//
// The pattern was originally embedded in /+page.svelte (commit
// 8bb6d29) and copied verbatim into /metrics (5bb328e). DLQ becomes
// the third consumer, so the helpers move here per the standing
// "lift on the second copy" rule.
//
// Design notes:
//   • Maps are returned, NOT stored as a Svelte store. Each consumer
//     keeps its own reactive `let pipelineMap = new Map(...)` so the
//     existing reactive {#each} blocks re-render unchanged when the
//     catalogue lands — no migration of caller code beyond swapping
//     the local fetch body for a call here.
//   • Best-effort: failures resolve to empty maps + a console.warn.
//     The caller's UI then degrades to "id:<short>" / raw queue
//     strings rather than going blank.
//   • No timer / polling. Callers decide cadence — the dashboard
//     wants 15 s, the studio wants on-mount only, etc.
//   • No `any` — all helpers thread the Maps in explicitly so Svelte's
//     dependency tracking inside templates stays accurate. Plain
//     module-level reads from inside a reactive {#each} do NOT
//     subscribe; passing the map in does.

import { api, type Connection, type ConnectionType, type Pipeline } from '$lib/api';

export interface Catalogues {
  pipelines: Map<string, Pipeline>;
  connections: Map<string, Connection>;
}

/**
 * Fetch /v1/pipelines + /v1/connections in parallel and return the
 * resulting lookup maps. Best-effort: a failed endpoint contributes an
 * empty map instead of throwing, so the caller's UI never goes blank.
 *
 * @param scope label used in the console.warn message — usually the
 *   page name ("dashboard", "metrics", "dlq") so the warning identifies
 *   which surface degraded.
 */
export async function loadCatalogues(scope: string): Promise<Catalogues> {
  const [pipes, conns] = await Promise.allSettled([
    api.get<Pipeline[]>('/v1/pipelines').then((v) => v ?? []),
    api.get<Connection[]>('/v1/connections').then((v) => v ?? [])
  ]);
  const pipelines = new Map<string, Pipeline>();
  const connections = new Map<string, Connection>();
  if (pipes.status === 'fulfilled') {
    for (const p of pipes.value) if (p.id) pipelines.set(p.id, p);
  } else {
    console.warn(`${scope}: failed to load pipelines catalogue`, pipes.reason);
  }
  if (conns.status === 'fulfilled') {
    for (const c of conns.value) if (c.id) connections.set(c.id, c);
  } else {
    console.warn(`${scope}: failed to load connections catalogue`, conns.reason);
  }
  return { pipelines, connections };
}

/**
 * Resolve pipeline_id → human label. Falls back to a short id slice
 * prefixed with `id:` so resolution failure is visible without
 * overwhelming the cell.
 *
 * Maps MUST be passed in explicitly (not read from a module-level let)
 * so reactive {#each} blocks that call this helper subscribe to map
 * changes — without the parameter, Svelte's reactivity tracker has no
 * dependency to wire up.
 */
export function pipelineLabel(id: string, map: Map<string, Pipeline>): string {
  const p = map.get(id);
  if (p?.name) return p.name;
  return `id:${id.slice(0, 8)}`;
}

/**
 * Same as pipelineLabel but returns a caller-supplied "deleted"
 * placeholder when the pipeline_id is missing from the catalogue
 * entirely. Used on the DLQ list where rows can outlive their
 * pipeline (deleted-then-replayed messages).
 *
 * The placeholder string is passed in (rather than read from i18n
 * here) so this helper stays locale-free and equally testable.
 */
export function pipelineLabelOrDeleted(
  id: string | undefined,
  map: Map<string, Pipeline>,
  deletedLabel: string,
  emptyLabel = '—'
): string {
  if (!id) return emptyLabel;
  const p = map.get(id);
  if (p?.name) return p.name;
  return deletedLabel;
}

/**
 * Resolve the source / destination connection for a given pipeline_id.
 * Returns null when the chain can't be completed; callers typically
 * degrade to the queue / topic string already on the metric record.
 */
export function endpointFor(
  pipelineId: string,
  end: 'source' | 'destination',
  pMap: Map<string, Pipeline>,
  cMap: Map<string, Connection>
): Connection | null {
  const p = pMap.get(pipelineId);
  if (!p) return null;
  const connId = end === 'source' ? p.source_id : p.destination_id;
  if (!connId) return null;
  return cMap.get(connId) ?? null;
}

export function endpointType(
  pipelineId: string,
  end: 'source' | 'destination',
  pMap: Map<string, Pipeline>,
  cMap: Map<string, Connection>
): ConnectionType | undefined {
  return endpointFor(pipelineId, end, pMap, cMap)?.type;
}

export function endpointName(
  pipelineId: string,
  end: 'source' | 'destination',
  pMap: Map<string, Pipeline>,
  cMap: Map<string, Connection>
): string | null {
  return endpointFor(pipelineId, end, pMap, cMap)?.name ?? null;
}
