<script lang="ts">
  /*
   * §5.2 — Card.
   *
   * Brand-correct surface bg + border + theme-aware shadow + 16 dp
   * radius, all token-driven (see app.css `.card`). Three props:
   *
   *   strip   — paint a 3 px gold brand-gradient strip across the top
   *             edge (§4.2 allowed use). Set this on a hero or section
   *             header card; never on every card in a list.
   *   hover   — turn on the spec-mandated hover/press state per §5.2
   *             (bg = --card-hover-bg, border = --primary). Adds a
   *             cursor:pointer affordance so callers don't have to.
   *   padding — `sm` (12 dp), `md` (20 dp — current default), `lg`
   *             (28 dp). 16 dp matches the spec verbatim but the
   *             default stays at md to avoid cramming the existing
   *             dense admin tables.
   */
  export let strip = false;
  export let hover = false;
  export let padding: 'sm' | 'md' | 'lg' = 'md';

  const padClass: Record<string, string> = {
    sm: 'p-3',
    md: 'p-5',
    lg: 'p-7'
  };
</script>

<div class="card" class:card-strip={strip} class:card-interactive={hover}>
  <div class={padClass[padding]}>
    <slot />
  </div>
</div>
