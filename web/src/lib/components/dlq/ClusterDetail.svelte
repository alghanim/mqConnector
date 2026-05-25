<!--
  ClusterDetail — center panel of the /dlq Cluster console.

  Shows the selected cluster's headline + AI summary/suggestion + a 7-day
  occurrence heatmap + the representative payload + a clickable timeline
  of recent entries. Clicking a recent entry promotes it to the action
  drawer on the right (via the `selectEntry` event).

  Data flow:
    * `cluster` — DLQCluster from the parent. Always non-null when this
      component renders (the parent shows an empty-state otherwise).
    * `representative` — full DLQEntry for the cluster's
      representative_id. The parent fetches it lazily on selection.
    * `recentEntries` — DLQEntry[] for the cluster's recent_ids. The
      parent fetches each one lazily; while we're waiting, we render the
      id stubs as a degraded timeline.
    * `selectedEntryId` — the id currently active in the drawer; we
      visually highlight it in the timeline.

  Decoding of the payload mirrors the legacy drawer pattern (JSON
  pretty-print, binary detect with hex preview). Pulled here so the
  Cluster console doesn't depend on the legacy drawer remaining around.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import Card from '$lib/components/Card.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Heatmap from '$lib/components/charts/Heatmap.svelte';
  import AINameBadge from './AINameBadge.svelte';
  import type { DLQCluster, DLQEntry, Pipeline } from '$lib/api';
  import { pipelineLabelOrDeleted } from '$lib/stores/catalogue';
  import { locale, t } from '$lib/stores/locale';

  export let cluster: DLQCluster;
  export let representative: DLQEntry | null = null;
  /** Recent-entries we've actually resolved; keys may be a subset of
      cluster.recent_ids while fetches are still in flight. */
  export let recentEntries: Map<string, DLQEntry> = new Map();
  export let selectedEntryId: string | null = null;
  export let pipelineMap: Map<string, Pipeline> = new Map();
  export let deletedLabel = '(deleted)';

  const dispatch = createEventDispatcher<{ selectEntry: { entry: DLQEntry } }>();

  // ─── Heatmap binning ────────────────────────────────────────────
  // Backend doesn't expose per-cluster per-hour buckets today (Wave 4
  // TODO). For v1 we synthesise a 168-bucket array from the cluster's
  // (first_seen, last_seen, count) tuple: spread `count` events over
  // the time window's hourly slots, weighted toward the most-recent
  // hour to match the eye's expectation. It's a rough visualisation
  // — good enough to spot "this cluster fires every hour of the day"
  // vs "this fires once a week". Real per-hour bins land in Wave 4.
  $: heatmapBuckets = (() => {
    const buckets = new Array<number>(168).fill(0);
    if (cluster.count <= 0) return buckets;
    const last = Date.parse(cluster.last_seen);
    const first = Date.parse(cluster.first_seen);
    if (Number.isNaN(last) || Number.isNaN(first)) return buckets;

    // Cap the window at 7 days. The heatmap is "last 7 days"; anything
    // older than that just stays at index 0. The "today" row is index
    // 6 (rows 0..6); each row is a 24-cell hour band.
    const now = Date.now();
    const windowMs = 7 * 86_400_000;
    const earliest = now - windowMs;
    const lo = Math.max(first, earliest);
    const hi = Math.min(last, now);
    if (hi < lo) return buckets;

    // Index helper: hours-ago → bucket index (0 oldest, 167 newest).
    function bucketIndexFor(t: number): number {
      const ago = now - t;
      const hourBlock = Math.floor(ago / 3_600_000);
      // Day 6 (today) is the bottom row; hour 0 is the leftmost cell.
      // The expected ordering is row = (6 - daysAgo), col = (23 - hoursInDay).
      // Translate that into the 168 array by:
      //   idx = row*24 + col
      // where the array is oldest→newest row-major.
      const day = Math.floor(hourBlock / 24);
      const hourInDay = hourBlock % 24;
      const row = 6 - day; // 0 oldest, 6 today
      if (row < 0 || row > 6) return -1;
      // The current real hour-of-day on the user's clock:
      const currentHour = new Date(now).getHours();
      // col such that col=currentHour is "this hour today" (rightmost
      // for today's row). For older rows the same column maps to the
      // same wall-clock hour.
      let col = currentHour - hourInDay;
      while (col < 0) col += 24;
      return row * 24 + col;
    }

    // Spread the count over the touched bucket range. We weight the
    // most-recent bucket more heavily — most clusters trend rather
    // than spike, but the eye expects fresh activity to sit at the
    // bottom-right corner. Simple linear ramp from 1 (oldest) to 3
    // (newest); normalise so the total adds up to count.
    const idxStart = bucketIndexFor(lo);
    const idxEnd = bucketIndexFor(hi);
    if (idxStart < 0 || idxEnd < 0) return buckets;
    const lo2 = Math.min(idxStart, idxEnd);
    const hi2 = Math.max(idxStart, idxEnd);

    if (lo2 === hi2) {
      buckets[lo2] = cluster.count;
      return buckets;
    }
    const span = hi2 - lo2 + 1;
    // Triangular weighting.
    const weights = new Array<number>(span);
    let total = 0;
    for (let i = 0; i < span; i++) {
      const w = 1 + (2 * i) / Math.max(1, span - 1);
      weights[i] = w;
      total += w;
    }
    for (let i = 0; i < span; i++) {
      const share = (weights[i] / total) * cluster.count;
      buckets[lo2 + i] = Math.max(0, Math.round(share));
    }
    return buckets;
  })();

  // ─── Payload decoding ──────────────────────────────────────────
  // Same logic as the legacy /dlq drawer. Inline-copied so the new
  // panel doesn't import from the page module.
  type Decoded = { text: string; binary: boolean; bytes: number };
  function decodePayload(b64: string): Decoded {
    try {
      const raw = atob(b64);
      let ctrl = 0;
      for (let i = 0; i < raw.length; i++) {
        const c = raw.charCodeAt(i);
        if (c < 32 && c !== 9 && c !== 10 && c !== 13) ctrl++;
      }
      const binary = ctrl > raw.length * 0.05;
      if (binary) {
        const hex = Array.from(raw.slice(0, 512))
          .map((c) => c.charCodeAt(0).toString(16).padStart(2, '0'))
          .join(' ');
        return { text: hex, binary: true, bytes: raw.length };
      }
      try {
        const parsed = JSON.parse(raw);
        return { text: JSON.stringify(parsed, null, 2), binary: false, bytes: raw.length };
      } catch {
        return { text: raw, binary: false, bytes: raw.length };
      }
    } catch {
      return { text: '(undecodable)', binary: false, bytes: 0 };
    }
  }

  $: decoded = representative ? decodePayload(representative.original_msg) : null;

  // ─── Relative time formatter (local copy — same as ClusterCard) ──
  function formatRelative(ts: string | undefined, lc: 'en' | 'ar'): string {
    if (!ts) return '';
    const at = Date.parse(ts);
    if (Number.isNaN(at)) return '';
    const diffSec = (Date.now() - at) / 1000;
    const abs = Math.abs(diffSec);
    const past = diffSec >= 0;
    type Unit = 'second' | 'minute' | 'hour' | 'day';
    let value: number;
    let unit: Unit;
    if (abs < 60) {
      value = Math.max(1, Math.round(abs));
      unit = 'second';
    } else if (abs < 3600) {
      value = Math.round(abs / 60);
      unit = 'minute';
    } else if (abs < 86_400) {
      value = Math.round(abs / 3600);
      unit = 'hour';
    } else {
      value = Math.round(abs / 86_400);
      unit = 'day';
    }
    const key = `time.${unit}${value === 1 ? '' : 's'}`;
    const unitLabel = t(lc, key);
    return past
      ? t(lc, 'time.ago').replace('{n}', String(value)).replace('{unit}', unitLabel)
      : t(lc, 'time.in').replace('{n}', String(value)).replace('{unit}', unitLabel);
  }

  // Title: prefer the AI title; fall back to the cluster template.
  $: titleText = cluster.ai_name?.title?.trim() || cluster.template || '(unnamed cluster)';

  function pickEntry(entry: DLQEntry) {
    dispatch('selectEntry', { entry });
  }
