<!--
  PathPicker — pop-over for picking a JSONPath out of a list of
  extracted sample paths.

  Shared utility, used by every editor that lets the operator name a
  JSONPath (FilterEditor, the routing-rule rows inside RouteEditor's
  underlying list editor, the transform source/target paths inside
  TransformEditor — though the wrapped list editors keep their plain
  inputs for now and only the new FilterEditor wires PathPicker in. The
  utility ships standalone so Task 11 (DryRunDock) and follow-ups can
  reuse it without re-authoring.)

  Props
    paths   — pre-extracted JSONPath strings (POST /samples/extract). May
              be empty; the popover renders an "empty" hint in that case.
    value   — current selection. Used to highlight the existing pick.
    label   — button label override. Defaults to "From sample".

  Events
    pick   — CustomEvent<string>; fires once when the operator clicks a
             path. Closes the popover automatically.

  Notes:
    - Uses the shared <Dialog> component as the popover host so we get
      Escape-to-close + focus trap for free; it's a modal, not an
      anchored popover, which avoids positioning bugs in narrow panels.
    - All CSS uses brand tokens; no raw hex.
-->
<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { locale, t } from '$lib/stores/locale';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';

  export let paths: string[] = [];
  export let value = '';
  export let label = '';

  const dispatch = createEventDispatcher<{ pick: string }>();

  let open = false;
  let filter = '';

  $: visible = filter.trim()
    ? paths.filter((p) => p.toLowerCase().includes(filter.trim().toLowerCase()))
    : paths;

  function onOpen() {
    open = true;
    filter = '';
  }

  function onPick(p: string) {
    open = false;
    dispatch('pick', p);
  }
</script>

<Button variant="ghost" on:click={onOpen}>
  {label || t($locale, 'studio.pathPicker.button')}
</Button>

<Dialog
  open={open}
  title={t($locale, 'studio.pathPicker.title')}
  cancelLabel={t($locale, 'common.cancel')}
  confirmLabel={t($locale, 'common.cancel')}
  on:cancel={() => (open = false)}
  on:confirm={() => (open = false)}
>
  {#if paths.length === 0}
    <p class="pp-empty">{t($locale, 'studio.pathPicker.empty')}</p>
  {:else}
    <input
      type="text"
      class="pp-filter"
      bind:value={filter}
      placeholder={t($locale, 'studio.pathPicker.filter')}
      aria-label={t($locale, 'studio.pathPicker.filter')}
    />
    <ul class="pp-list" role="listbox">
      {#each visible as p (p)}
        <li>
          <button
            type="button"
            class="pp-item"
            class:pp-item-selected={p === value}
            on:click={() => onPick(p)}
          >
            {p}
          </button>
        </li>
      {/each}
      {#if visible.length === 0}
        <li class="pp-no-match">{t($locale, 'studio.pathPicker.empty')}</li>
      {/if}
    </ul>
  {/if}
</Dialog>

<style>
  .pp-empty {
    color: var(--text-muted);
    font-size: 13px;
    margin: 0;
  }
  .pp-filter {
    display: block;
    inline-size: 100%;
    margin-block-end: 8px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--bg);
    color: var(--text);
    font-size: 13px;
  }
  .pp-list {
    list-style: none;
    margin: 0;
    padding: 0;
    max-block-size: 260px;
    overflow-y: auto;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface);
  }
  .pp-list li + li {
    border-block-start: 1px solid var(--border);
  }
  .pp-item {
    inline-size: 100%;
    padding: 8px 12px;
    background: transparent;
    border: none;
    color: var(--text);
    font-family: 'SFMono-Regular', Menlo, Consolas, monospace;
    font-size: 12px;
    text-align: start;
    cursor: pointer;
  }
  .pp-item:hover,
  .pp-item:focus-visible {
    background: var(--surface-high);
    outline: none;
  }
  .pp-item-selected {
    color: var(--accent);
    font-weight: 600;
  }
  .pp-no-match {
    padding: 8px 12px;
    color: var(--text-muted);
    font-size: 12px;
  }
</style>
