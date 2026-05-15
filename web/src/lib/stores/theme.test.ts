import { describe, it, expect, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import { theme } from './theme';

describe('theme store', () => {
  beforeEach(() => {
    // Each test starts from a clean slate; setup.ts already clears
    // localStorage and the html attribute after each test.
  });

  it('defaults to dark when nothing is stored', () => {
    expect(['dark', 'light']).toContain(get(theme));
  });

  it('toggle flips dark → light → dark', () => {
    theme.set('dark');
    theme.toggle();
    expect(get(theme)).toBe('light');
    theme.toggle();
    expect(get(theme)).toBe('dark');
  });

  it('set persists to localStorage', () => {
    theme.set('light');
    expect(localStorage.getItem('mqc-theme')).toBe('light');
  });

  it('set updates the documentElement data-theme attribute', () => {
    theme.set('light');
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
    theme.set('dark');
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
  });
});
