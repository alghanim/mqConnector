<!--
  TransformListEditor — the rename/mask/move/set/delete operation list
  that lives at the pipeline level (one list per pipeline, regardless of
  how many `transform` stages appear in the chain).

  Shared between the form editor (/pipelines/[id]) and the visual editor
  (/flow) so both round-trip against the same schema and behave the same
  way. The component owns no fetch state — it edits its `transforms`
  prop in place via `bind:transforms` and lets the parent persist via
  PUT /v1/pipelines/:id/transforms.

  Why "pipeline-level, not per-node": the runtime treats a transform
  stage as a marker that says "apply the transform list here". Two
  transform stages in the same chain would both consume the same list,
  so attaching the list to a single node is the cleanest mental model.
-->
<script lang="ts">
  import type { Transform, TransformType } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';

  export let transforms: Transform[] = [];
  /**
   * Force single-column layout (no `sm:` two-column grid). Used when
   * the editor sits inside the flow props panel (300 px) where the
   * desktop sm: breakpoint would otherwise crush the inputs to ~140 px.
   */
  export let compact = false;

  $: transformTypeOptions = (
    ['rename', 'mask', 'move', 'set', 'delete'] as TransformType[]
  ).map((v) => ({ value: v, label: v }));

  export function add() {
    transforms = [
      ...transforms,
      {
        transform_type: 'rename',
        source_path: '',
        target_path: '',
        mask_pattern: '',
        mask_replace: '',
        set_value: '',
        order: transforms.length + 1
      }
    ];
  }

  function remove(i: number) {
    transforms = transforms
      .filter((_, idx) => idx !== i)
      .map((tr, idx) => ({ ...tr, order: idx + 1 }));
  }
</script>

<div class="space-y-3">
  <div class="flex items-center justify-between">
    <p class="section-heading">{t($locale, 'pipelines.transforms')}</p>
    <Button variant="ghost" on:click={add}>{t($locale, 'pipelines.transforms.add')}</Button>
  </div>

  {#if transforms.length === 0}
    <p style="color: var(--text-muted)">{t($locale, 'pipelines.transforms.empty')}</p>
  {:else}
    <div class="space-y-3">
      {#each transforms as tr, i (i)}
        <div class="tx-row">
          <div class="grid gap-3" class:tx-grid-2={!compact} class:tx-grid-1={compact}>
            <Select
              bind:value={tr.transform_type}
              options={transformTypeOptions}
              label={t($locale, 'pipelines.transforms.type')}
            />
            <Input
              bind:value={tr.source_path}
              label={t($locale, 'pipelines.transforms.sourcePath')}
            />
            {#if tr.transform_type === 'rename' || tr.transform_type === 'move'}
              <Input
                bind:value={tr.target_path}
                label={t($locale, 'pipelines.transforms.targetPath')}
              />
            {/if}
            {#if tr.transform_type === 'mask'}
              <Input
                bind:value={tr.mask_pattern}
                label={t($locale, 'pipelines.transforms.maskPattern')}
              />
              <Input
                bind:value={tr.mask_replace}
                label={t($locale, 'pipelines.transforms.maskReplace')}
              />
            {/if}
            {#if tr.transform_type === 'set'}
              <Input
                bind:value={tr.set_value}
                label={t($locale, 'pipelines.transforms.setValue')}
              />
            {/if}
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
  .tx-row {
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 12px 14px;
    background: var(--surface);
  }
  /* Two-column form fields for the wide form-editor view */
  .tx-grid-2 {
    grid-template-columns: 1fr;
  }
  @media (min-width: 640px) {
    .tx-grid-2 { grid-template-columns: 1fr 1fr; }
  }
  /* Single-column form fields for the narrow flow props panel */
  .tx-grid-1 { grid-template-columns: 1fr; }
</style>
