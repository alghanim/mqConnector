// Shared test setup. Runs BEFORE any test file's own imports, so polyfills
// installed here are visible when stores like theme/locale evaluate their
// module-top `initial()` (which reads from localStorage and matchMedia).
import '@testing-library/jest-dom/vitest';
// Registers afterEach(act()+cleanup()) so Svelte's onMount lifecycle
// actually fires inside @testing-library/svelte's jsdom mount. Without
// this the runtime queues onMount but never flushes — page tests that
// depend on data-fetching-on-mount would hang indefinitely.
import '@testing-library/svelte/vitest';
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

// --- File / Blob.text polyfill ----------------------------------------------
// jsdom 24's File/Blob ship without the async `.text()` method that's part
// of the WHATWG File API. The settings page's drag-drop import staging
// reads stagedFile.text() to capture the bundle before POSTing — without
// this polyfill those tests blow up with "f.text is not a function".
if (typeof Blob !== 'undefined' && typeof (Blob.prototype as { text?: unknown }).text !== 'function') {
  Object.defineProperty(Blob.prototype, 'text', {
    configurable: true,
    value(this: Blob): Promise<string> {
      return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(reader.result as string);
        reader.onerror = () => reject(reader.error);
        reader.readAsText(this);
      });
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
