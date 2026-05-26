<!--
  ExplanationCard — generic renderer for an `Explanation` wire object.

  Three tabs of the Observability page (Latency / Drift / Circuit) all
  call the same backend endpoint and render the same shape. This
  component is the shared body so the tab markup stays thin.

  Layout:

    ┌───── severity-tinted top border ────────────────────────┐
    │ Headline (h2)                                            │
    │ [AI summary chip + paraphrase] (only when source === ai) │
    │ Facts list — label · value · source attribution         │
    │ Sections (rendered per kind)                            │
    │   stages    → <WaterfallStages>                          │
    │   timeline  → table of { at, from, to, reason }          │
    │   fields    → key/value chip grid                        │
    │   narrative → plain prose                                │
    │   (unknown) → JSON dump in a <pre>                       │
    └──────────────────────────────────────────────────────────┘

  The component is intentionally dumb — no fetching, no auto-refresh.
  Pass in `explanation` from the parent.

  AI sourcing:
    `aiSource === 'ai'`            → show the summary block + the "AI"
                                     badge linked to /governance/audit?source=ai
    `aiSource === 'deterministic'` → opt was requested but the LLM
                                     failed; we don't render the chip
                                     (the structured headline IS the
                                     paraphrase in that fallback path)
    `aiSource === ''`              → ?ai=summary wasn't requested; same
                                     as above — no chip.
-->
<script lang="ts">
  import type { Explanation, LatencyStagesData } from '$lib/api';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import WaterfallStages from '$lib/components/charts/WaterfallStages.svelte';
  import { Sparkles } from 'lucide-svelte';
  import { createEventDispatcher } from 'svelte';

  export let explanation: Explanation | null = null;
  export let loading: boolean = false;
  export let aiSummary: string = '';
  export let aiSource: 'ai' | 'deterministic' | '' = '';

  const dispatch = createEventDispatcher<{ stage: { stageName: string } }>();

  function toneOf(sev: string): 'info' | 'warning' | 'danger' {
    if (sev === 'critical') return 'danger';
    if (sev === 'warning') return 'warning';
    return 'info';
  }

  // Section payload extractors. The wire's `data` field is `unknown`;
  // we defensively narrow per kind and pass an empty fallback so a
  // malformed payload never crashes the render.
  function asStages(data: unknown): LatencyStagesData {
    if (data && typeof data === 'object') {
      const d = data as Partial<LatencyStagesData>;
      return {
        stages: Array.isArray(d.stages) ? d.stages : [],
        total_p99: typeof d.total_p99 === 'number' ? d.total_p99 : 0
      };
    }
    return { stages: [], total_p99: 0 };
  }
  type TimelineEntry = { at?: string; from?: string; to?: string; reason?: string };
  function asTimeline(data: unknown): TimelineEntry[] {
    if (Array.isArray(data)) return data as TimelineEntry[];
    if (data && typeof data === 'object') {
      const d = data as { entries?: unknown };
      if (Array.isArray(d.entries)) return d.entries as TimelineEntry[];
    }
    return [];
  }
  type FieldEntry = { name?: string; key?: string; value?: string };
  function asFields(data: unknown): FieldEntry[] {
    if (Array.isArray(data)) return data as FieldEntry[];
    if (data && typeof data === 'object') {
      const d = data as { fields?: unknown };
      if (Array.isArray(d.fields)) return d.fields as FieldEntry[];
    }
    return [];
  }
  function asNarrative(data: unknown): string {
    if (typeof data === 'string') return data;
    if (data && typeof data === 'object') {
      const d = data as { text?: unknown };
      if (typeof d.text === 'string') return d.text;
    }
    return '';
  }
</script>

<div
  class="exp-card"
  data-severity={explanation?.severity ?? 'info'}
  data-testid="explanation-card"
