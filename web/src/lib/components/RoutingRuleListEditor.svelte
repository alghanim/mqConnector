<!--
  RoutingRuleListEditor — the pipeline-level routing-rules list
  (condition_path, operator, value, destination, priority).

  Shared between the form editor (/pipelines/[id]) and the visual editor
  (/flow). Owns no fetch state — edits its `rules` prop in place via
  `bind:rules` and lets the parent persist via
  PUT /v1/pipelines/:id/routing-rules.

  In the flow editor a `route` stage in the chain is what makes the
  rules fire at runtime — the rule list is rendered in the route node's
  props panel, and on Save & Deploy the parent persists the full list.
  Destination nodes on the canvas are no longer the source of truth for
  predicates (that data lives here).
-->
<script lang="ts">
  import type { Connection, RoutingOperator, RoutingRule } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';

  export let rules: RoutingRule[] = [];
  export let connections: Connection[] = [];
  /** Single-column layout for the narrow flow props panel. */
  export let compact = false;

  $: connOptions = connections.map((c) => ({
    value: c.id || '',
    label: `${c.name} (${c.type})`
  }));
  $: routingOpOptions = (
    ['eq', 'neq', 'contains', 'regex', 'gt', 'lt', 'exists'] as RoutingOperator[]
  ).map((v) => ({ value: v, label: v }));

  export function add() {
    rules = [
      ...rules,
      {
        condition_path: '',
        condition_operator: 'eq',
        condition_value: '',
        destination_id: connections[0]?.id || '',
        priority: rules.length + 1,
        enabled: true
      }
    ];
  }

  function remove(i: number) {
    rules = rules.filter((_, idx) => idx !== i);
  }
</script>

<div class="space-y-3">
  <div class="flex items-center justify-between">
    <p class="section-heading">{t($locale, 'pipelines.routing')}</p>
    <Button variant="ghost" on:click={add}>{t($locale, 'pipelines.routing.add')}</Button>
  </div>

  {#if rules.length === 0}
    <p style="color: var(--text-muted)">{t($locale, 'pipelines.routing.empty')}</p>
  {:else}
    <div class="space-y-3">
      {#each rules as r, i (i)}
        <div class="rr-row">
          <div class="grid gap-3" class:rr-grid-2={!compact} class:rr-grid-1={compact}>
            <Input
              bind:value={r.condition_path}
              label={t($locale, 'pipelines.routing.path')}
            />
            <Select
              bind:value={r.condition_operator}
              options={routingOpOptions}
              label={t($locale, 'pipelines.routing.operator')}
            />
            {#if r.condition_operator !== 'exists'}
              <Input
                bind:value={r.condition_value}
                label={t($locale, 'pipelines.routing.value')}
              />
            {/if}
            <Select
              bind:value={r.destination_id}
              options={connOptions}
              label={t($locale, 'pipelines.routing.destination')}
            />
            <Input
              bind:value={r.priority}
              type="number"
              label={t($locale, 'pipelines.routing.priority')}
            />
            <label class="rr-enable">
              <input type="checkbox" bind:checked={r.enabled} />
              {t($locale, 'common.enabled')}
            </label>
          </div>
          <div class="flex justify-end mt-2">
            <Button variant="outline" on:click={() => remove(i)}>
              {t($locale, 'common.delete')}
            </Button>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .rr-row {
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 12px 14px;
    background: var(--surface);
  }
  .rr-grid-2 { grid-template-columns: 1fr; }
  @media (min-width: 640px) {
    .rr-grid-2 { grid-template-columns: 1fr 1fr; }
  }
  .rr-grid-1 { grid-template-columns: 1fr; }
  .rr-enable {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--text);
    font-size: 13px;
  }
</style>
