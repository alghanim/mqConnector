<script lang="ts">
  // Status badge. Variant maps to CSS class from app.css.
  export let variant: 'success' | 'warning' | 'danger' | 'neutral' = 'neutral';
</script>

<span class="badge badge-{variant}">
  <slot />
</span>
