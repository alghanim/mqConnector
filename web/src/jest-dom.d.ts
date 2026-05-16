// Wire jest-dom's matcher types into vitest's Assertion shape.
//
// vitest 3 declares Assertion<T> inside `declare module "@vitest/expect"`.
// @testing-library/jest-dom@6 ships an augmentation but it targets the
// legacy `vitest` module path, so matchers register at runtime via
// src/test/setup.ts but svelte-check treats .toBeInTheDocument() etc.
// as `never`. Augment @vitest/expect ourselves so types track runtime.
import type { TestingLibraryMatchers } from '@testing-library/jest-dom/matchers';

declare module '@vitest/expect' {
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  interface Assertion<T = unknown> extends TestingLibraryMatchers<unknown, void> {}
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  interface AsymmetricMatchersContaining extends TestingLibraryMatchers<unknown, void> {}
}

export {};
