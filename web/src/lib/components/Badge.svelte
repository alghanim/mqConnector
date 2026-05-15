<script lang="ts">
  /*
   * §5.5 — Badge / chip.
   *
   * Status (success/danger) carry an automatic dot indicator per the
   * spec — the dot is rendered by the .badge-* CSS classes' ::before,
   * so we don't need extra DOM here. Warning and neutral are text-only
   * by spec.
   *
   * For a notification count, use Badge with variant="count" — it
   * renders the only fully-pill chip in the system (maroon, 20 dp min).
   */
  export let variant:
    | 'success'
    | 'warning'
    | 'danger'
    | 'neutral'
    | 'info'
    | 'count' = 'neutral';
</script>

{#if variant === 'count'}
  <span class="badge-count">
    <slot />
  </span>
{:else}
  <span class="badge badge-{variant}">
    <slot />
  </span>
{/if}
