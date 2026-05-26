// Studio store — central state for the Pipeline Studio (the visual editor
// at /pipelines/{id}/studio). Holds the loaded baseline + working draft,
// the most recent revisions for the version rail, and the high-level
// state-machine state (empty | building | dirty | validating | deploying
// | error | simulating | version-comparing) that drives the chrome.
//
// Wave 1 / Task 8 — chrome only. This file ships:
//
//   - The TypeScript shapes that the canvas, inspector, dock, and rail
//     all read against. Subsequent tasks (9/10/11/12) add granular
//     mutations (addStage / removeStage / patchStage etc.) on top of the
//     primitives here; for Task 8 the only mutation a consumer can run
//     is `markDirty`, which bumps the dirty counter and flips the state
//     to 'dirty'. The granular mutations land alongside the components
//     that need them.
//
//   - `hydrate(pipelineId)` — five parallel fetches that assemble the
//     baseline + draft. Treats a 404 from /revisions/current as success
//     (a brand-new pipeline has no deployed revision yet) and a partial
//     /revisions response (empty list) as success too. Any other fetch
//     failure flips state='error' and surfaces the message — the route
//     renders an Alert.
import { writable, get, type Readable } from 'svelte/store';
import {
  api,
  type Pipeline,
  type Stage,
  type Transform,
  type RoutingRule,
  type ApiError
} from '$lib/api';

// ─── State machine ──────────────────────────────────────────────────────

export type StudioState =
  | 'empty'             // before hydration — store has no data
  | 'building'          // hydrated, no pending edits
  | 'dirty'             // pending edits relative to baseline
  | 'validating'        // /preview or schema check in flight
  | 'deploying'         // /deploy in flight
  | 'error'             // last operation failed; see `error`
  | 'simulating'        // dry-run result on screen
  | 'version-comparing'; // diff viewer open against another rev

// ─── Wire shapes ────────────────────────────────────────────────────────
//
// PipelineRevision mirrors `internal/storage/models.go` PipelineRevision
// with `snapshot` swapped for a parsed PipelineSnapshot per the server's
// revisionResponse envelope (handlers_pipeline_revisions.go).

export interface PipelineSnapshot {
  pipeline: Pipeline | null;
  stages: Stage[];
  transforms: Transform[];
  routing_rules: RoutingRule[];
  snapshot_schema_version?: number;
}

export interface PipelineRevision {
  id: string;
  tenant_id: string;
  pipeline_id: string;
  revision_number: number;
  snapshot: PipelineSnapshot | null;
  snapshot_hash: string;
  author_sub: string;
  author_username: string;
  change_summary: string;
  created_at: string;
  deployed_at?: string | null;
  deploy_request_id?: string;
}

// Local, in-memory studio snapshot. Distinct from PipelineSnapshot
// (which is the server's storage-format shape) so we can hold and edit
// data with the same naming conventions the rest of the UI uses
// (`routingRules` is the JS convention; `routing_rules` is the wire
// convention).
export interface StudioSnapshot {
  pipeline: Pipeline;
  stages: Stage[];
  transforms: Transform[];
  routingRules: RoutingRule[];
}

// The Studio's complete state. The store is a single writable<this> so
// that derivations and components can subscribe once and re-render on any
// change without needing to mix in multiple stores.
export interface StudioStateData {
  pipelineId: string;
  state: StudioState;
  baseline: StudioSnapshot | null;
  draft: StudioSnapshot | null;
  deployedRev: PipelineRevision | null;
  latestRev: PipelineRevision | null;
  revisions: PipelineRevision[];
  error: string | null;
  dirtyCount: number;
  selectedNodeId: string | null;
  comparison: ComparisonState | null;
  dryRun: unknown | null;
  // dockError: dry-run-specific error channel. Distinct from `error` so a
  // dry-run failure (typically "build: foo" or "stage X failed") doesn't
  // pollute the build-error chip in the header. Task 11.
  dockError: string | null;
}

export interface ComparisonState {
  from: number;
  to: number;
  diff: unknown;
}

