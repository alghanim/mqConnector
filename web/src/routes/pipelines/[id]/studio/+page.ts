// /pipelines/[id]/studio — the Pipeline Studio shell. Same SPA-only
// posture as the rest of the /pipelines/* tree: ID is only known at
// runtime, so we don't pre-render or SSR. SvelteKit's static adapter
// would otherwise try to bake a placeholder page on build.
export const prerender = false;
export const ssr = false;
