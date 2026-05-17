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
    type RoutingRule,
    type Schema
  } from '$lib/api';
  import { locale, t } from '$lib/stores/locale';
  import Card from '$lib/components/Card.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import StageConfigForm from '$lib/components/StageConfigForm.svelte';
  import TransformListEditor from '$lib/components/TransformListEditor.svelte';
  import RoutingRuleListEditor from '$lib/components/RoutingRuleListEditor.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import { SAMPLE_FIXTURES } from '$lib/sample-fixtures';

  $: id = $page.params.id;

  let pipeline: Pipeline | null = null;
  let connections: Connection[] = [];
  let stages: Stage[] = [];
  let transforms: Transform[] = [];
  let rules: RoutingRule[] = [];
  let schemas: Schema[] = [];
  let error = '';
  let saved = '';
  let saving = false;

  $: stageTypeOptions = (
    ['filter', 'transform', 'translate', 'route', 'script', 'validate'] as StageType[]
  ).map((v) => ({ value: v, label: v }));

  async function load() {
    if (!id) return;
    try {
      const [p, conns, st, tr, rr, sc] = await Promise.all([
        api.get<Pipeline>(`/v1/pipelines/${id}`),
        api.get<Connection[]>('/v1/connections').then((v) => v ?? []),
        api.get<Stage[]>(`/v1/pipelines/${id}/stages`).then((v) => v ?? []),
        api.get<Transform[]>(`/v1/pipelines/${id}/transforms`).then((v) => v ?? []),
        api.get<RoutingRule[]>(`/v1/pipelines/${id}/routing-rules`).then((v) => v ?? []),
        api.get<Schema[]>('/v1/schemas').then((v) => v ?? [])
      ]);
      pipeline = p;
      connections = conns;
      stages = st.sort((a, b) => a.stage_order - b.stage_order);
      transforms = tr.sort((a, b) => a.order - b.order);
      rules = rr.sort((a, b) => a.priority - b.priority);
      schemas = sc;
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

  // ---------- routing ---------- (list editor + state now in RoutingRuleListEditor)

  // ---------- sample + preview ----------
  const samplePlaceholder = '{"id":"order-1","secret":"hush","total":42}';
  let sampleText = '';

  async function loadSampleFixture(body: string) {
    sampleText = body;
    await extractPaths();
  }
  let extractedPaths: string[] = [];

  /**
   * The whole product is: drop a sample → click which fields to strip →
   * those become the filter stage's `paths`. ensureFilterStage creates a
   * filter stage at order 1 (or reuses one already at the head of the
   * chain) and pushPathsToFilter merges new paths into its config.
   */
  function ensureFilterStage(): Stage {
    let s = stages.find((st) => st.stage_type === 'filter');
    if (s) return s;
    s = {
      stage_order: 1,
      stage_type: 'filter',
      stage_config: '{"paths":[]}',
      enabled: true
    };
    // New filter goes to the front; everything else shifts +1.
    stages = [s, ...stages.map((x) => ({ ...x, stage_order: x.stage_order + 1 }))];
    return s;
  }

  function readFilterPaths(s: Stage): string[] {
    try {
      const v = JSON.parse(s.stage_config || '{}');
      return Array.isArray(v.paths) ? v.paths.filter((p: unknown): p is string => typeof p === 'string') : [];
    } catch {
      return [];
    }
  }

  function writeFilterPaths(s: Stage, paths: string[]) {
    let v: Record<string, unknown> = {};
    try {
      const parsed = JSON.parse(s.stage_config || '{}');
      if (parsed && typeof parsed === 'object') v = parsed;
    } catch {
      // start fresh
    }
    v.paths = paths;
    s.stage_config = JSON.stringify(v);
    // Trigger Svelte reactivity on the array.
    stages = [...stages];
  }

  function addPathToFilter(path: string) {
    const s = ensureFilterStage();
    const current = readFilterPaths(s);
    if (current.includes(path)) return;
    writeFilterPaths(s, [...current, path]);
  }
  function addAllPathsToFilter() {
    if (extractedPaths.length === 0) return;
    const s = ensureFilterStage();
    const current = readFilterPaths(s);
    const merged = [...current];
    for (const p of extractedPaths) {
      if (!merged.includes(p)) merged.push(p);
    }
    writeFilterPaths(s, merged);
  }
  function isPathInFilter(path: string): boolean {
    const s = stages.find((st) => st.stage_type === 'filter');
    if (!s) return false;
    return readFilterPaths(s).includes(path);
  }
  $: filterPathSet = (() => {
    const s = stages.find((st) => st.stage_type === 'filter');
    return s ? new Set(readFilterPaths(s)) : new Set<string>();
  })();
  let extractedFormat = '';
  let previewOutput = '';
  let previewFormat = '';
  let previewError = '';
  let previewing = false;
  let extracting = false;

  async function onSampleFile(e: Event) {
    const target = e.target as HTMLInputElement;
    const file = target.files?.[0];
    if (!file) return;
    const text = await file.text();
    sampleText = text;
    await extractPaths();
    target.value = ''; // allow re-upload of the same file
  }

  async function extractPaths() {
    if (!sampleText) {
      extractedPaths = [];
      extractedFormat = '';
      return;
    }
    extracting = true;
    try {
      // Send the sample as the raw body — the handler auto-detects.
      const r = await api.postRaw<{ format: string; paths: string[] }>(
        '/v1/samples/extract',
        sampleText,
        'application/octet-stream'
      );
      extractedFormat = r.format || '';
      extractedPaths = r.paths || [];
    } catch (e: unknown) {
      extractedPaths = [];
      extractedFormat = '';
      previewError = (e as { message?: string }).message || 'extract failed';
    } finally {
      extracting = false;
    }
  }

  async function runPreview() {
    if (!pipeline?.id || !sampleText) return;
    previewing = true;
    previewError = '';
    previewOutput = '';
    previewFormat = '';
    try {
      // Validate stage configs before sending so the server doesn't
      // have to reject for an obvious typo.
      for (const s of stages) {
        try {
          JSON.parse(s.stage_config || '{}');
        } catch {
          throw new Error(`stage ${s.stage_order} (${s.stage_type}): config is not valid JSON`);
        }
      }
      const r = await api.post<{
        ok: boolean;
        output: string;
        format: string;
        error?: string;
        routes?: string[];
      }>('/v1/preview', {
        stages,
        transforms,
        routing_rules: rules,
        output_format: pipeline.output_format,
        sample: sampleText
      });
      if (!r.ok) {
        previewError = r.error || 'preview failed';
        return;
      }
      previewOutput = r.output;
      previewFormat = r.format;
    } catch (e: unknown) {
      previewError = (e as { message?: string }).message || 'preview failed';
    } finally {
      previewing = false;
    }
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

  // ── Replay ──────────────────────────────────────────────────────────
  //
  // Replay drives the same stage chain against a historical time window
  // on the source broker. Only sources that retain committed messages
  // (Kafka, NATS JetStream — note: core NATS does NOT) can replay; for
  // every other broker the API returns 400 and we hide the form.
  let replaySince = '';
  let replayUntil = '';
  let replaying = false;
  let replayError = '';
  let replayResult: { messages_read: number; messages_sent: number; messages_dropped: number; duration: number } | null = null;

  $: source = connections.find((c) => c.id === pipeline?.source_id);
  $: sourceSupportsReplay = source?.type === 'kafka' || (source?.type === 'nats' && !!source?.stream_name);

  async function runReplay() {
    if (!pipeline) return;
    replaying = true;
    replayError = '';
    replayResult = null;
    try {
      // datetime-local emits "YYYY-MM-DDTHH:MM" without timezone; the
      // backend expects RFC 3339. Append ":00Z" to treat the input as
      // UTC. Operators in non-UTC timezones can use the form and the
      // window is still well-defined.
      const since = replaySince.length === 16 ? replaySince + ':00Z' : replaySince;
      const until = replayUntil.length === 16 ? replayUntil + ':00Z' : replayUntil;
      replayResult = await api.post<typeof replayResult>(
        `/v1/pipelines/${pipeline.id}/replay`,
        { since, until }
      );
    } catch (e: unknown) {
      const err = e as { message?: string };
      replayError = err?.message || String(e);
    } finally {
      replaying = false;
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
          <span class="form-mode-tag">{t($locale, 'pipelines.form.tag')}</span>
          {pipeline.name}
        </h2>
        <p class="text-sm mt-1" style="color: var(--text-muted)">
          {connections.find((c) => c.id === pipeline?.source_id)?.name || '?'}
          → {connections.find((c) => c.id === pipeline?.destination_id)?.name || '?'}
          · {pipeline.output_format}
          · {pipeline.enabled ? t($locale, 'common.enabled') : t($locale, 'common.disabled')}
        </p>
        <p class="text-xs mt-2" style="color: var(--text-tertiary)">
          {t($locale, 'pipelines.form.hint')}
        </p>
      </div>
      <div class="flex items-center gap-2">
        <a href="/flow?pipeline={id}" class="btn-visual-link">
          {t($locale, 'flow.openVisual')}
        </a>
        <Button on:click={save} loading={saving}>
          {t($locale, 'pipelines.saveDeploy')}
        </Button>
      </div>
    </div>
  {/if}

  {#if error}
    <Alert variant="error" dismissible on:dismiss={() => (error = '')}>{error}</Alert>
  {/if}
  {#if saved}
    <Alert variant="success" dismissible on:dismiss={() => (saved = '')}>{saved}</Alert>
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
            <div class="mt-2">
              <StageConfigForm type={s.stage_type} bind:config={s.stage_config} {schemas} />
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </Card>

  <!-- ─── Transforms ────────────────────────────────────────────────── -->
  <Card>
    <TransformListEditor bind:transforms />
  </Card>

  <!-- ─── Routing rules ─────────────────────────────────────────────── -->
  <Card>
    <RoutingRuleListEditor bind:rules {connections} />
  </Card>

  <!-- ─── Sample & preview ─────────────────────────────────────────── -->
  <Card>
    <p class="section-heading mb-3">{t($locale, 'preview.title')}</p>
    <p class="text-sm mb-3" style="color: var(--text-muted)">
      {t($locale, 'preview.help')}
    </p>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <div>
        <label class="config-label" for="sample-file">{t($locale, 'preview.upload')}</label>
        <input id="sample-file" type="file" accept=".json,.xml,.txt"
          on:change={onSampleFile} class="file-input" />

        <div class="try-row">
          <span class="try-label">{t($locale, 'preview.try')}</span>
          {#each SAMPLE_FIXTURES as f}
            <button type="button" class="try-btn" on:click={() => loadSampleFixture(f.body)}>
              {f.label}
            </button>
          {/each}
        </div>

        <label class="config-label" for="sample-text" style="margin-top: 12px;">
          {t($locale, 'preview.sample')}
        </label>
        <textarea
          id="sample-text"
          class="config-input"
          rows="10"
          bind:value={sampleText}
          on:blur={extractPaths}
          placeholder={samplePlaceholder}></textarea>

        {#if extractedFormat || extractedPaths.length > 0}
          <div class="mt-3">
            <div class="paths-row">
              <span class="path-format">
                <Badge variant="neutral">{extractedFormat || '?'}</Badge>
              </span>
              {#each extractedPaths as p}
                <button
                  type="button"
                  class="path-chip"
                  class:on={filterPathSet.has(p)}
                  on:click={() => addPathToFilter(p)}
                  title={t($locale, 'preview.paths.chipHint')}
                >
                  {filterPathSet.has(p) ? '✓ ' : '+ '}{p}
                </button>
              {/each}
            </div>
            <p class="text-xs mt-2" style="color: var(--text-muted)">
              {t($locale, 'preview.paths.help')}
            </p>
          </div>
        {/if}

        <div class="flex gap-2 mt-3 flex-wrap">
          <Button variant="ghost" on:click={extractPaths} loading={extracting}>
            {t($locale, 'preview.extract')}
          </Button>
          {#if extractedPaths.length > 0}
            <Button variant="ghost" on:click={addAllPathsToFilter}>
              {t($locale, 'preview.paths.useAll')}
            </Button>
          {/if}
          <Button on:click={runPreview} loading={previewing}>
            {t($locale, 'preview.run')}
          </Button>
        </div>
      </div>

      <div>
        <label class="config-label" for="preview-output">
          {t($locale, 'preview.output')}
          {#if previewFormat}<Badge variant="neutral">{previewFormat}</Badge>{/if}
        </label>
        <textarea
          id="preview-output"
          class="config-input"
          rows="10"
          readonly
          value={previewOutput}
          placeholder={t($locale, 'preview.outputPlaceholder')}></textarea>
        {#if previewError}
          <div class="mt-2">
            <Alert variant="error" dismissible on:dismiss={() => (previewError = '')}>
              {previewError}
            </Alert>
          </div>
        {/if}
      </div>
    </div>
  </Card>

  {#if pipeline && sourceSupportsReplay}
    <Card>
      <p class="section-heading mb-2">{t($locale, 'replay.title')}</p>
      <p class="text-sm mb-3" style="color: var(--text-muted)">
        {t($locale, 'replay.description')}
      </p>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <label class="block text-sm">
          <span class="block mb-1" style="color: var(--text-muted)">{t($locale, 'replay.since')}</span>
          <input
            type="datetime-local"
            class="w-full rounded px-2 py-1.5 text-sm"
            style="background: var(--surface); color: var(--text); border: 1px solid var(--border)"
            bind:value={replaySince}
          />
        </label>
        <label class="block text-sm">
          <span class="block mb-1" style="color: var(--text-muted)">{t($locale, 'replay.until')}</span>
          <input
            type="datetime-local"
            class="w-full rounded px-2 py-1.5 text-sm"
            style="background: var(--surface); color: var(--text); border: 1px solid var(--border)"
            bind:value={replayUntil}
          />
        </label>
      </div>
      <div class="mt-3 flex items-center gap-3">
        <Button on:click={runReplay} loading={replaying} disabled={!replaySince || !replayUntil}>
          {t($locale, 'replay.run')}
        </Button>
        {#if replayResult}
          <span class="text-sm" style="color: var(--text-muted)">
            read {replayResult.messages_read} · sent {replayResult.messages_sent} ·
            dropped {replayResult.messages_dropped} ·
            {Math.round(replayResult.duration / 1_000_000)}ms
          </span>
        {/if}
      </div>
      {#if replayError}
        <div class="mt-2">
          <Alert variant="error" dismissible on:dismiss={() => (replayError = '')}>
            {replayError}
          </Alert>
        </div>
      {/if}
    </Card>
  {/if}
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

  .file-input {
    display: block;
    color: var(--text-muted);
    font-size: 13px;
  }
  .paths-row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .path-chip {
    border: 1px solid var(--border);
    border-radius: 12px; /* labeled chip — design system §5 / Rule 9 (pill is count-badge only) */
    padding: 4px 12px;
    font-size: 12px;
    color: var(--text);
    background: var(--surface);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    cursor: pointer;
    transition: border-color 120ms, background-color 120ms, color 120ms;
  }
  .path-chip:hover { border-color: var(--accent); }
  .path-chip.on {
    border-color: var(--accent);
    background: var(--accent);
    color: var(--bg);
    font-weight: 600;
  }
  .path-chip:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .path-format { margin-inline-end: 4px; }

  .try-row {
    margin-top: 8px;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
  }
  .try-label {
    font-size: 12px;
    color: var(--text-muted);
  }
  .try-btn {
    border: 1px solid var(--border);
    border-radius: 12px; /* labeled chip — design system §5 / Rule 9 (pill is count-badge only) */
    padding: 2px 10px;
    font-size: 11px;
    background: var(--surface);
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    cursor: pointer;
    transition: border-color 120ms, color 120ms;
  }
  .try-btn:hover { border-color: var(--accent); color: var(--accent); }
  /*
   * Phase 5 demotion cues — this page is now the secondary (form) view
   * of a pipeline; the visual editor at /flow?pipeline=:id is the
   * recommended workflow. We mark that with an uppercase eyebrow before
   * the pipeline name and a filled-gold link to jump into the canvas.
   */
  .form-mode-tag {
    display: inline-block;
    margin-inline-end: 8px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--section-header);
    vertical-align: middle;
  }
  .btn-visual-link {
    display: inline-flex;
    align-items: center;
    padding: 8px 14px;
    border-radius: 12px;
    color: var(--primary-on);
    background: var(--primary);
    font-size: 14px;
    font-weight: 500;
    line-height: 1.2;
    text-decoration: none;
    transition: background-color 200ms;
  }
  .btn-visual-link:hover { background: var(--palette-copper); }
  .btn-visual-link:active { background: var(--palette-gold-muted); }
</style>
