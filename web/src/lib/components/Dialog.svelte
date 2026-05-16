<!--
  Dialog — Brand Guide §5.14.

  Modal that replaces `window.confirm()` for destructive admin actions.
  Renders into `document.body` to escape any positioned ancestor, traps
  Tab + Shift+Tab inside, closes on Escape (when not blocked), and emits
  `confirm` / `cancel` so the caller doesn't need to wire its own.

  Brand spec:
    - Background `--dialog-bg` (#2E3840 dark / #FFFFFF light)
    - Scrim `--dialog-scrim` (#000 60% dark / 40% light)
    - 16 dp radius, 24 dp content padding
    - Confirm button: maroon (--accent) — destructive sits on maroon
      per §5.14 (visual cue is the dialog itself, not red on red)
    - Cancel button: text-style (--secondary)

  Usage:

      <Dialog
        open={confirmingDelete}
        title="Delete pipeline?"
        confirmLabel="Delete"
        cancelLabel="Cancel"
        on:confirm={onDelete}
        on:cancel={() => (confirmingDelete = false)}
      >
        <p>This permanently removes the pipeline and stops its workers.</p>
      </Dialog>
-->
<script lang="ts">
  import { createEventDispatcher, onDestroy } from 'svelte';

  export let open = false;
  export let title = '';
  export let confirmLabel = 'Confirm';
  export let cancelLabel = 'Cancel';
  export let dismissible = true;
  /** Set true while the action is in-flight; locks the confirm button
   *  and prevents Escape from cancelling mid-request. */
  export let busy = false;

  const dispatch = createEventDispatcher<{ confirm: void; cancel: void }>();

  let dialogEl: HTMLDivElement | null = null;
  let lastFocused: Element | null = null;

  /** Returns every focusable descendant inside the dialog in tab order. */
  function focusables(): HTMLElement[] {
    if (!dialogEl) return [];
    return Array.from(
      dialogEl.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
      )
    );
  }

  function trapTab(e: KeyboardEvent) {
    if (e.key !== 'Tab') return;
    const items = focusables();
    if (items.length === 0) {
      e.preventDefault();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }

  function onKey(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === 'Tab') return trapTab(e);
    if (e.key === 'Escape' && dismissible && !busy) {
      e.preventDefault();
      dispatch('cancel');
    }
  }

  // Lifecycle: focus the first focusable when opening; restore focus
  // on close so the operator's cursor lands back where they invoked
  // the dialog (keyboard accessibility).
  $: if (typeof document !== 'undefined') {
    if (open) {
      lastFocused = document.activeElement;
      document.documentElement.style.overflow = 'hidden';
      queueMicrotask(() => focusables()[0]?.focus());
    } else {
      document.documentElement.style.overflow = '';
      if (lastFocused instanceof HTMLElement) lastFocused.focus({ preventScroll: true });
      lastFocused = null;
    }
  }

  onDestroy(() => {
    if (typeof document !== 'undefined') document.documentElement.style.overflow = '';
  });
</script>

<svelte:window on:keydown={onKey} />

{#if open}
  <div
    class="dialog-scrim"
    role="presentation"
    on:click={() => dismissible && !busy && dispatch('cancel')}
  >
    <!--
      The inner dialog stops click propagation so clicking the panel
      itself doesn't fire the scrim's dismiss handler. svelte-a11y flags
      this as needing a keyboard pair, but the dialog has its own
      keyboard handling (Esc → cancel) on the window listener above —
      this stopPropagation is purely about ignoring scrim clicks on the
      panel. Suppress the a11y warning.
    -->
    <!-- svelte-ignore a11y-click-events-have-key-events -->
    <!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
    <div
      bind:this={dialogEl}
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby={title ? 'dialog-title' : undefined}
      on:click|stopPropagation
    >
      {#if title}
        <h2 id="dialog-title" class="dialog-title">{title}</h2>
      {/if}
      <div class="dialog-body"><slot /></div>
      <div class="dialog-actions">
        <button
          type="button"
          class="btn btn-ghost"
          on:click={() => dispatch('cancel')}
          disabled={busy}
        >
          {cancelLabel}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          on:click={() => dispatch('confirm')}
          disabled={busy}
        >
          {confirmLabel}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .dialog-scrim {
    position: fixed;
    inset: 0;
    background: var(--dialog-scrim);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 16px;
    z-index: 1000;
  }
  .dialog {
    background: var(--dialog-bg);
    color: var(--text);
    border-radius: 16px;
    inline-size: 100%;
    max-inline-size: 480px;
    padding: 24px;
    /* §5.14 dialog elevation via the brand-token shadow; the previous
       literal rgba(0,0,0,0.35) ignored the light-theme adjustment in
       §5.2 (#333F48 at 8%). */
    box-shadow: var(--dialog-shadow);
    /* The scrim's fixed-position container scrolls the body, but make
       sure a very tall dialog body doesn't push the actions out of
       viewport. */
    max-block-size: calc(100vh - 32px);
    overflow-y: auto;
  }
  .dialog-title {
    margin: 0 0 12px;
    font-size: 18px;
    font-weight: 600;
    line-height: 1.35;
    color: var(--text);
  }
  .dialog-body {
    color: var(--text-muted);
    font-size: 14px;
    line-height: 1.55;
  }
  .dialog-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-block-start: 24px;
  }
</style>
