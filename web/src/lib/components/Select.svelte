<script lang="ts">
  export let value: string;
  export let label = '';
  export let id = `sel-${Math.random().toString(36).slice(2, 9)}`;
  export let options: { value: string; label: string }[] = [];
  export let disabled = false;
</script>

<div class="w-full">
  {#if label}
    <label class="label" for={id}>{label}</label>
  {/if}
  <select {id} {disabled} bind:value class="input">
    {#each options as o (o.value)}
      <option value={o.value}>{o.label}</option>
    {/each}
  </select>
</div>
