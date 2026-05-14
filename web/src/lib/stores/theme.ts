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

function createTheme() {
  const { subscribe, set, update } = writable<Theme>(initial());
  return {
    subscribe,
    set(value: Theme) {
      if (browser) {
        document.documentElement.setAttribute('data-theme', value);
        document.querySelector('meta[name="theme-color"]')?.setAttribute(
          'content',
          value === 'dark' ? '#222A31' : '#F7F5F3'
        );
        localStorage.setItem('mqc-theme', value);
      }
      set(value);
    },
    toggle() {
      update((v) => {
        const next: Theme = v === 'dark' ? 'light' : 'dark';
        if (browser) {
          document.documentElement.setAttribute('data-theme', next);
          localStorage.setItem('mqc-theme', next);
        }
        return next;
      });
    }
  };
}

export const theme = createTheme();