// emptyData returns the canonical initial state. Exposed for tests so
// they can assert against the documented shape rather than re-deriving
// it each time.
export function emptyData(pipelineId: string): StudioStateData {
  return {
    pipelineId,
    state: 'empty',
    baseline: null,
    draft: null,
    deployedRev: null,
    latestRev: null,
    revisions: [],
    error: null,
    dirtyCount: 0,
    selectedNodeId: null,
    comparison: null,
    dryRun: null,
    dockError: null
  };
}

// Deep-clone a snapshot. Used when resetting the draft back to the
// baseline — without a clone, edits to the draft would mutate the
// baseline too (arrays + objects are shared by reference). structuredClone
// covers all the JSON-safe shapes we hold (Pipeline / Stage / etc.).
function cloneSnapshot(snap: StudioSnapshot): StudioSnapshot {
  return {
    pipeline: structuredClone(snap.pipeline),
    stages: structuredClone(snap.stages),
    transforms: structuredClone(snap.transforms),
    routingRules: structuredClone(snap.routingRules)
  };
}

// Default page size for the version rail. 25 matches the backend's
// defaultRevisionListLimit so we get a full first page on hydrate.
const REVISION_PAGE_LIMIT = 25;

interface RevisionsListResponse {
  revisions: PipelineRevision[];
  total: number;
  limit: number;
  offset: number;
}

// Internal store. Exposed only via the `studio` facade below so callers
// can't bypass the API surface (which is what keeps the state machine
// coherent — if anyone could call `inner.set` they could leave the
// store in an inconsistent state).
const inner = writable<StudioStateData>(emptyData(''));

// studioState is a read-only Readable for components that only need to
// subscribe; matches the convention used by `auth` / `tenants` / `live`.
export const studioState: Readable<StudioStateData> = { subscribe: inner.subscribe };

// hydrate kicks off the five parallel fetches that assemble the studio
// state. Order is intentional:
//
//   pipeline   — required; failure aborts the whole hydrate
//   stages     — required
//   transforms — required
//   rules      — required
//   revisions  — best-effort (a new pipeline has none; empty is fine)
//   current    — best-effort (404 is the documented response for "no
//                deployed revision yet")
//
// We run them all in parallel for latency, then post-process to keep the
// error semantics above. Any required-fetch failure leaves the store in
// state='error' with `error` populated; the route surfaces the message.
async function hydrate(pipelineId: string): Promise<void> {
  inner.set({ ...emptyData(pipelineId), state: 'empty' });

  type Settled<T> = { ok: true; value: T } | { ok: false; err: ApiError };
  const settle = async <T>(p: Promise<T>): Promise<Settled<T>> => {
    try {
      return { ok: true, value: await p };
    } catch (e) {
      return { ok: false, err: e as ApiError };
    }
  };

  const [
    pipelineRes,
    stagesRes,
    transformsRes,
    rulesRes,
    revisionsRes,
    currentRes
  ] = await Promise.all([
    settle(api.get<Pipeline>(`/v1/pipelines/${pipelineId}`)),
    settle(api.get<Stage[]>(`/v1/pipelines/${pipelineId}/stages`)),
    settle(api.get<Transform[]>(`/v1/pipelines/${pipelineId}/transforms`)),
    settle(api.get<RoutingRule[]>(`/v1/pipelines/${pipelineId}/routing-rules`)),
    settle(
      api.get<RevisionsListResponse>(
        `/v1/pipelines/${pipelineId}/revisions?limit=${REVISION_PAGE_LIMIT}`
      )
    ),
    settle(api.get<PipelineRevision>(`/v1/pipelines/${pipelineId}/revisions/current`))
  ]);

  // Required fetches: pipeline + stages + transforms + rules. Any one
  // failing makes the whole studio unrenderable, so we surface the
  // first failure verbatim. The order matches the order of the array
  // above so the message the user sees is deterministic.
  const required = [pipelineRes, stagesRes, transformsRes, rulesRes] as const;
  for (const r of required) {
    if (!r.ok) {
      inner.set({
        ...emptyData(pipelineId),
        state: 'error',
        error: r.err?.message || 'failed to load studio'
      });
      return;
    }
  }

  const pipeline = (pipelineRes as { ok: true; value: Pipeline }).value;
  const stages = ((stagesRes as { ok: true; value: Stage[] }).value ?? []).slice();
  const transforms = ((transformsRes as { ok: true; value: Transform[] }).value ?? []).slice();
  const rules = ((rulesRes as { ok: true; value: RoutingRule[] }).value ?? []).slice();

  // Sort by the same ordering keys the legacy form uses (matches the
  // server's storage order for stages by stage_order and transforms by
  // order). This keeps the canvas deterministic across loads.
  stages.sort((a, b) => a.stage_order - b.stage_order);
  transforms.sort((a, b) => a.order - b.order);
  rules.sort((a, b) => a.priority - b.priority);

  const baseline: StudioSnapshot = {
    pipeline,
    stages,
    transforms,
    routingRules: rules
  };

  // Best-effort: empty revisions or a 404 current is fine. A non-404
  // failure on either endpoint we treat as soft — log via the error
  // field but DON'T flip state='error', because the studio is still
  // usable with the live tables. Subsequent tasks may surface a
  // banner; for now we just keep the field nullable.
  let revisions: PipelineRevision[] = [];
  let latestRev: PipelineRevision | null = null;
  if (revisionsRes.ok) {
    revisions = revisionsRes.value?.revisions ?? [];
    latestRev = revisions.length > 0 ? revisions[0] : null;
  }

  let deployedRev: PipelineRevision | null = null;
  if (currentRes.ok) {
    deployedRev = currentRes.value;
  } else if (currentRes.err?.status && currentRes.err.status !== 404) {
    // A 5xx on /current is unexpected but non-fatal — the editor
    // still loads from the live tables. We don't promote this to a
    // user-visible error because that would block use of a perfectly
    // valid pipeline; Task 12 (version rail) will display a softer
    // signal when revisions fail to load.
  }

  inner.set({
    pipelineId,
    state: 'building',
    baseline,
    draft: cloneSnapshot(baseline),
    deployedRev,
    latestRev,
    revisions,
    error: null,
    dirtyCount: 0,
    selectedNodeId: null,
    comparison: null,
    dryRun: null,
    dockError: null
  });
}

