<script lang="ts">
  // Brand-compliant button. Variant maps to CSS classes defined in app.css —
  // see Brand Guide §5.4 for spec.
  export let type: 'button' | 'submit' = 'button';
  export let variant: 'primary' | 'secondary' | 'outline' | 'ghost' | 'danger' = 'primary';
  export let disabled = false;
  export let loading = false;
  export let fullWidth = false;
  export let onClick: (() => void) | undefined = undefined;
</script>

<button
  {type}
  class="btn btn-{variant}"
  class:w-full={fullWidth}
  disabled={disabled || loading}
  aria-busy={loading}
  on:click={onClick}
>
  {#if loading}
    <span class="opacity-60">…</span>
  {/if}
  <slot />
</button>