>
  {#if loading}
    <div class="exp-skel">
      <Skeleton width="60%" height="1.4rem" />
      <Skeleton width="80%" height="0.85em" />
      <Skeleton width="65%" height="0.85em" />
      <Skeleton width="50%" height="0.85em" />
    </div>
  {:else if !explanation}
    <p class="exp-empty">No explanation available.</p>
  {:else}
    <header class="exp-head">
      <div class="exp-head-row">
        <Badge variant={toneOf(explanation.severity)}>{explanation.severity}</Badge>
        <h2 class="exp-headline">{explanation.headline}</h2>
      </div>
      {#if aiSource === 'ai' && aiSummary}
        <div class="exp-ai" data-testid="explanation-ai">
          <span class="exp-ai-chip">
            <Sparkles size={11} aria-hidden="true" />
            <span>AI</span>
          </span>
          <p class="exp-ai-text">{aiSummary}</p>
          <a class="exp-ai-link" href="/governance/audit?source=ai">audit</a>
        </div>
      {/if}
    </header>

    {#if explanation.facts && explanation.facts.length > 0}
      <ul class="exp-facts" aria-label="Facts">
        {#each explanation.facts as f (f.label)}
          <li class="exp-fact">
            <span class="exp-fact-label">{f.label}</span>
            <span class="exp-fact-value">{f.value}</span>
            {#if f.source}
              <span class="exp-fact-source" title={f.source}>
                {f.source}
              </span>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}

    {#if explanation.sections}
      <div class="exp-sections">
        {#each explanation.sections as sec, i (i)}
          <section class="exp-section" data-kind={sec.kind}>
            {#if sec.title}
              <h3 class="exp-section-title">{sec.title}</h3>
            {/if}
            {#if sec.kind === 'stages'}
              {@const s = asStages(sec.data)}
              <WaterfallStages
                stages={s.stages}
                total_p99_ms={s.total_p99}
                on:select={(e) => dispatch('stage', { stageName: e.detail.stageName })}
              />
            {:else if sec.kind === 'timeline'}
              {@const entries = asTimeline(sec.data)}
              {#if entries.length === 0}
                <p class="exp-section-empty">No timeline entries.</p>
              {:else}
                <table class="exp-timeline">
                  <thead>
                    <tr>
                      <th>At</th>
                      <th>From</th>
                      <th>To</th>
                      <th>Reason</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each entries as t, idx (idx)}
                      <tr>
                        <td class="num">{t.at ?? '—'}</td>
                        <td>{t.from ?? ''}</td>
                        <td>{t.to ?? ''}</td>
                        <td>{t.reason ?? ''}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              {/if}
            {:else if sec.kind === 'fields'}
              {@const fields = asFields(sec.data)}
              {#if fields.length === 0}
                <p class="exp-section-empty">No fields.</p>
              {:else}
                <div class="exp-fields">
                  {#each fields as f, idx (idx)}
                    <div class="exp-field-chip">
                      <span class="exp-field-key">{f.name ?? f.key ?? '—'}</span>
                      <span class="exp-field-val">{f.value ?? ''}</span>
                    </div>
                  {/each}
                </div>
              {/if}
            {:else if sec.kind === 'narrative'}
              {@const text = asNarrative(sec.data)}
              {#if text}
                <p class="exp-narrative">{text}</p>
              {:else}
                <p class="exp-section-empty">No narrative.</p>
              {/if}
            {:else}
              <pre class="exp-dump">{JSON.stringify(sec.data, null, 2)}</pre>
            {/if}
          </section>
        {/each}
      </div>
    {/if}

    {#if explanation.sources && explanation.sources.length > 0}
      <p class="exp-sources">
        sources · {explanation.sources.join(' · ')}
      </p>
    {/if}
  {/if}
</div>

<style>
  .exp-card {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 14px 16px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 12px;
    border-block-start-width: 3px;
  }
  .exp-card[data-severity='info'] {
    border-block-start-color: var(--info);
  }
  .exp-card[data-severity='warning'] {
    border-block-start-color: var(--warning);
  }
  .exp-card[data-severity='critical'] {
    border-block-start-color: var(--danger);
  }
  .exp-skel {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .exp-empty {
    color: var(--text-tertiary);
    font-size: 13px;
    margin: 0;
  }

  .exp-head {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .exp-head-row {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    flex-wrap: wrap;
  }
  .exp-headline {
    margin: 0;
    color: var(--text);
    font-size: 16px;
    font-weight: 600;
    line-height: 1.35;
  }
  .exp-ai {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 8px 10px;
    background: color-mix(in srgb, var(--info) 8%, var(--surface));
    border: 1px solid color-mix(in srgb, var(--info) 30%, var(--border));
    border-radius: 8px;
    flex-wrap: wrap;
  }
  .exp-ai-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding-inline: 6px;
    padding-block: 2px;
    border-radius: 4px;
    background: color-mix(in srgb, var(--info) 20%, transparent);
    color: var(--info);
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    flex-shrink: 0;
  }
  .exp-ai-text {
    margin: 0;
    flex: 1 1 0;
    min-inline-size: 0;
    color: var(--text);
    font-size: 13px;
    line-height: 1.45;
  }
  .exp-ai-link {
    color: var(--text-tertiary);
    font-size: 11px;
    text-decoration: none;
    border-block-end: 1px dashed currentColor;
    align-self: center;
  }
  .exp-ai-link:hover {
    color: var(--text-muted);
  }

  .exp-facts {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .exp-fact {
    display: grid;
    grid-template-columns: minmax(110px, 0.7fr) minmax(0, 1.3fr) auto;
    gap: 10px;
    align-items: baseline;
    padding-block: 4px;
    border-block-end: 1px dashed var(--border);
  }
  .exp-fact:last-child {
    border-block-end: none;
  }
  .exp-fact-label {
    color: var(--text-muted);
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-weight: 600;
  }
  .exp-fact-value {
    color: var(--text);
    font-size: 13px;
    font-variant-numeric: tabular-nums;
    word-break: break-word;
  }
  .exp-fact-source {
    color: var(--text-tertiary);
    font-size: 10px;
    font-family: 'SFMono-Regular', Menlo, monospace;
    text-align: end;
    cursor: help;
    max-inline-size: 14rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .exp-sections {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .exp-section {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .exp-section-title {
    margin: 0;
    color: var(--text-muted);
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 600;
  }
  .exp-section-empty {
    color: var(--text-tertiary);
    font-size: 12px;
    margin: 0;
    font-style: italic;
  }
  .exp-timeline {
    inline-size: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }
  .exp-timeline th {
    text-align: start;
    color: var(--text-muted);
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 4px 6px;
    border-block-end: 1px solid var(--border);
  }
  .exp-timeline td {
    padding: 4px 6px;
    border-block-end: 1px dashed var(--border);
    color: var(--text);
    vertical-align: top;
  }
  .exp-timeline .num {
    font-variant-numeric: tabular-nums;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .exp-fields {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .exp-field-chip {
    display: inline-flex;
    align-items: baseline;
    gap: 6px;
    padding-block: 3px;
    padding-inline: 8px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    font-size: 11px;
  }
  .exp-field-key {
    color: var(--text-tertiary);
    font-family: 'SFMono-Regular', Menlo, monospace;
  }
  .exp-field-val {
    color: var(--text);
    font-variant-numeric: tabular-nums;
  }
  .exp-narrative {
    margin: 0;
    color: var(--text);
    font-size: 13px;
    line-height: 1.5;
  }
  .exp-dump {
    margin: 0;
    padding: 8px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text-muted);
    font-family: 'SFMono-Regular', Menlo, monospace;
    font-size: 11px;
    line-height: 1.4;
    overflow-x: auto;
    max-block-size: 240px;
  }

  .exp-sources {
    margin: 0;
    color: var(--text-tertiary);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-weight: 600;
  }
</style>
