<!--
  AINameBadge — small pill that marks an AI-named cluster title.

  The cluster naming pipeline (internal/dlq/cluster/naming.go) returns a
  short title via either the LLM ("ai") or a deterministic templater
  ("deterministic"). We badge both so the operator knows the difference:

    * "AI"   — accent-tinted pill, links to the governance audit feed
               (when /governance/audit ships in Wave 5 the title attr
               also explains the provenance). Tooltip on hover.
    * "auto" — subdued neutral pill for the deterministic backstop;
               same shape so the eye doesn't bounce between cluster
               cards but the colour reads as "less novel".
-->
<script lang="ts">
  import { locale, t } from '$lib/stores/locale';

  /**
   * source — Wave 3 backend returns "ai" or "deterministic" on each
   * cluster when `?ai=names` is set. Anything else (including undef)
   * suppresses the badge entirely; the caller is expected to gate on
   * presence already, this is a defence-in-depth fallback.
   */
  export let source: 'ai' | 'deterministic' | undefined = undefined;

  // The Wave 5 governance/audit feed isn't shipped yet — for v1 we
  // resolve to a no-op anchor. When the route lands, swap the href
  // for the real `/governance/audit?source=ai` deep-link.
  $: href = source === 'ai' ? '/governance/audit?source=ai' : undefined;
  $: label =
    source === 'ai'
      ? t($locale, 'dlq.clusters.ai.badge')
      : t($locale, 'dlq.clusters.ai.badge.deterministic');
  $: tooltip =
    source === 'ai'
      ? t($locale, 'dlq.clusters.ai.title')
      : t($locale, 'dlq.clusters.ai.deterministic.title');
</script>

{#if source === 'ai' || source === 'deterministic'}
  {#if href}
    <a
      class="ai-badge ai-badge-ai"
      href={href}
      title={tooltip}
      data-source={source}
      aria-label={tooltip}
    >
      {label}
    </a>
  {:else}
    <span
      class="ai-badge ai-badge-deterministic"
      title={tooltip}
      data-source={source}
      aria-label={tooltip}
    >
      {label}
    </span>
  {/if}
{/if}

<style>
  .ai-badge {
    display: inline-flex;
    align-items: center;
    gap: 0.125rem;
    padding-block: 0.0625rem;
    padding-inline: 0.375rem;
    font-size: 0.625rem;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    border-radius: 4px;
    line-height: 1;
    border: 1px solid transparent;
    text-decoration: none;
  }
  .ai-badge-ai {
    background: var(--accent-container);
    color: var(--accent);
    border-color: color-mix(in srgb, var(--accent) 40%, transparent);
  }
  .ai-badge-ai:hover,
  .ai-badge-ai:focus-visible {
    background: color-mix(in srgb, var(--accent) 20%, var(--accent-container));
    outline: none;
  }
  .ai-badge-deterministic {
    background: var(--surface-high);
    color: var(--text-tertiary);
    border-color: var(--border);
  }
</style>
