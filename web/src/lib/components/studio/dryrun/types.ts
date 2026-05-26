// Shared types for the DryRunDock + child components. Lives in a
// standalone .ts so .svelte files can `import type` from here without
// triggering the "cross-file Svelte exports need <script context='module'>"
// constraint.

// StageRun mirrors pipeline.StageRun on the server (serialised by
// internal/server/handlers_preview.go stageRunJSON). Kept here as the
// single source of truth for the dry-run-dock + canvas overlay path.
export interface StageRun {
  name: string;
  duration_ns: number;
  failed: boolean;
  body?: string;
  format?: string;
  err?: string;
}

// CanvasOverlay — the shape <StudioCanvas> consumes via its
// `dryRunOverlays` prop and falls back to reading from `studio.dryRun`
// when none is supplied.
export interface CanvasOverlay {
  stageId: string;
  failed: boolean;
  durationMs: number;
}

// PreviewResponse — narrowed shape of POST /api/v1/preview as the dock
// renders it. The backend may add fields; we treat anything we don't
// recognise as absent (defensive narrowing in the dock).
export interface PreviewResponse {
  ok?: boolean;
  output?: string;
  format?: string;
  routes?: string[];
  error?: string;
  stage_runs?: StageRun[];
}
