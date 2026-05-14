<script lang="ts">
  import { theme } from '$lib/stores/theme';

  function toggle() {
    theme.toggle();
  }
</script>

<button
  type="button"
  class="btn btn-ghost"
  aria-label="Toggle theme"
  on:click={toggle}
  title="Toggle theme"
>
  {#if $theme === 'dark'}
    <!-- sun -->
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <circle cx="12" cy="12" r="4"></circle>
      <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"></path>
    </svg>
  {:else}
    <!-- moon -->
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M21 12.79A9 9 0 1 1 11.21 3a7 7 0 0 0 9.79 9.79z"></path>
    </svg>
  {/if}
</button>
