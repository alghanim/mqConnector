import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import Badge from './Badge.svelte';

describe('Badge', () => {
  it('defaults to neutral variant', () => {
    const { container } = render(Badge, {});
    const span = container.querySelector('.badge');
    expect(span).toBeInTheDocument();
    expect(span?.className).toContain('badge-neutral');
  });

  it('applies each variant', () => {
    for (const variant of ['success', 'warning', 'danger', 'neutral'] as const) {
      const { container } = render(Badge, { props: { variant } });
      const span = container.querySelector('.badge');
      expect(span?.className).toContain(`badge-${variant}`);
    }
  });
});
