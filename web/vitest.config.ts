// Vitest setup for the SvelteKit admin UI. Uses jsdom so component tests
// can mount real DOM, and the svelte plugin so .svelte files are
// transformed identically to the production build.
import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import path from 'node:path';

export default defineConfig({
  plugins: [svelte({ hot: false })],
  resolve: {
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
    include: ['src/**/*.{test,spec}.{js,ts}']
  }
});
