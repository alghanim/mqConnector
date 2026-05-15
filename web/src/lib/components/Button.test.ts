import { describe, it, expect } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import Button from './Button.svelte';

describe('Button', () => {
  it('renders as a button element with the brand class', () => {
    const { getByRole } = render(Button, { props: { variant: 'primary' } });
    const btn = getByRole('button');
    expect(btn).toBeInTheDocument();
    expect(btn.className).toContain('btn');
  });

  it('applies the chosen variant class', () => {
    const { getByRole } = render(Button, { props: { variant: 'danger' } });
    expect(getByRole('button').className).toContain('btn-danger');
  });

  it('is disabled and aria-busy when loading', () => {
    const { getByRole } = render(Button, { props: { loading: true } });
    const btn = getByRole('button');
    expect(btn).toBeDisabled();
    expect(btn.getAttribute('aria-busy')).toBe('true');
  });

  it('invokes the onClick prop when clicked', async () => {
    let clicked = false;
    const { getByRole } = render(Button, {
      props: {
        onClick: () => {
          clicked = true;
        }
      }
    });
    await fireEvent.click(getByRole('button'));
    expect(clicked).toBe(true);
  });

  it('honours the type prop', () => {
    const { getByRole } = render(Button, { props: { type: 'submit' } });
    expect(getByRole('button').getAttribute('type')).toBe('submit');
  });
});
