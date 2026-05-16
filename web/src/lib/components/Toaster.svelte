<!--
  Toaster — single instance mounted in the layout. Renders the toast
  stack in the inline-end corner. Respects prefers-reduced-motion.
-->
<script lang="ts">
  import { toasts } from '$lib/stores/toasts';
  import { fly } from 'svelte/transition';
  import { CheckCircle2, AlertTriangle, XOctagon, Info, X } from 'lucide-svelte';
</script>

<div class="toaster" role="region" aria-live="polite" aria-label="notifications">
  {#each $toasts as t (t.id)}
    <div class="toast" data-tone={t.tone} transition:fly={{ y: -8, duration: 180 }}>
      <span class="toast-icon" aria-hidden="true">
        {#if t.tone === 'success'}
          <CheckCircle2 size={18} />
        {:else if t.tone === 'warning'}
          <AlertTriangle size={18} />
        {:else if t.tone === 'error'}
          <XOctagon size={18} />
        {:else}
          <Info size={18} />
        {/if}
      </span>
      <div class="toast-body">
        <p class="toast-title">{t.title}</p>
        {#if t.body}
          <p class="toast-sub">{t.body}</p>
        {/if}
      </div>
      <button class="toast-dismiss" aria-label="dismiss" on:click={() => toasts.dismiss(t.id)}>
        <X size={14} />
      </button>
    </div>
  {/each}
</div>

<style>
  .toaster {
    position: fixed;
    top: 1.25rem;
    inset-inline-end: 1.25rem;
    z-index: 80;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    max-width: min(380px, calc(100vw - 2rem));
    pointer-events: none;
  }
  .toast {
    pointer-events: auto;
    display: grid;
    grid-template-columns: auto 1fr auto;
    align-items: start;
    gap: 0.625rem;
    padding: 0.625rem 0.75rem;
    border-radius: 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    box-shadow:
      0 12px 30px rgba(0, 0, 0, 0.25),
      0 2px 6px rgba(0, 0, 0, 0.12);
    color: var(--text);
  }
  .toast[data-tone='success'] {
    border-inline-start: 3px solid var(--success);
  }
  .toast[data-tone='success'] .toast-icon {
    color: var(--success);
  }
  .toast[data-tone='warning'] {
    border-inline-start: 3px solid var(--warning);
  }
  .toast[data-tone='warning'] .toast-icon {
    color: var(--warning);
  }
  .toast[data-tone='error'] {
    border-inline-start: 3px solid var(--danger);
  }
  .toast[data-tone='error'] .toast-icon {
    color: var(--danger);
  }
  .toast[data-tone='info'] {
    border-inline-start: 3px solid var(--text-tertiary);
  }
  .toast[data-tone='info'] .toast-icon {
    color: var(--text-muted);
  }
  .toast-icon {
    display: inline-flex;
    align-items: center;
    margin-top: 1px;
  }
  .toast-body {
    min-width: 0;
  }
  .toast-title {
    margin: 0;
    font-size: 0.875rem;
    font-weight: 600;
  }
  .toast-sub {
    margin: 2px 0 0;
    color: var(--text-muted);
    font-size: 0.75rem;
    line-height: 1.4;
  }
  .toast-dismiss {
    background: transparent;
    border: 0;
    color: var(--text-muted);
    padding: 2px;
    cursor: pointer;
    border-radius: 4px;
  }
  .toast-dismiss:hover {
    color: var(--text);
    background: var(--surface-2);
  }
</style>