// markDirty bumps the dirty counter and flips state→'dirty'. Granular
// mutations (Tasks 9/10) call this after applying their change to the
// draft. Doesn't touch state when we're in a non-edit state (deploying,
// validating, etc.) — those will resolve back to 'building'/'dirty' on
// their own via clearError / setState.
function markDirty(): void {
  inner.update((s) => ({
    ...s,
    state: s.state === 'empty' ? 'empty' : 'dirty',
    dirtyCount: s.dirtyCount + 1
  }));
}

// resetDraft throws away pending edits and snaps the working copy back
// to the last-loaded baseline. The CommandPalette's "Discard draft"
// entry calls this; the Deploy success path also resets, but it sets
// the baseline to the new snapshot first (Task 12).
function resetDraft(): void {
  inner.update((s) => {
    if (!s.baseline) return s;
    return {
      ...s,
      draft: cloneSnapshot(s.baseline),
      dirtyCount: 0,
      state: 'building',
      error: null
    };
  });
}

function setState(state: StudioState): void {
  inner.update((s) => ({ ...s, state }));
}

function setError(msg: string): void {
  inner.update((s) => ({ ...s, state: 'error', error: msg }));
}

function clearError(): void {
  inner.update((s) => ({ ...s, state: 'building', error: null }));
}

function selectNode(nodeId: string | null): void {
  inner.update((s) => ({ ...s, selectedNodeId: nodeId }));
}

function setComparison(from: number, to: number, diff: unknown): void {
  inner.update((s) => ({
    ...s,
    state: 'version-comparing',
    comparison: { from, to, diff }
  }));
}

function clearComparison(): void {
  inner.update((s) => ({
    ...s,
    state: s.dirtyCount > 0 ? 'dirty' : 'building',
    comparison: null
  }));
}

