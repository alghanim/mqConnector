// ValidateEditor tests — three cases:
//
//   1. Renders the SchemaSelector with the supplied schemas.
//   2. Picking a protobuf schema reveals the proto_message input.
//   3. No-schema-selected error hint renders on mount with empty config.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import ValidateEditor from './ValidateEditor.svelte';
import type { Schema } from '$lib/api';

const schemas: Schema[] = [
  { id: 's1', name: 'orders.json', schema_type: 'json_schema', content: '{}' },
  { id: 's3', name: 'orders.proto', schema_type: 'protobuf', content: 'syntax="proto3";' }
];

beforeEach(() => {
  globalThis.fetch = vi.fn(async () =>
    new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } })
  ) as unknown as typeof fetch;
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('ValidateEditor', () => {
  it('renders the SchemaSelector with the supplied schemas', () => {
    const { getByText } = render(ValidateEditor, {
      props: { config: '{}', valid: false, schemas }
    });
    // Both schemas show with the type-suffix label (unfiltered).
    expect(getByText('orders.json (json_schema)')).toBeInTheDocument();
    expect(getByText('orders.proto (protobuf)')).toBeInTheDocument();
  });

  it('picking a protobuf schema reveals the proto_message input', async () => {
    const { container, queryByText } = render(ValidateEditor, {
      props: { config: '{}', valid: false, schemas }
    });
    expect(queryByText(/Proto message/i)).toBeNull();
    const select = container.querySelector('select') as HTMLSelectElement;
    select.value = 's3';
    await fireEvent.change(select);
    await waitFor(() => expect(queryByText(/Proto message/i)).not.toBeNull());
  });

  it('renders the no-schema error hint on mount with empty config', () => {
    const { getByText } = render(ValidateEditor, {
      props: { config: '{}', valid: true, schemas }
    });
    expect(getByText(/A schema must be selected/i)).toBeInTheDocument();
  });
});
