<script lang="ts">
  import { locale } from '$lib/stores/locale';

  function toggle() {
    locale.set($locale === 'en' ? 'ar' : 'en');
  }
</script>

<button
  type="button"
  class="btn btn-ghost"
  aria-label="Toggle language"
  on:click={toggle}
  title="Toggle language"
>
  {#if $locale === 'en'}
    العربية
  {:else}
    English
  {/if}
</button>
