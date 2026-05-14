<script lang="ts">
  // Single-line text input. Label and error are optional. Touch target is
  // guaranteed by the .input class (min-h-touch = 48px).
  export let value = '';
  export let type: 'text' | 'password' | 'email' | 'url' | 'number' = 'text';
  export let label = '';
  export let placeholder = '';
  export let id = `inp-${Math.random().toString(36).slice(2, 9)}`;
  export let required = false;
  export let disabled = false;
  export let autocomplete: string | undefined = undefined;
  export let error = '';
</script>

<div class="w-full">
  {#if label}
    <label class="label" for={id}>{label}{#if required} <span aria-hidden="true" style="color:var(--accent)">*</span>{/if}</label>
  {/if}
  <!--
    Svelte rejects a dynamic `type` attribute combined with two-way binding,
    so we hard-branch on the (small) set of supported types. Pattern from the
    Svelte FAQ: each branch has a literal `type=` to keep `bind:value` valid.
  -->
  {#if type === 'password'}
    <input {id} type="password" {placeholder} {required} {disabled} {autocomplete}
      bind:value class="input" class:border-danger={!!error} aria-invalid={!!error} />
  {:else if type === 'email'}
    <input {id} type="email" {placeholder} {required} {disabled} {autocomplete}
      bind:value class="input" class:border-danger={!!error} aria-invalid={!!error} />
  {:else if type === 'url'}
    <input {id} type="url" {placeholder} {required} {disabled} {autocomplete}
      bind:value class="input" class:border-danger={!!error} aria-invalid={!!error} />
  {:else if type === 'number'}
    <input {id} type="number" {placeholder} {required} {disabled} {autocomplete}
      bind:value class="input" class:border-danger={!!error} aria-invalid={!!error} />
  {:else}
    <input {id} type="text" {placeholder} {required} {disabled} {autocomplete}
      bind:value class="input" class:border-danger={!!error} aria-invalid={!!error} />
  {/if}
  {#if error}
    <p class="mt-1 text-xs" style="color: var(--danger)">{error}</p>
  {/if}
</div>
