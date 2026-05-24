<!--
  SchemaSelector — type-filterable schema dropdown.

  Wraps the shared <Select> with an optional `filter` prop that narrows
  the list to one schema_type ('json_schema' | 'xsd' | 'protobuf'). If
  no `schemas` prop is supplied the component fetches them on mount.

  Props
    schemas — optional pre-fetched list. When omitted we lazy-fetch
              GET /api/v1/schemas on mount; failure leaves the list
              empty so the selector is still renderable.
    filter  — optional schema_type filter. When set, only schemas of
              that type appear (and the badge is hidden — type is
              implicit). When unset, every schema appears with a small
              type badge next to its name.
    value   — selected schema id. Two-way via bind:value.
    label   — Select label. Defaults to the i18n "Schema" string.

  Events
    pick    — CustomEvent<Schema | null>; fires when the selection
              changes. Detail is the chosen schema, or null when the
              caller picks the "— None —" entry.
-->
<script lang="ts">
  import { createEventDispatcher, onMount } from 'svelte';
  import { api, type Schema } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Select from '$lib/components/Select.svelte';

  export let schemas: Schema[] | null = null;
  export let filter: 'json_schema' | 'xsd' | 'protobuf' | '' = '';
  export let value = '';
  export let label = '';

  const dispatch = createEventDispatcher<{ pick: Schema | null }>();

  let internal: Schema[] = schemas ?? [];

  // Reactive: keep `internal` in sync with the (possibly mutated) prop.
  $: if (schemas !== null) internal = schemas;

  onMount(async () => {
    // Only fetch when the caller didn't pre-supply a list.
    if (schemas === null) {
      try {
        internal = (await api.get<Schema[]>('/v1/schemas')) ?? [];
      } catch {
        internal = [];
      }
    }
  });

  // Filtered + alphabetised view. We sort by name so two adjacent
  // editors aren't subject to backend insert-order quirks.
  $: filtered = (filter
    ? internal.filter((s) => s.schema_type === filter)
    : internal
  )
    .slice()
    .sort((a, b) => a.name.localeCompare(b.name));

  $: options = [
    { value: '', label: t($locale, 'studio.schemaSelector.empty') },
    ...filtered.map((s) => ({
      value: s.id || '',
      // When a filter is active the type is implicit; suppress the
      // bracketed type so the dropdown stays scannable.
      label: filter ? s.name : `${s.name} (${s.schema_type})`
    }))
  ];

  function onChange() {
    const picked = filtered.find((s) => s.id === value) ?? null;
    dispatch('pick', picked);
  }
</script>

<Select
  bind:value
  options={options}
  label={label || t($locale, 'studio.schemaSelector.label')}
  on:change={onChange}
/>
