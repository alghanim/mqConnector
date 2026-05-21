<!--
  Brand mark. Two flowing lines with a maroon notch — reads as "two
  queues joined." Scales fluidly with the size prop.
-->
<script lang="ts">
  export let size = 28;
</script>

<svg
  xmlns="http://www.w3.org/2000/svg"
  viewBox="0 0 28 28"
  width={size}
  height={size}
  aria-hidden="true"
  role="img"
>
  <defs>
    <!--
      Gold gradient matches §4 of the brand-guide — light gold → olive
      gold → dark gold. The maroon notch below is allowed by §1.1 (the
      brand mark itself is the canonical home of the maroon accent).
    -->
    <linearGradient id="mqc-logo-gold" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0%"   style="stop-color: var(--palette-light-gold)" />
      <stop offset="50%"  style="stop-color: var(--palette-olive-gold)" />
      <stop offset="100%" style="stop-color: var(--palette-dark-gold)" />
    </linearGradient>
  </defs>
  <!-- Outer ring -->
  <circle cx="14" cy="14" r="12" fill="none" stroke="url(#mqc-logo-gold)" stroke-width="2.2" />
  <!-- Two flowing lines (source and destination queues) -->
  <path
    d="M5.5 10.5h7.5a3 3 0 0 1 3 3v0a3 3 0 0 0 3 3h3.5"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    opacity="0.85"
  />
  <!-- Maroon notch where queues meet (brand-mark accent, §1.1). -->
  <circle cx="14" cy="14" r="2.2" fill="var(--palette-maroon)" />
</svg>
