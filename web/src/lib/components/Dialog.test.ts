import { describe, it, expect } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import Dialog from './Dialog.svelte';

describe('Dialog', () => {
  it('does not render when closed', () => {
    const { queryByRole } = render(Dialog, { props: { open: false } });
    expect(queryByRole('dialog')).toBeNull();
  });

  it('renders with role=dialog + aria-modal when open', () => {
    const { getByRole } = render(Dialog, { props: { open: true, title: 'Test' } });
    const el = getByRole('dialog');
    expect(el.getAttribute('aria-modal')).toBe('true');
  });

  it('clicking Confirm emits confirm', async () => {
    let fired = false;
    const { getByText, component } = render(Dialog, {
      props: { open: true, confirmLabel: 'Yes', cancelLabel: 'No' }
    });
    component.$on('confirm', () => (fired = true));
    await fireEvent.click(getByText('Yes'));
    expect(fired).toBe(true);
  });

  it('clicking Cancel emits cancel', async () => {
    let fired = false;
    const { getByText, component } = render(Dialog, {
      props: { open: true, confirmLabel: 'Yes', cancelLabel: 'No' }
    });
    component.$on('cancel', () => (fired = true));
    await fireEvent.click(getByText('No'));
    expect(fired).toBe(true);
  });

  it('busy=true disables both action buttons', () => {
    const { getByText } = render(Dialog, {
      props: { open: true, busy: true, confirmLabel: 'Yes', cancelLabel: 'No' }
    });
    expect(getByText('Yes')).toBeDisabled();
    expect(getByText('No')).toBeDisabled();
  });
});
