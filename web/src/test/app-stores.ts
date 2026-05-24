// Stub for `$app/stores` so components that import `page` work under
// jsdom. SvelteKit's real $app/stores is only available at runtime
// (inside `svelte-kit dev` / `build`), so vitest can't resolve the
// module — we replace it with a minimal readable store that mirrors
// the shape components actually read (url + params).
//
// Tests that need to assert on $page.params or $page.url can override
// the store's value via `set()` from the test before rendering.
import { readable, writable } from 'svelte/store';

type PageShape = {
  url: URL;
  params: Record<string, string>;
  route: { id: string | null };
  status: number;
  error: Error | null;
  data: Record<string, unknown>;
  form: unknown;
};

// A writable backing store so tests can mutate it; the exported
// `page` is the same store typed as readable to match Kit's surface.
export const _pageStore = writable<PageShape>({
  url: new URL('http://localhost/'),
  params: {},
  route: { id: null },
  status: 200,
  error: null,
  data: {},
  form: null
});

export const page = {
  subscribe: _pageStore.subscribe
};

// Other exports from $app/stores — components rarely use them under
// jsdom, but providing a no-op readable keeps any import that does
// happen to reach for them from blowing up.
export const navigating = readable<null>(null);
export const updated = readable<boolean>(false);
