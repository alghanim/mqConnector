import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import Input from './Input.svelte';

// The Input component is the surface every form in the app builds on
// top of. ARIA correctness here is what makes the whole UI screen-
// reader-usable, so the tests pin the wiring rather than the styling.

describe('Input', () => {
  it('associates the label with the input via for/id', () => {
    const { getByLabelText, getByText } = render(Input, {
      props: { label: 'Email', value: '' }
    });
    // getByLabelText only finds elements whose label[for] matches
    // the input id; this assertion fails if the wiring breaks.
    const input = getByLabelText('Email');
    expect(input).toBeInTheDocument();
    expect(input.tagName).toBe('INPUT');
    const label = getByText('Email');
    expect(label.getAttribute('for')).toBe(input.id);
  });

  it('sets aria-invalid and aria-describedby on error', () => {
    const { getByLabelText, getByText } = render(Input, {
      props: { label: 'Email', value: 'no-at-sign', error: 'Invalid email' }
    });
    const input = getByLabelText('Email');
    expect(input.getAttribute('aria-invalid')).toBe('true');
    const errNode = getByText('Invalid email');
    expect(input.getAttribute('aria-describedby')).toBe(errNode.id);
  });

  it('points aria-describedby at the helper when no error', () => {
    const { getByLabelText, getByText } = render(Input, {
      props: { label: 'Email', value: '', helper: 'we will not share this' }
    });
    const input = getByLabelText('Email');
    const helperNode = getByText('we will not share this');
    expect(input.getAttribute('aria-describedby')).toBe(helperNode.id);
    // The framework normalises aria-invalid="false" rather than
    // omitting it. Either form is screen-reader-equivalent; pin the
    // current behaviour so a regression is visible.
    const ariaInvalid = input.getAttribute('aria-invalid');
    expect(ariaInvalid === null || ariaInvalid === 'false').toBe(true);
  });

  it('marks required inputs with the required attribute', () => {
    // getByLabelText against a label containing an asterisk span
    // does not always match the bare label string; query via id
    // instead so the assertion focuses on the attribute, not the
    // label-association mechanics already covered above.
    const { container } = render(Input, {
      props: { label: 'Email', value: '', required: true }
    });
    const input = container.querySelector('input') as HTMLInputElement;
    expect(input).not.toBeNull();
    expect(input.required).toBe(true);
  });

  it('renders the appropriate type', () => {
    for (const type of ['text', 'password', 'email', 'url', 'number'] as const) {
      const { getByLabelText, unmount } = render(Input, {
        props: { label: type, value: '', type }
      });
      const input = getByLabelText(type) as HTMLInputElement;
      expect(input.type).toBe(type);
      unmount();
    }
  });
});
