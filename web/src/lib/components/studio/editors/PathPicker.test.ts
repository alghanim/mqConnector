// PathPicker tests — three cases covering the contract:
//
//   1. Renders the path list when opened.
//   2. The filter input narrows the visible list.
//   3. Clicking a path emits the `pick` event with the path as detail
//      and closes the popover.
//
// The Dialog used as the popover host renders into the document body;
// queries via `getByText` / `queryByText` work transparently because
// jsdom exposes the body as the default container.
import { describe, expect, it } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import PathPicker from './PathPicker.svelte';

describe('PathPicker', () => {
  it('renders the path list when opened', async () => {
    const { getByText, queryByText } = render(PathPicker, {
      props: { paths: ['$.a.b', '$.c.d', '$.e'], value: '' }
    });
    // Closed by default — no list items in the DOM yet.
    expect(queryByText('$.a.b')).toBeNull();
    // Open via the trigger button.
    await fireEvent.click(getByText(/From sample/i));
    await waitFor(() => expect(getByText('$.a.b')).toBeInTheDocument());
    expect(getByText('$.c.d')).toBeInTheDocument();
    expect(getByText('$.e')).toBeInTheDocument();
  });

  it('filter input narrows the visible list', async () => {
    const { getByText, getByPlaceholderText, queryByText } = render(PathPicker, {
      props: { paths: ['$.alpha', '$.beta', '$.gamma'], value: '' }
    });
    await fireEvent.click(getByText(/From sample/i));
    const filter = await waitFor(() => getByPlaceholderText(/Filter paths/i));
    await fireEvent.input(filter, { target: { value: 'alpha' } });
    expect(getByText('$.alpha')).toBeInTheDocument();
    expect(queryByText('$.beta')).toBeNull();
    expect(queryByText('$.gamma')).toBeNull();
  });

  it('clicking a path emits pick and closes the popover', async () => {
    const picks: string[] = [];
    const { getByText, queryByText } = render(PathPicker, {
      props: { paths: ['$.x', '$.y'], value: '' },
      events: { pick: (e: CustomEvent<string>) => picks.push(e.detail) }
    });
    await fireEvent.click(getByText(/From sample/i));
    await waitFor(() => expect(getByText('$.x')).toBeInTheDocument());
    await fireEvent.click(getByText('$.x'));
    expect(picks).toEqual(['$.x']);
    await waitFor(() => expect(queryByText('$.x')).toBeNull());
  });
});
