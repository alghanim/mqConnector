// Vitest setup for the SvelteKit admin UI. Uses jsdom so component tests
// can mount real DOM, and the svelte plugin so .svelte files are
// transformed identically to the production build.
import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import path from 'node:path';

export default defineConfig({
  // Force client-side compilation: without `compilerOptions.generate:
  // 'dom'`, vite-plugin-svelte can fall through to the SSR transform
  // when running under vitest, producing component output that never
  // fires onMount. The runtime mounts fine but lifecycle hooks are
  // stripped from SSR output.
  plugins: [svelte({ hot: false, compilerOptions: { generate: 'dom' } })],
  resolve: {
    // Pick the browser export condition for every package. vite-plugin-
    // svelte's `package.json` ships separate exports for `browser` vs
    // `node` — without this, vitest in SSR-default mode resolves to
    // the SSR runtime which strips DOM lifecycle (onMount becomes a
    // no-op). The `browser` condition routes us to the client runtime
    // that actually mounts to the DOM and fires lifecycle hooks.
    conditions: ['browser'],
    alias: {
      $lib: path.resolve(__dirname, 'src/lib'),
      // The SvelteKit modules under $app are not available in unit tests.
      // Stub them to the minimal surface our stores need.
      '$app/environment': path.resolve(__dirname, 'src/test/app-environment.ts')
    }
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.{test,spec}.{js,ts}'],
    // Vitest's default for module imports is vite's SSR loader, which
    // makes vite-plugin-svelte emit SSR output — no event listeners,
    // no lifecycle hooks. Components render their HTML but onMount
    // never fires, breaking any page test that depends on a
    // data-fetching mount. transformMode.web pins .svelte files to the
    // client transform pipeline so the runtime DOM lifecycle works.
    transformMode: { web: [/\.svelte$/] }
  }
});
