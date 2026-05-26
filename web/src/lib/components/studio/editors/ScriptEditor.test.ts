// ScriptEditor tests — four cases:
//
//   1. Renders the script textarea pre-filled from config.
//   2. Line-number gutter reflects the line count.
//   3. Empty-script error hint renders when the script body is blank.
//   4. Test-on-sample button emits a `test` event with the body.
import { describe, expect, it } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import ScriptEditor from './ScriptEditor.svelte';

describe('ScriptEditor', () => {
  it('renders the script textarea pre-filled from config', () => {
    const { getByDisplayValue } = render(ScriptEditor, {
      props: { config: '{"script":"msg.x = 1"}', valid: true }
    });
    expect(getByDisplayValue('msg.x = 1')).toBeInTheDocument();
  });

  it('line-number gutter reflects the line count', () => {
    const cfg = JSON.stringify({ script: 'a\nb\nc\nd' });
    const { container } = render(ScriptEditor, {
      props: { config: cfg, valid: true }
    });
    const gutter = container.querySelector('.se-gutter') as HTMLElement;
    expect(gutter.textContent).toBe('1\n2\n3\n4');
  });

  it('renders the empty-script error hint when the body is blank', () => {
    const { getByText } = render(ScriptEditor, {
      props: { config: '{"script":""}', valid: true }
    });
    expect(getByText(/Script body cannot be empty/i)).toBeInTheDocument();
  });

  it('Test button emits a test event with the script body', async () => {
    const events: { script: string; timeout_ms: number }[] = [];
    const { getByText } = render(ScriptEditor, {
      props: { config: '{"script":"msg;"}', valid: false },
      events: {
        test: (e: CustomEvent<{ script: string; timeout_ms: number }>) =>
          events.push(e.detail)
      }
    });
    await fireEvent.click(getByText('Test on sample'));
    await waitFor(() => expect(events.length).toBe(1));
    expect(events[0].script).toBe('msg;');
  });
});
