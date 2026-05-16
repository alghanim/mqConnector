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
    <linearGradient id="mqc-logo-gold" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0%" stop-color="#F8E08E" />
      <stop offset="50%" stop-color="#A59F8A" />
      <stop offset="100%" stop-color="#8F6A2A" />
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
  <!-- Maroon notch where queues meet -->
  <circle cx="14" cy="14" r="2.2" fill="#8B153D" />
</svg>
