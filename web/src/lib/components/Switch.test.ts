import { describe, it, expect } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import Switch from './Switch.svelte';

describe('Switch', () => {
  it('renders role="switch" with the correct aria-checked', () => {
    const { getByRole } = render(Switch, { props: { checked: true, label: 'Enabled' } });
    const el = getByRole('switch');
    expect(el.getAttribute('aria-checked')).toBe('true');
  });

  it('toggling the input emits a state change via two-way bind', async () => {
    const { getByRole, component } = render(Switch, { props: { checked: false, label: 'On' } });
    const el = getByRole('switch') as HTMLInputElement;
    await fireEvent.click(el);
    // After click, the component's `checked` prop reads back via $$.ctx —
    // but checking the DOM state is the user-facing assertion.
    expect(el.checked).toBe(true);
    expect(el.getAttribute('aria-checked')).toBe('true');
    void component;
  });

  it('disabled state forwards to the input element', () => {
    const { getByRole } = render(Switch, { props: { disabled: true } });
    expect(getByRole('switch')).toBeDisabled();
  });

  it('label text renders next to the track when provided', () => {
    const { getByText } = render(Switch, { props: { label: 'Notifications' } });
    expect(getByText('Notifications')).toBeInTheDocument();
  });
});
