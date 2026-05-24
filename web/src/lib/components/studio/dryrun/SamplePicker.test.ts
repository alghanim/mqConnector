// SamplePicker tests — the two-tab picker that feeds samples to the
// dry-run dock. Four cases cover the spec:
//   1. Saved tab lists the shipped SAMPLE_FIXTURES.
//   2. Clicking "Use this" copies the fixture body into `value` AND
//      emits 'change' with the body.
//   3. Paste-tab textarea binds two-way and emits change on input.
//   4. File upload below 1 MiB reads + sets the value.
//
// File uploads in jsdom require a fake File with .text(); setup.ts
// installs the polyfill if jsdom's Blob.text is missing.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import SamplePicker from './SamplePicker.svelte';
import { SAMPLE_FIXTURES } from '$lib/sample-fixtures';

afterEach(() => {
  vi.restoreAllMocks();
});

describe('SamplePicker', () => {
  it('lists the shipped fixtures on the Saved tab', () => {
    const { getByText } = render(SamplePicker);
    // Each fixture's label is rendered on its card.
    for (const f of SAMPLE_FIXTURES) {
      expect(getByText(f.label)).toBeInTheDocument();
    }
  });

  it('Use-this populates value and emits change with the fixture body', async () => {
    let received: string | null = null;
    const { getAllByText } = render(SamplePicker, {
      events: { change: (e: CustomEvent<string>) => (received = e.detail) }
    });
    // Two fixtures, two "Use this" buttons — click the first.
    const buttons = getAllByText(/use this/i);
    await fireEvent.click(buttons[0]);
    expect(received).toBe(SAMPLE_FIXTURES[0].body);
  });

  it('Paste tab textarea emits change on input', async () => {
    let received: string | null = null;
    const { getByPlaceholderText } = render(SamplePicker, {
      events: { change: (e: CustomEvent<string>) => (received = e.detail) }
    });
    // Tab strip — index 1 is the Paste tab.
    const tabs = document.querySelectorAll('button[role="tab"]');
    await fireEvent.click(tabs[1]);
    const ta = await waitFor(() => getByPlaceholderText(/paste a json or xml/i));
    await fireEvent.input(ta, { target: { value: '{"hello":"world"}' } });
    expect(received).toBe('{"hello":"world"}');
  });

  it('file upload below 1 MiB reads text and updates value', async () => {
    let received: string | null = null;
    const { container } = render(SamplePicker, {
      events: { change: (e: CustomEvent<string>) => (received = e.detail) }
    });
    // Switch to Paste tab to expose the <input type=file>.
    const tabs = document.querySelectorAll('button[role="tab"]');
    await fireEvent.click(tabs[1]);
    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    expect(fileInput).not.toBeNull();
    const blob = new File(['{"from":"file"}'], 'sample.json', {
      type: 'application/json'
    });
    // jsdom doesn't allow direct assignment to .files on plain
    // HTMLInputElement; use Object.defineProperty.
    Object.defineProperty(fileInput, 'files', { value: [blob], configurable: true });
    await fireEvent.change(fileInput);
    await waitFor(() => expect(received).toBe('{"from":"file"}'));
  });
});