</script>

<div class="cluster-detail" data-testid="cluster-detail">
  <header class="cluster-detail-head">
    <div class="cluster-detail-title-row">
      <h2 class="cluster-detail-title" title={titleText}>{titleText}</h2>
      {#if cluster.ai_source}
        <AINameBadge source={cluster.ai_source} />
      {/if}
    </div>
    <p class="cluster-detail-template">
      <span class="cluster-detail-template-label">{t($locale, 'dlq.clusters.detail.template')}:</span>
      <code title={cluster.template}>{cluster.template}</code>
    </p>
    <p class="cluster-detail-fp">
      <span>{t($locale, 'dlq.clusters.detail.fingerprint')}:</span>
      <code>{cluster.fingerprint}</code>
    </p>
    <div class="cluster-detail-stats">
      <span class="cluster-detail-stat">
        <Badge variant="count">{cluster.count}</Badge>
      </span>
      <span class="cluster-detail-stat-meta">
        {formatRelative(cluster.first_seen, $locale)} → {formatRelative(cluster.last_seen, $locale)}
      </span>
    </div>
  </header>

  {#if cluster.ai_name?.summary}
    <p class="cluster-detail-summary">{cluster.ai_name.summary}</p>
  {/if}

  <Card>
    <p class="section-heading">{t($locale, 'dlq.clusters.detail.failingStages')}</p>
    <div class="cluster-detail-chips" data-testid="failing-stages">
      {#each cluster.failing_stages as stage (stage)}
        <span class="cluster-detail-chip">{stage}</span>
      {:else}
        <span class="cluster-detail-empty-line">—</span>
      {/each}
    </div>
    <p class="section-heading mt-3">{t($locale, 'dlq.clusters.detail.pipelines')}</p>
    <div class="cluster-detail-chips">
      {#each cluster.pipelines_affected as pid (pid)}
        <span class="cluster-detail-chip" title={pid}>
          {pipelineLabelOrDeleted(pid, pipelineMap, deletedLabel)}
        </span>
      {:else}
        <span class="cluster-detail-empty-line">—</span>
      {/each}
    </div>
  </Card>

  <Card>
    <Heatmap buckets={heatmapBuckets} label={t($locale, 'dlq.clusters.heatmap')} />
  </Card>

  <Card>
    <p class="section-heading">{t($locale, 'dlq.clusters.detail.representative')}</p>
    {#if representative && decoded}
      <div class="cluster-detail-rep-meta">
        <time datetime={representative.created_at} title={representative.created_at}>
          {formatRelative(representative.created_at, $locale)}
        </time>
        <span class="cluster-detail-rep-id" title={representative.id}>
          {representative.id.slice(0, 8)}
        </span>
        <span class="cluster-detail-rep-bytes">
          {decoded.bytes} {t($locale, 'dlq.detail.bytes')}
        </span>
      </div>
      <pre
        class="cluster-detail-payload"
        class:is-binary={decoded.binary}>{decoded.text}</pre>
      {#if decoded.binary}
        <p class="cluster-detail-binary-hint">{t($locale, 'dlq.detail.binary')}</p>
      {/if}
    {:else}
      <p class="cluster-detail-empty-line">…</p>
    {/if}
  </Card>

  <Card>
    <p class="section-heading">{t($locale, 'dlq.clusters.detail.recent')}</p>
    <ul class="cluster-detail-timeline" data-testid="cluster-timeline">
      {#each cluster.recent_ids as rid (rid)}
        {@const entry = recentEntries.get(rid)}
        <li>
          <button
            type="button"
            class="cluster-detail-timeline-item"
            class:is-active={selectedEntryId === rid}
            disabled={!entry}
            on:click={() => entry && pickEntry(entry)}
            title={entry?.error_reason ?? rid}
          >
            <code class="cluster-detail-timeline-id">{rid.slice(0, 8)}</code>
            {#if entry}
              <span class="cluster-detail-timeline-when">
                {formatRelative(entry.created_at, $locale)}
              </span>
            {:else}
              <span class="cluster-detail-timeline-when">…</span>
            {/if}
          </button>
        </li>
      {/each}
    </ul>
  </Card>

  {#if cluster.ai_name?.suggestion}
    <Card>
      <p class="section-heading">{t($locale, 'dlq.clusters.detail.aiSuggestion')}</p>
      <p class="cluster-detail-suggestion">{cluster.ai_name.suggestion}</p>
    </Card>
  {/if}
</div>

<style>
  .cluster-detail {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .cluster-detail-head {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    padding-block-end: 0.5rem;
    border-block-end: 1px solid var(--border);
  }
  .cluster-detail-title-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .cluster-detail-title {
    margin: 0;
    color: var(--text);
    font-size: 1rem;
    font-weight: 600;
    line-height: 1.3;
  }
  .cluster-detail-template,
  .cluster-detail-fp {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.75rem;
    display: flex;
    align-items: center;
    gap: 0.375rem;
    flex-wrap: wrap;
  }
  .cluster-detail-template code,
  .cluster-detail-fp code {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    color: var(--text);
    overflow-wrap: anywhere;
  }
  .cluster-detail-template-label {
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 600;
    color: var(--text-tertiary);
    font-size: 0.625rem;
  }
  .cluster-detail-stats {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-block-start: 0.25rem;
  }
  .cluster-detail-stat-meta {
    color: var(--text-muted);
    font-size: 0.75rem;
  }
  .cluster-detail-summary {
    margin: 0;
    color: var(--text);
    font-size: 0.875rem;
    line-height: 1.5;
    padding: 0.625rem 0.75rem;
    background: var(--surface-2);
    border-inline-start: 3px solid var(--accent);
    border-radius: 6px;
  }
  :global([dir='rtl']) .cluster-detail-summary {
    border-inline-start: none;
    border-inline-end: 3px solid var(--accent);
  }
  .cluster-detail-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
    margin-block-start: 0.375rem;
  }
  .cluster-detail-chip {
    display: inline-flex;
    align-items: center;
    padding-block: 0.125rem;
    padding-inline: 0.5rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 999px;
    color: var(--text);
    font-size: 0.6875rem;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    max-inline-size: 16rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .cluster-detail-empty-line {
    color: var(--text-tertiary);
    font-size: 0.8125rem;
  }
  .cluster-detail-rep-meta {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-block: 0.25rem 0.375rem;
    color: var(--text-muted);
    font-size: 0.75rem;
  }
  .cluster-detail-rep-id {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    color: var(--text-tertiary);
  }
  .cluster-detail-rep-bytes {
    color: var(--text-tertiary);
  }
  .cluster-detail-payload {
    margin: 0;
    padding: 0.5rem 0.75rem;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.6875rem;
    line-height: 1.45;
    color: var(--text);
    white-space: pre-wrap;
    word-break: break-word;
    max-block-size: 16rem;
    overflow: auto;
  }
  .cluster-detail-payload.is-binary {
    word-break: break-all;
    color: var(--text-muted);
  }
  .cluster-detail-binary-hint {
    margin-block: 0.25rem 0;
    color: var(--text-tertiary);
    font-size: 0.6875rem;
  }
  .cluster-detail-timeline {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem;
    margin-block-start: 0.375rem;
  }
  .cluster-detail-timeline-item {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding-block: 0.25rem;
    padding-inline: 0.5rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 999px;
    color: var(--text);
    font: inherit;
    cursor: pointer;
    transition: background 120ms ease, border-color 120ms ease;
  }
  .cluster-detail-timeline-item:hover:not(:disabled),
  .cluster-detail-timeline-item:focus-visible {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 8%, var(--surface-2));
    outline: none;
  }
  .cluster-detail-timeline-item.is-active {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 14%, var(--surface-2));
  }
  .cluster-detail-timeline-item:disabled {
    cursor: default;
    opacity: 0.6;
  }
  .cluster-detail-timeline-id {
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 0.6875rem;
    color: var(--text-tertiary);
  }
  .cluster-detail-timeline-when {
    color: var(--text-muted);
    font-size: 0.6875rem;
  }
  .cluster-detail-suggestion {
    margin: 0.25rem 0 0;
    color: var(--text);
    font-size: 0.8125rem;
    line-height: 1.5;
  }
  .section-heading {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.6875rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .mt-3 {
    margin-block-start: 0.75rem;
  }
</style>
