// Theme store — controls the data-theme attribute on <html>. Persists user
// choice in localStorage; respects prefers-color-scheme on first load.
import { writable } from 'svelte/store';
import { browser } from '$app/environment';

export type Theme = 'dark' | 'light';

function initial(): Theme {
  if (!browser) return 'dark';
  const stored = localStorage.getItem('mqc-theme') as Theme | null;
  if (stored === 'dark' || stored === 'light') return stored;
  return matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}

/**
 * Read the resolved `--bg` token from the live stylesheet. Cleaner than
 * inlining the hex here — keeps brand-tokens.css the single source of
 * truth even for the `<meta name="theme-color">` value (which controls
 * the mobile-browser chrome bar and so must match the page bg exactly).
 */
function bgColor(): string {
  if (!browser) return '';
  return getComputedStyle(document.documentElement)
    .getPropertyValue('--bg')
    .trim();
}

function syncThemeMeta() {
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute('content', bgColor());
}

function createTheme() {
  const { subscribe, set, update } = writable<Theme>(initial());
  return {
    subscribe,
    set(value: Theme) {
      if (browser) {
        document.documentElement.setAttribute('data-theme', value);
        localStorage.setItem('mqc-theme', value);
        // setAttribute is sync but custom-property resolution isn't always
        // — defer a tick so `getComputedStyle` reads the new theme's --bg.
        queueMicrotask(syncThemeMeta);
      }
      set(value);
    },
    toggle() {
      update((v) => {
        const next: Theme = v === 'dark' ? 'light' : 'dark';
        if (browser) {
          document.documentElement.setAttribute('data-theme', next);
          localStorage.setItem('mqc-theme', next);
          queueMicrotask(syncThemeMeta);
        }
        return next;
      });
    }
  };
}

export const theme = createTheme();
