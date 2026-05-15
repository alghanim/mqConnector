<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import {
    api,
    type Pipeline,
    type Connection,
    type Stage,
    type StageType,
    type Transform,
    type TransformType,
    type RoutingRule,
    type RoutingOperator
  } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';

  $: id = $page.params.id;

  let pipeline: Pipeline | null = null;
  let connections: Connection[] = [];
  let stages: Stage[] = [];
  let transforms: Transform[] = [];
  let rules: RoutingRule[] = [];
  let error = '';
  let saved = '';
  let saving = false;

  $: stageTypeOptions = (
    ['filter', 'transform', 'translate', 'route', 'script', 'validate'] as StageType[]
  ).map((v) => ({ value: v, label: v }));
  $: transformTypeOptions = (
    ['rename', 'mask', 'move', 'set', 'delete'] as TransformType[]
  ).map((v) => ({ value: v, label: v }));
  $: routingOpOptions = (
    ['eq', 'neq', 'contains', 'regex', 'gt', 'lt', 'exists'] as RoutingOperator[]
  ).map((v) => ({ value: v, label: v }));
  $: connOptions = connections.map((c) => ({
    value: c.id || '',
    label: `${c.name} (${c.type})`
  }));

  async function load() {
    if (!id) return;
    try {
      const [p, conns, st, tr, rr] = await Promise.all([
        api.get<Pipeline>(`/v1/pipelines/${id}`),
        api.get<Connection[]>('/v1/connections').then((v) => v ?? []),
        api.get<Stage[]>(`/v1/pipelines/${id}/stages`).then((v) => v ?? []),
        api.get<Transform[]>(`/v1/pipelines/${id}/transforms`).then((v) => v ?? []),
        api.get<RoutingRule[]>(`/v1/pipelines/${id}/routing-rules`).then((v) => v ?? [])
      ]);
      pipeline = p;
      connections = conns;
      stages = st.sort((a, b) => a.stage_order - b.stage_order);
      transforms = tr.sort((a, b) => a.order - b.order);
      rules = rr.sort((a, b) => a.priority - b.priority);
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'failed to load';
    }
  }

  onMount(load);

  // ---------- stages ----------
  function addStage() {
    stages = [
      ...stages,
      {
        stage_order: stages.length + 1,
        stage_type: 'filter',
        stage_config: '{}',
        enabled: true
      }
    ];
  }
  function removeStage(i: number) {
    stages = stages.filter((_, idx) => idx !== i).map((s, idx) => ({ ...s, stage_order: idx + 1 }));
  }
  function moveStage(i: number, dir: -1 | 1) {
    const j = i + dir;
    if (j < 0 || j >= stages.length) return;
    const copy = stages.slice();
    [copy[i], copy[j]] = [copy[j], copy[i]];
    stages = copy.map((s, idx) => ({ ...s, stage_order: idx + 1 }));
  }

  // ---------- transforms ----------
  function addTransform() {
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
  function removeTransform(i: number) {
    transforms = transforms
      .filter((_, idx) => idx !== i)
      .map((tr, idx) => ({ ...tr, order: idx + 1 }));
  }

  // ---------- routing ----------
  function addRule() {
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
  function removeRule(i: number) {
    rules = rules.filter((_, idx) => idx !== i);
  }

  // ---------- save ----------
  async function save() {
    if (!pipeline?.id) return;
    saving = true;
    saved = '';
    try {
      // Each stage's config must be valid JSON before we send.
      for (const s of stages) {
        try {
          JSON.parse(s.stage_config || '{}');
        } catch {
          throw new Error(`stage ${s.stage_order} (${s.stage_type}): config is not valid JSON`);
        }
      }
      await Promise.all([
        api.put(`/v1/pipelines/${pipeline.id}/stages`, stages),
        api.put(`/v1/pipelines/${pipeline.id}/transforms`, transforms),
        api.put(`/v1/pipelines/${pipeline.id}/routing-rules`, rules)
      ]);
      // The Manager hot-reloads on update, but POST /reload is the explicit
      // way to be sure the workers picked up the change.
      await api.post('/v1/reload');
      saved = t($locale, 'pipelines.saved');
      error = '';
    } catch (e: unknown) {
      error = (e as { message?: string }).message || 'save failed';
    } finally {
      saving = false;
    }
  }
</script>

<div class="space-y-6 max-w-5xl">
  <a href="/pipelines" style="color: var(--accent); font-size: 14px;">
    {t($locale, 'pipelines.back')}
  </a>

  {#if pipeline}
    <div class="flex items-baseline justify-between">
      <div>
        <h2 class="text-2xl font-semibold" style="color: var(--text)">
          {pipeline.name}
        </h2>
        <p class="text-sm mt-1" style="color: var(--text-muted)">
          {connections.find((c) => c.id === pipeline?.source_id)?.name || '?'}
          → {connections.find((c) => c.id === pipeline?.destination_id)?.name || '?'}
          · {pipeline.output_format}
          · {pipeline.enabled ? t($locale, 'common.enabled') : t($locale, 'common.disabled')}
        </p>
      </div>
      <Button on:click={save} loading={saving}>
        {t($locale, 'pipelines.saveDeploy')}
      </Button>
    </div>
  {/if}

  {#if error}
    <p style="color: var(--danger)">{error}</p>
  {/if}
  {#if saved}
    <p style="color: var(--success)">{saved}</p>
  {/if}

  <!-- ─── Stages ────────────────────────────────────────────────────── -->
  <Card>
    <div class="flex items-center justify-between mb-3">
      <p class="section-heading">{t($locale, 'pipelines.stages')}</p>
      <Button variant="ghost" on:click={addStage}>{t($locale, 'pipelines.stages.add')}</Button>
    </div>
    {#if stages.length === 0}
      <p style="color: var(--text-muted)">{t($locale, 'pipelines.stages.empty')}</p>
    {:else}
      <div class="space-y-3">
        {#each stages as s, i (i)}
          <div class="stage-row">
            <div class="stage-head">
              <Badge variant="neutral">#{s.stage_order}</Badge>
              <Select
                bind:value={s.stage_type}
                options={stageTypeOptions}
                label={t($locale, 'pipelines.stages.type')}
              />
              <label class="enable">
                <input type="checkbox" bind:checked={s.enabled} />
                {t($locale, 'common.enabled')}
              </label>
              <div class="row-actions">
                <Button variant="ghost" on:click={() => moveStage(i, -1)}
                  >{t($locale, 'pipelines.stages.up')}</Button>
                <Button variant="ghost" on:click={() => moveStage(i, 1)}
                  >{t($locale, 'pipelines.stages.down')}</Button>
                <Button variant="outline" on:click={() => removeStage(i)}
                  >{t($locale, 'common.delete')}</Button>
              </div>
            </div>
            <label class="config-label" for="cfg-{i}">{t($locale, 'pipelines.stages.config')}</label>
            <textarea id="cfg-{i}" class="config-input" bind:value={s.stage_config} rows="3"></textarea>
          </div>
        {/each}
      </div>
    {/if}
  </Card>

  <!-- ─── Transforms ────────────────────────────────────────────────── -->
  <Card>
    <div class="flex items-center justify-between mb-3">
      <p class="section-heading">{t($locale, 'pipelines.transforms')}</p>
      <Button variant="ghost" on:click={addTransform}>{t($locale, 'pipelines.transforms.add')}</Button>
    </div>
    {#if transforms.length === 0}
      <p style="color: var(--text-muted)">{t($locale, 'pipelines.transforms.empty')}</p>
    {:else}
      <div class="space-y-3">
        {#each transforms as tr, i (i)}
          <div class="stage-row">
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <Select
                bind:value={tr.transform_type}
                options={transformTypeOptions}
                label={t($locale, 'pipelines.transforms.type')}
              />
              <Input bind:value={tr.source_path} label={t($locale, 'pipelines.transforms.sourcePath')} />
              {#if tr.transform_type === 'rename' || tr.transform_type === 'move'}
                <Input bind:value={tr.target_path} label={t($locale, 'pipelines.transforms.targetPath')} />
              {/if}
              {#if tr.transform_type === 'mask'}
                <Input bind:value={tr.mask_pattern} label={t($locale, 'pipelines.transforms.maskPattern')} />
                <Input bind:value={tr.mask_replace} label={t($locale, 'pipelines.transforms.maskReplace')} />
              {/if}
              {#if tr.transform_type === 'set'}
                <Input bind:value={tr.set_value} label={t($locale, 'pipelines.transforms.setValue')} />
              {/if}
            </div>
            <div class="flex justify-end mt-2">
              <Button variant="outline" on:click={() => removeTransform(i)}
                >{t($locale, 'common.delete')}</Button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </Card>

  <!-- ─── Routing rules ─────────────────────────────────────────────── -->
  <Card>
    <div class="flex items-center justify-between mb-3">
      <p class="section-heading">{t($locale, 'pipelines.routing')}</p>
      <Button variant="ghost" on:click={addRule}>{t($locale, 'pipelines.routing.add')}</Button>
    </div>
    {#if rules.length === 0}
      <p style="color: var(--text-muted)">{t($locale, 'pipelines.routing.empty')}</p>
    {:else}
      <div class="space-y-3">
        {#each rules as r, i (i)}
          <div class="stage-row">
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <Input bind:value={r.condition_path} label={t($locale, 'pipelines.routing.path')} />
              <Select
                bind:value={r.condition_operator}
                options={routingOpOptions}
                label={t($locale, 'pipelines.routing.operator')}
              />
              {#if r.condition_operator !== 'exists'}
                <Input bind:value={r.condition_value} label={t($locale, 'pipelines.routing.value')} />
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
              <label class="enable">
                <input type="checkbox" bind:checked={r.enabled} />
                {t($locale, 'common.enabled')}
              </label>
            </div>
            <div class="flex justify-end mt-2">
              <Button variant="outline" on:click={() => removeRule(i)}
                >{t($locale, 'common.delete')}</Button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </Card>
</div>

<style>
  .stage-row {
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 12px 14px;
    background: var(--surface);
  }
  .stage-head {
    display: flex;
    gap: 12px;
    align-items: end;
    flex-wrap: wrap;
  }
  .row-actions {
    margin-inline-start: auto;
    display: flex;
    gap: 6px;
  }
  .enable {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--text);
    font-size: 13px;
  }
  .config-label {
    display: block;
    margin-top: 10px;
    margin-bottom: 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .config-input {
    width: 100%;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    padding: 8px 10px;
    resize: vertical;
  }
  .config-input:focus { outline: 2px solid var(--accent); }
</style>
