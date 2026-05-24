<!--
  /pipelines/[id]/studio — the Pipeline Studio shell.

  Hydrates the central studio store from five backend endpoints
  (pipeline metadata + stages + transforms + routing rules + revisions
  list + latest deployed) then renders <Studio>. While the hydrate is
  in flight, a full-page Skeleton stands in; on failure an Alert
  surfaces the message with a Retry button.

  Wave 1 / Task 8 — chrome only. Tasks 9-12 fill the canvas / inspector
  / dock / version rail.
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { page } from '$app/stores';

  import { studio, studioState } from '$lib/stores/studio';
  import { locale, t } from '$lib/stores/locale';
  import Studio from '$lib/components/studio/Studio.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Button from '$lib/components/Button.svelte';

  $: pipelineId = $page.params.id ?? '';

  let mounted = false;

  // The studio store is a single global — by definition only one
  // Studio mounts at a time. On unmount we wipe it so a different
  // pipeline doesn't pick up stale state.
  onMount(async () => {
    mounted = true;
    if (pipelineId) await studio.hydrate(pipelineId);
  });
  onDestroy(() => {
    studio.reset();
  });

  async function retry() {
    if (pipelineId) await studio.hydrate(pipelineId);
  }

  // Derived state flags from the store. We pre-compute them so the
  // template branches read top-down without nested conditionals.
  $: state = $studioState.state;
  $: error = $studioState.error;
  $: hasData = $studioState.draft !== null;
  $: showLoading = mounted && !hasData && state !== 'error';
  $: showError = state === 'error' && !hasData;
</script>

{#if showLoading}
  <div class="studio-loading-shell" aria-busy="true" aria-label={t($locale, 'studio.loading')}>
    <Skeleton width="100%" height="56px" />
    <div class="studio-loading-grid">
      <Skeleton width="100%" height="320px" />
      <Skeleton width="100%" height="320px" />
      <Skeleton width="100%" height="320px" />
    </div>
    <Skeleton width="100%" height="80px" />
  </div>
{:else if showError}
  <div class="studio-error-shell">
    <Alert variant="error">
      <svelte:fragment slot="title">{t($locale, 'studio.error.title')}</svelte:fragment>
      <p>{error || 'unknown error'}</p>
      <div class="studio-error-actions">
        <Button on:click={retry}>{t($locale, 'studio.error.retry')}</Button>
      </div>
    </Alert>
  </div>
{:else if hasData}
  <Studio {pipelineId} />
{/if}

<style>
  .studio-loading-shell {
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .studio-loading-grid {
    display: grid;
    grid-template-columns: minmax(14rem, 18rem) 1fr minmax(16rem, 22rem);
    gap: 0.75rem;
  }
  @media (max-inline-size: 900px) {
    .studio-loading-grid {
      grid-template-columns: 1fr;
    }
  }

  .studio-error-shell {
    padding: 1rem;
  }
  .studio-error-actions {
    display: flex;
    justify-content: flex-end;
    margin-block-start: 0.75rem;
  }
</style>
