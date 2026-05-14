<script lang="ts">
  // Card primitive. `strip` adds a decorative brand-gradient strip at the top
  // (Brand Guide §4: gradient on thin strips only — never large surfaces).
  export let strip = false;
  export let padding: 'sm' | 'md' | 'lg' = 'md';

  const padClass: Record<string, string> = {
    sm: 'p-3',
    md: 'p-5',
    lg: 'p-7'
  };
</script>

<div class="card" class:card-strip={strip}>
  <div class={padClass[padding]}>
    <slot />
  </div>
</div>
