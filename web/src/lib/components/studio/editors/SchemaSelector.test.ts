// SchemaSelector tests — three cases covering the contract:
//
//   1. Pre-supplied schemas: renders every option (no fetch).
//   2. Filter prop narrows the list to one schema_type.
//   3. Selecting an option fires the `pick` event with the schema.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import SchemaSelector from './SchemaSelector.svelte';
import type { Schema } from '$lib/api';

const sampleSchemas: Schema[] = [
  { id: 's1', name: 'orders.json', schema_type: 'json_schema', content: '{}' },
  { id: 's2', name: 'orders.xsd', schema_type: 'xsd', content: '<x/>' },
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

describe('SchemaSelector', () => {
  it('renders all options when no filter is provided', () => {
    const { getByText } = render(SchemaSelector, {
      props: { schemas: sampleSchemas, value: '', filter: '' }
    });
    expect(getByText('orders.json (json_schema)')).toBeInTheDocument();
    expect(getByText('orders.xsd (xsd)')).toBeInTheDocument();
    expect(getByText('orders.proto (protobuf)')).toBeInTheDocument();
  });

  it('filter narrows the list to a single schema_type', () => {
    const { getByText, queryByText } = render(SchemaSelector, {
      props: { schemas: sampleSchemas, value: '', filter: 'protobuf' }
    });
    expect(getByText('orders.proto')).toBeInTheDocument();
    expect(queryByText('orders.json (json_schema)')).toBeNull();
    expect(queryByText('orders.xsd (xsd)')).toBeNull();
  });

  it('selecting an option fires pick with the schema as detail', async () => {
    const picks: (Schema | null)[] = [];
    const { container } = render(SchemaSelector, {
      props: { schemas: sampleSchemas, value: '', filter: '' },
      events: { pick: (e: CustomEvent<Schema | null>) => picks.push(e.detail) }
    });
    const select = container.querySelector('select') as HTMLSelectElement;
    select.value = 's2';
    await fireEvent.change(select);
    await waitFor(() => expect(picks.length).toBe(1));
    expect(picks[0]?.id).toBe('s2');
    expect(picks[0]?.schema_type).toBe('xsd');
  });
});
