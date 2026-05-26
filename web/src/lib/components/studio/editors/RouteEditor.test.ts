// RouteEditor tests — three cases:
//
//   1. Renders the wrapped RoutingRuleListEditor with the help string.
//   2. The "Test pattern" button opens the tester dialog.
//   3. Advanced JSON escape hatch preserves unknown keys.
import { describe, expect, it } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import RouteEditor from './RouteEditor.svelte';
import type { Connection, RoutingRule } from '$lib/api';

const conns: Connection[] = [
  { id: 'c1', name: 'src', type: 'kafka' },
  { id: 'c2', name: 'dst', type: 'rabbitmq' }
];

const rules: RoutingRule[] = [
  {
    condition_path: '$.country',
    condition_operator: 'regex',
    condition_value: '^Q',
    destination_id: 'c2',
    priority: 1,
    enabled: true
  }
];

describe('RouteEditor', () => {
  it('renders the help string and the wrapped list editor heading', () => {
    const { getByText, container } = render(RouteEditor, {
      props: { config: '{}', valid: true, rules, connections: conns }
    });
    // Help string from wrapper.
    expect(getByText(/routing rules below/i)).toBeInTheDocument();
    // Heading from wrapped RoutingRuleListEditor (in a <p
    // class="section-heading">). The text "Routing rules" appears
    // both in the heading and inside the wrapper's help string, so
    // assert via the dedicated container the list editor uses.
    expect(container.querySelector('.section-heading')?.textContent).toMatch(
      /Routing rules/i
    );
    // The list editor renders one row per supplied rule.
    expect(container.querySelectorAll('.rr-row').length).toBe(rules.length);
  });

  it('Test pattern button opens the tester dialog', async () => {
    const { getByText, queryByText } = render(RouteEditor, {
      props: { config: '{}', valid: true, rules, connections: conns }
    });
    expect(queryByText('Test regex')).toBeNull();
    await fireEvent.click(getByText('Test pattern'));
    await waitFor(() => expect(getByText('Test regex')).toBeInTheDocument());
  });

  it('Advanced JSON escape hatch preserves unknown keys', () => {
    const { container } = render(RouteEditor, {
      props: {
        config: '{"future_flag":"x"}',
        valid: true,
        rules: [],
        connections: conns
      }
    });
    const textarea = container.querySelector('details textarea') as HTMLTextAreaElement;
    expect(textarea.value).toContain('future_flag');
  });
});