// ─── Stage mutations (Task 9) ──────────────────────────────────────────
//
// The canvas + inspector both edit the draft via these helpers rather
// than mutating `draft.stages` directly. Each mutation:
//
//   1. No-ops if there's no draft (pre-hydrate).
//   2. Replaces `draft` with a fresh object (referential change) so
//      Svelte's reactive boundary detects it; mutating in place wouldn't
//      re-trigger derived blocks.
//   3. Calls markDirty() at the end to bump the counter + flip the chip.
//
// The mutations never touch the baseline — the user's only way to send
// the draft to the server is the deploy flow (Task 12).

// StudioStageType — the set of stage types the studio palette can place
// onto the canvas. Extends api.StageType with 'wasm' (backend supports it
// via migrations 0015/0016; the api.ts type predates the plugin work and
// will catch up in a separate cleanup). Kept as a local alias so the
// canvas/palette can reference one symbol.
export type StudioStageType =
  | 'filter'
  | 'transform'
  | 'translate'
  | 'route'
  | 'script'
  | 'validate'
  | 'wasm';

// defaultStageConfig returns a minimal-valid JSON config string for each
// stage type, picked so the stage renders + dry-runs without further
// editing. Keeps addStage from leaving a stage in a broken state on the
// canvas. Mirrors `defaultConfig` in /flow.
function defaultStageConfig(stageType: StudioStageType): string {
  switch (stageType) {
    case 'filter':
      return '{"paths":[]}';
    case 'translate':
      return '{"output_format":"same"}';
    case 'script':
      return '{"script":"// msg.foo = 1\\nmsg;"}';
    case 'validate':
      return '{"schema_id":""}';
    case 'route':
      return '{}';
    case 'transform':
      return '{}';
    case 'wasm':
      return '{"plugin":""}';
    default:
      return '{}';
  }
}

