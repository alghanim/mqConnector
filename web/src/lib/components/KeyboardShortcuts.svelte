<!--
  KeyboardShortcuts — overlay triggered by `?`.

  Lists every global shortcut the shell exposes so power users can
  discover them without leaving the keyboard. Rendered as a fixed
  modal with the same scrim/focus-trap conventions as Dialog.svelte.

  Categories:
    Navigation — palette, links inside the palette
    Workspace  — theme, locale
    Overlays   — help, escape

  RTL is handled by CSS logical properties; key glyphs (⌘, ↵, Esc)
  are intentionally Latin in both locales because they correspond to
  physical keys.
-->
<script lang="ts">
  import { createEventDispatcher, onDestroy } from 'svelte';
  import { locale, t } from '$lib/stores/locale';
  import { Keyboard } from 'lucide-svelte';

  export let open = false;

  const dispatch = createEventDispatcher<{ close: void }>();

  let dialogEl: HTMLDivElement | null = null;
  let lastFocused: Element | null = null;

  function onKey(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      dispatch('close');
    }
  }

  $: if (typeof document !== 'undefined') {
    if (open) {
      lastFocused = document.activeElement;
      document.documentElement.style.overflow = 'hidden';
      queueMicrotask(() =>
        dialogEl?.querySelector<HTMLElement>('button, a[href], [tabindex]:not([tabindex="-1"])')?.focus()
      );
    } else {
      document.documentElement.style.overflow = '';
      if (lastFocused instanceof HTMLElement) lastFocused.focus({ preventScroll: true });
      lastFocused = null;
    }
  }

  onDestroy(() => {
    if (typeof document !== 'undefined') document.documentElement.style.overflow = '';
  });

  type Row = { keys: string[]; label: string };
  $: groups = [
    {
      title: t($locale, 'shortcuts.group.navigation'),
      rows: [
        { keys: ['⌘', 'K'], label: t($locale, 'shortcuts.openPalette') },
        { keys: ['/'], label: t($locale, 'shortcuts.openPalette') },
        { keys: ['↵'], label: t($locale, 'shortcuts.paletteEnter') },
        { keys: ['↑', '↓'], label: t($locale, 'shortcuts.paletteMove') }
      ] as Row[]
    },
    {
      title: t($locale, 'shortcuts.group.workspace'),
      rows: [
        { keys: ['T'], label: t($locale, 'shortcuts.toggleTheme') },
        { keys: ['L'], label: t($locale, 'shortcuts.toggleLocale') }
      ] as Row[]
    },
    {
      title: t($locale, 'shortcuts.group.overlays'),
      rows: [
        { keys: ['?'], label: t($locale, 'shortcuts.help') },
        { keys: ['Esc'], label: t($locale, 'shortcuts.escape') }
      ] as Row[]
    }
  ];
</script>

<svelte:window on:keydown={onKey} />

{#if open}
  <div
    class="scrim"
    role="presentation"
    on:click={() => dispatch('close')}
  >
    <!-- svelte-ignore a11y-click-events-have-key-events -->
    <!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
    <div
      bind:this={dialogEl}
      class="panel"
      role="dialog"
      aria-modal="true"
      aria-labelledby="ks-title"
      on:click|stopPropagation
    >
      <header class="head">
        <span class="head-icon" aria-hidden="true"><Keyboard size={16} strokeWidth={1.75} /></span>
        <h2 id="ks-title" class="head-title">{t($locale, 'shortcuts.title')}</h2>
        <button type="button" class="close-btn" on:click={() => dispatch('close')} aria-label="Close">
          ✕
        </button>
      </header>

      <div class="body">
        {#each groups as g (g.title)}
          <section class="group">
            <p class="group-title">{g.title}</p>
            <ul class="rows">
              {#each g.rows as r (r.label + r.keys.join('+'))}
                <li class="row">
                  <span class="row-label">{r.label}</span>
                  <span class="row-keys">
                    {#each r.keys as k, i (i)}
                      <kbd class="kbd">{k}</kbd>
                      {#if i < r.keys.length - 1}
                        <span class="kbd-sep" aria-hidden="true">+</span>
                      {/if}
                    {/each}
                  </span>
                </li>
              {/each}
            </ul>
          </section>
        {/each}
      </div>

      <footer class="foot">
        <span class="foot-hint">{t($locale, 'shortcuts.hint')}</span>
        <button type="button" class="close-link" on:click={() => dispatch('close')}>
          {t($locale, 'shortcuts.close')}
        </button>
      </footer>
    </div>
  </div>
{/if}

<style>
  .scrim {
    position: fixed;
    inset: 0;
    background: var(--dialog-scrim);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 16px;
    z-index: 1000;
  }
  .panel {
    background: var(--dialog-bg);
    color: var(--text);
    border-radius: 16px;
    inline-size: 100%;
    max-inline-size: 560px;
    max-block-size: calc(100vh - 32px);
    box-shadow: var(--dialog-shadow); /* §5.14 elevation via brand token */
    border: 1px solid var(--card-border);
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 14px 16px;
    border-block-end: 1px solid var(--divider);
  }
  .head-icon {
    color: var(--secondary);
    display: inline-flex;
  }
  :global([data-theme='light']) .head-icon {
    color: var(--primary);
  }
  .head-title {
    flex: 1;
    margin: 0;
    font-size: 14px;
    font-weight: 600;
    color: var(--text);
  }
  .close-btn {
    appearance: none;
    border: none;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    font-size: 14px;
    padding: 4px 8px;
    border-radius: 6px;
  }
  .close-btn:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .body {
    padding: 16px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 18px;
  }
  .group-title {
    color: var(--text-tertiary);
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    margin-block-end: 8px;
  }
  .rows {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 6px 8px;
    border-radius: 8px;
  }
  .row:hover {
    background: var(--surface-hover);
  }
  .row-label {
    color: var(--text);
    font-size: 13px;
  }
  .row-keys {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .kbd {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-inline-size: 22px;
    block-size: 22px;
    padding: 0 6px;
    border: 1px solid var(--border-strong);
    border-block-end-width: 2px;
    border-radius: 5px;
    background: var(--surface);
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 11px;
    line-height: 1;
  }
  .kbd-sep {
    color: var(--text-tertiary);
    font-size: 10px;
  }
  .foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 16px;
    border-block-start: 1px solid var(--divider);
    background: var(--surface);
  }
  .foot-hint {
    color: var(--text-tertiary);
    font-size: 11px;
  }
  .close-link {
    appearance: none;
    border: none;
    background: transparent;
    color: var(--secondary);
    cursor: pointer;
    font-size: 12px;
    font-weight: 500;
  }
  :global([data-theme='light']) .close-link {
    color: var(--primary);
  }
  .close-link:hover {
    text-decoration: underline;
  }
</style>
