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

  // The whole UI is held up by parents writing `<Button on:click={…}>`.
  // Forwarding is what makes that work in Svelte 4 — without `on:click`
  // on the inner <button>, every Save/Delete/Edit/Configure click dies
  // inside the component. Phase 16 broke this for two releases; pinning
  // it so it never happens again.
  it('forwards DOM click events to parent listeners', async () => {
    let clicked = false;
    const { getByRole } = render(Button, {
      events: { click: () => (clicked = true) }
    });
    await fireEvent.click(getByRole('button'));
    expect(clicked).toBe(true);
  });

  // JSDOM's fireEvent.click ignores the disabled attribute (real browsers
  // don't fire click on a disabled button). Test the contract we control:
  // the disabled attribute must be set, the browser will honour it.
  it('carries disabled=true when disabled', () => {
    const { getByRole } = render(Button, { props: { disabled: true } });
    expect(getByRole('button')).toBeDisabled();
  });

  it('honours the type prop', () => {
    const { getByRole } = render(Button, { props: { type: 'submit' } });
    expect(getByRole('button').getAttribute('type')).toBe('submit');
  });
});
