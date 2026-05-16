<script lang="ts">
  // Single-line text input. Wired against brand-tokens.css §5.11.
  //   - 48 dp touch target (.input → min-h-touch)
  //   - Theme-correct bg / border / text / placeholder
  //   - Dark Gold focus border
  //   - error state: red border + bottom-mounted error message
  //   - helper: dim caption text for hints, hidden when an error fires
  //
  // ARIA: when `error` is set, the input gains aria-invalid="true" and
  // aria-describedby the error node. When only `helper` is set, the
  // helper node is described instead — screen readers always read the
  // most-specific available message.
  export let value: string | number = '';
  export let type: 'text' | 'password' | 'email' | 'url' | 'number' = 'text';
  export let label = '';
  export let placeholder = '';
  export let id = `inp-${Math.random().toString(36).slice(2, 9)}`;
  export let required = false;
  export let disabled = false;
  // Svelte 5 narrows the input element's autocomplete attribute to the
  // WHATWG `FullAutoFill` union (HTMLInputAutoCompleteAttribute) rather
  // than accepting any string. Type the prop to match so callers get
  // autocomplete on the autocomplete prop, instead of pushing the
  // mismatch into runtime markup.
  export let autocomplete: import('svelte/elements').FullAutoFill | undefined = undefined;
  /** Persistent hint shown below the input. Hidden when `error` is set. */
  export let helper = '';
  /** Validation message. Switches the border to --danger and overrides helper. */
  export let error = '';

  $: helperId = helper ? `${id}-help` : undefined;
  $: errorId = error ? `${id}-err` : undefined;
  $: describedBy = errorId ?? helperId;
</script>

<div class="w-full">
  {#if label}
    <label class="label" for={id}>
      {label}{#if required}<span aria-hidden="true" class="req-star">*</span>{/if}
    </label>
  {/if}
  <!--
    Svelte rejects a dynamic `type` attribute combined with two-way binding,
    so we hard-branch on the (small) set of supported types. Pattern from the
    Svelte FAQ: each branch has a literal `type=` to keep `bind:value` valid.
  -->
  {#if type === 'password'}
    <input {id} type="password" {placeholder} {required} {disabled} {autocomplete}
      bind:value class="input" class:input-invalid={!!error}
      aria-invalid={!!error} aria-describedby={describedBy} />
  {:else if type === 'email'}
    <input {id} type="email" {placeholder} {required} {disabled} {autocomplete}
      bind:value class="input" class:input-invalid={!!error}
      aria-invalid={!!error} aria-describedby={describedBy} />
  {:else if type === 'url'}
    <input {id} type="url" {placeholder} {required} {disabled} {autocomplete}
      bind:value class="input" class:input-invalid={!!error}
      aria-invalid={!!error} aria-describedby={describedBy} />
  {:else if type === 'number'}
    <input {id} type="number" {placeholder} {required} {disabled} {autocomplete}
      bind:value class="input" class:input-invalid={!!error}
      aria-invalid={!!error} aria-describedby={describedBy} />
  {:else}
    <input {id} type="text" {placeholder} {required} {disabled} {autocomplete}
      bind:value class="input" class:input-invalid={!!error}
      aria-invalid={!!error} aria-describedby={describedBy} />
  {/if}

  {#if error}
    <p id={errorId} class="input-error">{error}</p>
  {:else if helper}
    <p id={helperId} class="input-helper">{helper}</p>
  {/if}
</div>

<style>
  .req-star {
    color: var(--accent);
    margin-inline-start: 2px;
  }
</style>
