import { describe, it, expect } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import Alert from './Alert.svelte';

describe('Alert', () => {
  it('renders default info variant with status role', () => {
    const { getByRole } = render(Alert, { props: {}, context: new Map() });
    const el = getByRole('status');
    expect(el).toBeInTheDocument();
    expect(el.className).toMatch(/alert-info/);
    expect(el.getAttribute('aria-live')).toBe('polite');
  });

  it('error variant gets role=alert and aria-live=assertive', () => {
    const { getByRole } = render(Alert, { props: { variant: 'error' } });
    const el = getByRole('alert');
    expect(el.className).toMatch(/alert-error/);
    expect(el.getAttribute('aria-live')).toBe('assertive');
  });

  it('shows close button only when dismissible and emits dismiss', async () => {
    let dismissed = false;
    const { queryByLabelText, component } = render(Alert, {
      props: { dismissible: true }
    });
    component.$on('dismiss', () => (dismissed = true));
    const close = queryByLabelText('Dismiss')!;
    expect(close).toBeInTheDocument();
    await fireEvent.click(close);
    expect(dismissed).toBe(true);
  });

  it('omits close button when not dismissible', () => {
    const { queryByLabelText } = render(Alert);
    expect(queryByLabelText('Dismiss')).toBeNull();
  });
});
