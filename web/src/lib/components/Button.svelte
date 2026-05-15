<script lang="ts">
  // Brand-compliant button. Variant maps to CSS classes defined in app.css —
  // see Brand Guide §5.4 for spec.
  //
  // Event forwarding: a bare `on:click` on the inner <button> below tells
  // Svelte 4 to forward DOM click events from the component to whichever
  // parent attaches `<Button on:click={…}>`. The previous shape bound
  // on:click to an `onClick` prop, which silently swallowed every parent
  // handler — leaving every Save / Edit / Delete / Configure click dead.
  // Pinned by Button.test.ts.
  export let type: 'button' | 'submit' = 'button';
  export let variant: 'primary' | 'secondary' | 'outline' | 'ghost' | 'danger' = 'primary';
  export let disabled = false;
  export let loading = false;
  export let fullWidth = false;
</script>

<button
  {type}
  class="btn btn-{variant}"
  class:w-full={fullWidth}
  disabled={disabled || loading}
  aria-busy={loading}
  on:click
>
  {#if loading}
    <span class="opacity-60">…</span>
  {/if}
  <slot />
</button>
