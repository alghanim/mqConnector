// Static SvelteKit build — no SSR. Embedding into a Go binary requires
// pre-rendered output.
export const prerender = true;
export const ssr = false;
export const trailingSlash = 'never';
