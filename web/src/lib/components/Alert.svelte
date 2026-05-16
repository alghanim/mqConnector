<!--
  Alert — Design system §5.10.

  Brand-compliant info / success / warning / error banner. 12 dp radius,
  16 dp padding, leading icon (auto-flipped under RTL via logical CSS),
  optional dismiss button, optional title above body. Replaces ad-hoc
  `color: var(--danger)` paragraphs scattered through the editor pages.

  Slots
    default — body content
    title   — optional bold title above the body

  Props
    variant     — info | success | warning | error  (default: info)
    dismissible — show an × close button; emits `dismiss` event
    role        — overrides the implicit ARIA role (status / alert)
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';

  export let variant: 'info' | 'success' | 'warning' | 'error' = 'info';
  export let dismissible = false;
  /**
   * `status` (default for info/success/warning) is announced politely;
   * `alert` is for errors that interrupt. Override via prop if needed.
   */
  export let role: 'status' | 'alert' | undefined = undefined;

  $: resolvedRole = role ?? (variant === 'error' ? 'alert' : 'status');
  // Narrow to the DOM enum so the spread onto <div aria-live={…}> typechecks.
  $: live = (variant === 'error' ? 'assertive' : 'polite') as 'assertive' | 'polite';

  const dispatch = createEventDispatcher<{ dismiss: void }>();
</script>

<div class="alert alert-{variant}" role={resolvedRole} aria-live={live}>
  <span class="alert-icon" aria-hidden="true">
    {#if variant === 'success'}
      <!-- Heroicons check-circle (outline) -->
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
        stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="10" />
        <path d="M9 12l2 2 4-4" />
      </svg>
    {:else if variant === 'warning'}
      <!-- exclamation-triangle (outline) -->
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
        stroke-linecap="round" stroke-linejoin="round">
        <path d="M12 9v4" />
        <path d="M12 17h.01" />
        <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
      </svg>
    {:else if variant === 'error'}
      <!-- x-circle (outline) -->
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
        stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="10" />
        <path d="M15 9l-6 6" />
        <path d="M9 9l6 6" />
      </svg>
    {:else}
      <!-- information-circle (outline) -->
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
        stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="10" />
        <path d="M12 16v-4" />
        <path d="M12 8h.01" />
      </svg>
    {/if}
  </span>
  <div class="alert-body">
    {#if $$slots.title}
      <p class="alert-title"><slot name="title" /></p>
    {/if}
    <div class="alert-content"><slot /></div>
  </div>
  {#if dismissible}
    <button
      type="button"
      class="alert-dismiss"
      aria-label="Dismiss"
      on:click={() => dispatch('dismiss')}
    >
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
        stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <path d="M18 6L6 18" />
        <path d="M6 6l12 12" />
      </svg>
    </button>
  {/if}
</div>

<style>
  /*
   * §5.10 — 12dp radius, 16dp padding, icon leading (RTL-aware via
   * flex + `gap`; logical layout means no manual mirroring needed).
   */
  .alert {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 16px;
    border-radius: 12px;
    border: 1px solid transparent;
    line-height: 1.55;
  }
  .alert-icon {
    flex-shrink: 0;
    inline-size: 20px;
    block-size: 20px;
    margin-block-start: 2px;
  }
  .alert-icon :global(svg) {
    inline-size: 100%;
    block-size: 100%;
  }
  .alert-body { flex: 1; min-inline-size: 0; }
  .alert-title { font-weight: 600; margin-bottom: 4px; }
  .alert-content :global(p) { margin: 0; }
  .alert-dismiss {
    flex-shrink: 0;
    inline-size: 24px;
    block-size: 24px;
    border-radius: 999px;
    background: transparent;
    color: currentColor;
    border: none;
    cursor: pointer;
    opacity: 0.7;
    transition: opacity 200ms, background-color 200ms;
  }
  .alert-dismiss:hover { opacity: 1; background: color-mix(in srgb, currentColor 12%, transparent); }
  .alert-dismiss :global(svg) { inline-size: 16px; block-size: 16px; }

  .alert-info {
    background: var(--info-bg);
    color: var(--info);
    border-color: color-mix(in srgb, var(--info) 30%, transparent);
  }
  .alert-success {
    background: var(--success-bg);
    color: var(--success);
    border-color: color-mix(in srgb, var(--success-solid) 30%, transparent);
  }
  .alert-warning {
    background: var(--warning-bg);
    color: var(--warning);
    border-color: color-mix(in srgb, var(--warning) 30%, transparent);
  }
  .alert-error {
    background: var(--danger-bg);
    color: var(--danger);
    border-color: color-mix(in srgb, var(--danger) 30%, transparent);
  }
</style>
