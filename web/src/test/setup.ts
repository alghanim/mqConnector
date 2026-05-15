// Shared test setup. Runs BEFORE any test file's own imports, so polyfills
// installed here are visible when stores like theme/locale evaluate their
// module-top `initial()` (which reads from localStorage and matchMedia).
import '@testing-library/jest-dom/vitest';
import { afterEach } from 'vitest';

// --- localStorage polyfill ---------------------------------------------------
// Node 24's experimental localStorage is partial; jsdom's coverage varies by
// vitest version. Install a trivial in-memory replacement when the runtime's
// version lacks `clear`.
const lsBroken =
  typeof localStorage === 'undefined' ||
  typeof (globalThis as { localStorage?: { clear?: unknown } }).localStorage?.clear !==
    'function';
if (lsBroken) {
  const store = new Map<string, string>();
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      get length() {
        return store.size;
      },
      clear: () => store.clear(),
      getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
      setItem: (k: string, v: string) => store.set(k, String(v)),
      removeItem: (k: string) => store.delete(k),
      key: (i: number) => Array.from(store.keys())[i] ?? null
    }
  });
}

// --- matchMedia polyfill -----------------------------------------------------
// jsdom doesn't implement it; the theme store's `initial()` calls it on a
// cold load to pick a default.
if (typeof window !== 'undefined' && !window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false
    })
  });
}

// --- Per-test reset ----------------------------------------------------------
afterEach(() => {
  try {
    localStorage.clear();
  } catch {
    /* no-op */
  }
  document.documentElement.removeAttribute('data-theme');
  document.documentElement.removeAttribute('dir');
  document.documentElement.removeAttribute('lang');
});