// localStageId generates a client-side id for a freshly-added stage.
// Server replaces it on deploy with a real ULID. The `tmp-` prefix is the
// signal a downstream save path can use to decide whether to POST vs PUT.
function localStageId(): string {
  return `tmp-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}

function addStage(stageType: StudioStageType): string {
  const newId = localStageId();
  inner.update((s) => {
    if (!s.draft) return s;
    const stages = s.draft.stages;
    const maxOrder = stages.reduce((m, st) => (st.stage_order > m ? st.stage_order : m), 0);
    const next: Stage = {
      // The server's Stage type allows `tenant_id` + `pipeline_id` but
      // the JS interface (api.ts) only carries `id` + `pipeline_id`. We
      // leave both empty — the deploy round-trip fills them.
      id: newId,
      pipeline_id: '',
      stage_order: maxOrder + 1,
      stage_type: stageType as Stage['stage_type'],
      stage_config: defaultStageConfig(stageType),
      enabled: true
    };
    return {
      ...s,
      draft: {
        ...s.draft,
        stages: [...stages, next]
      }
    };
  });
  markDirty();
  return newId;
}

function removeStage(stageId: string): void {
  let removed = false;
  inner.update((s) => {
    if (!s.draft) return s;
    const before = s.draft.stages.length;
    const stages = s.draft.stages.filter((st) => st.id !== stageId);
    if (stages.length === before) return s;
    removed = true;
    // Re-number stage_order to keep the chain contiguous (1..N). The
    // server enforces this anyway but renumbering up front keeps the
    // canvas + inspector reading the same indices.
    const renumbered = stages.map((st, i) => ({ ...st, stage_order: i + 1 }));
    const selectedNodeId = s.selectedNodeId === stageId ? null : s.selectedNodeId;
    return {
      ...s,
      draft: { ...s.draft, stages: renumbered },
      selectedNodeId
    };
  });
  if (removed) markDirty();
}

function reorderStages(stageIdsInOrder: string[]): void {
  let changed = false;
  inner.update((s) => {
    if (!s.draft) return s;
    const byId = new Map(s.draft.stages.map((st) => [st.id ?? '', st]));
    const reordered: Stage[] = [];
    for (let i = 0; i < stageIdsInOrder.length; i++) {
      const st = byId.get(stageIdsInOrder[i]);
      if (!st) continue;
      reordered.push({ ...st, stage_order: i + 1 });
    }
    // If the caller's list is missing stages, append them in their
    // existing order so we don't silently drop stages. Defensive — the
    // canvas always passes the full list.
    if (reordered.length !== s.draft.stages.length) {
      const seen = new Set(reordered.map((st) => st.id));
      for (const st of s.draft.stages) {
        if (!seen.has(st.id ?? '')) {
          reordered.push({ ...st, stage_order: reordered.length + 1 });
        }
      }
    }
    // Detect a real change before flipping dirty — re-publishing the
    // same ordering should be a no-op.
    const same =
      reordered.length === s.draft.stages.length &&
      reordered.every((st, i) => st.id === s.draft!.stages[i].id);
    if (same) return s;
    changed = true;
    return {
      ...s,
      draft: { ...s.draft, stages: reordered }
    };
  });
  if (changed) markDirty();
}

function patchStage(stageId: string, patch: Partial<Stage>): void {
  let changed = false;
  inner.update((s) => {
    if (!s.draft) return s;
    let found = false;
    const stages = s.draft.stages.map((st) => {
      if (st.id !== stageId) return st;
      found = true;
      return { ...st, ...patch };
    });
    if (!found) return s;
    changed = true;
    return {
      ...s,
      draft: { ...s.draft, stages }
    };
  });
  if (changed) markDirty();
}

function setDryRun(result: unknown): void {
  inner.update((s) => ({
    ...s,
    state: 'simulating',
    dryRun: result,
    dockError: null
  }));
}

function clearDryRun(): void {
  inner.update((s) => ({
    ...s,
    state: s.dirtyCount > 0 ? 'dirty' : 'building',
    dryRun: null,
    dockError: null
  }));
}

// beginDryRun — flip to 'simulating' and clear any prior dry-run + dock
// error so the spinner replaces a stale strip. The DryRunDock (Task 11)
// calls this before its fetch fires so the chip + Run-button pulse start
// immediately, regardless of how long the network takes. Pinned by
// studio.test.ts.
function beginDryRun(): void {
  inner.update((s) => ({
    ...s,
    state: 'simulating',
    dryRun: null,
    dockError: null
  }));
}

// finishDryRun — store a successful preview result. Keeps state at
// 'simulating' so the chrome reflects "a dry-run is on screen"; the
// operator clears the result explicitly via Clear (clearDryRun) which
// returns to building/dirty. Behaviour matches setDryRun — kept as a
// named alias so the dock's pre/post pair reads symmetrically.
function finishDryRun(result: unknown): void {
  setDryRun(result);
}

// failDryRun — record a dry-run error WITHOUT polluting the build-error
// channel. The header chip stays at building/dirty (the operator's edits
// aren't broken — the dry-run sample / inline draft is) and the dock
// renders the message inline. Clearing the dock or running another
// dry-run wipes it.
function failDryRun(message: string): void {
  inner.update((s) => ({
    ...s,
    state: s.dirtyCount > 0 ? 'dirty' : 'building',
    dryRun: null,
    dockError: message
  }));
}

function clearDockError(): void {
  inner.update((s) => ({ ...s, dockError: null }));
}

// reset wipes the store. Used by route teardown so a navigation away
// from /studio doesn't bleed state into the next pipeline load.
function reset(): void {
  inner.set(emptyData(''));
}

// The single public facade. Components import `studio` and treat the
// methods as the contract; the internal store is never exported.
export const studio = {
  subscribe: inner.subscribe,
  // for tests + emergency reads — never use inside reactive paths
  snapshot(): StudioStateData {
    return get(inner);
  },
  hydrate,
  markDirty,
  resetDraft,
  setState,
  setError,
  clearError,
  selectNode,
  setComparison,
  clearComparison,
  setDryRun,
  clearDryRun,
  beginDryRun,
  finishDryRun,
  failDryRun,
  clearDockError,
  // Stage mutations (Task 9) — the canvas + inspector edit the draft
  // through these helpers; the deploy flow (Task 12) round-trips the
  // result to the server.
  addStage,
  removeStage,
  reorderStages,
  patchStage,
  reset
};
