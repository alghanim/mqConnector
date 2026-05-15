<!--
  Switch — Brand Guide §5.16.

  Two-way bound on/off toggle replacing native `<input type="checkbox">`
  where the operator is choosing an enabled/disabled state, not "tick to
  accept" semantics.

  Track-on / thumb-on / track-off / thumb-off colours come straight from
  the `--switch-*` tokens (light-gold thumb on dark theme, white thumb on
  light theme — both per spec table). The thumb slides via a CSS
  transform; the whole control still respects `prefers-reduced-motion`
  through the global override in app.css.

  Props
    checked    — two-way bound boolean
    disabled   — disabled state, follows spec disabled-text colour
    label      — visible text adjacent to the control. If a non-empty
                 label is provided, the entire `<label>` becomes the
                 click target so anywhere on the label/track toggles it.
    name       — for forms
-->
<script lang="ts">
  export let checked = false;
  export let disabled = false;
  export let label = '';
  export let name = '';

  /** Stable id so the label[for] reference is unambiguous. */
  let id = `sw-${Math.random().toString(36).slice(2, 9)}`;
</script>

<label class="switch-row" class:is-disabled={disabled}>
  <span class="switch" class:on={checked} class:off={!checked}>
    <input
      type="checkbox"
      {id}
      {name}
      bind:checked
      {disabled}
      role="switch"
      aria-checked={checked}
      aria-label={label || undefined}
    />
    <span class="switch-thumb" aria-hidden="true"></span>
  </span>
  {#if label}
    <span class="switch-label">{label}</span>
  {/if}
</label>

<style>
  .switch-row {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    cursor: pointer;
    -webkit-tap-highlight-color: transparent;
  }
  .switch-row.is-disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }
  .switch-label {
    color: var(--text);
    font-size: 14px;
    line-height: 1.4;
    user-select: none;
  }

  .switch {
    position: relative;
    inline-size: 44px;
    block-size: 24px;
    border-radius: 999px;
    background: var(--switch-track-off);
    transition: background-color 200ms;
    flex-shrink: 0;
  }
  .switch.on { background: var(--switch-track-on); }

  /* Hide the actual checkbox but keep it in the keyboard tab order +
     screen-reader tree. Click on .switch-row toggles via the label
     binding; arrow keys / space work via the native checkbox. */
  .switch :global(input) {
    position: absolute;
    inset: 0;
    inline-size: 100%;
    block-size: 100%;
    margin: 0;
    opacity: 0;
    cursor: inherit;
  }

  .switch-thumb {
    position: absolute;
    inset-block-start: 3px;
    inset-inline-start: 3px;
    inline-size: 18px;
    block-size: 18px;
    border-radius: 50%;
    background: var(--switch-thumb-off);
    transition: transform 200ms, background-color 200ms;
    pointer-events: none;
  }
  .switch.on .switch-thumb {
    background: var(--switch-thumb-on);
    /* track width 44 − thumb 18 − 2× 3px inset = 20px slide */
    transform: translateX(20px);
  }
  /* RTL: slide in the opposite physical direction. */
  :global([dir='rtl']) .switch.on .switch-thumb {
    transform: translateX(-20px);
  }

  /* Brand-gold 2 px ring around the thumb when the (hidden) checkbox
     is keyboard-focused. Use :focus-within on the wrapper instead of a
     :global(input:focus-visible) sibling rule — Svelte rejects :global
     sandwiched between scoped selectors. */
  .switch:focus-within .switch-thumb {
    box-shadow: 0 0 0 2px var(--qb-dark-gold);
  }
</style>
