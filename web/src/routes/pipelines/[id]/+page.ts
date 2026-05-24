// /pipelines/[id] is per-pipeline configuration; there's nothing to
// prerender because the ID is only known at runtime. Keep it SPA-routed.
//
// Wave 1 / Task 14 — the legacy form view is preserved behind
// `?legacy=1` for one release as a safety net. Any plain visit to
// /pipelines/{id} redirects into the Studio so the operators that
// landed here via bookmarks, the command palette's "open pipeline"
// suggestion, or muscle memory from the prior release all funnel
// through the new primary surface. The escape hatch (still rendered
// by +page.svelte) is reachable only by an explicit `?legacy=1` —
// the secondary GitFork icon on the /pipelines list appends it.
//
// 307 (Temporary) is intentional: Wave 2 deletes this route, so we
// must not let browsers cache a permanent redirect — 308/301 would
// strand operators who upgrade past Wave 2 with a stale entry in
// their address bar history pointing to a path we've removed.
import { redirect } from '@sveltejs/kit';
import type { PageLoad } from './$types';

export const prerender = false;
export const ssr = false;

export const load: PageLoad = ({ params, url }) => {
  const isLegacy = url.searchParams.get('legacy') === '1';
  if (!isLegacy) {
    throw redirect(307, `/pipelines/${params.id}/studio`);
  }
  return { pipelineId: params.id ?? '' };
};
