<!--
  ConnectionTypeIcon — small, theme-aware inline SVG glyph that hints at
  the broker family for a Connection. Centralised here so the Studio
  canvas, header, and inspector all show the same icon for the same
  broker type, without each surface re-rolling its own SVG.

  Why hand-rolled SVG (not lucide-svelte): there's no single lucide glyph
  that maps cleanly to "Kafka", "RabbitMQ", "IBM MQ", etc. We need
  brand-shaped marks. Each glyph below is a small geometric abstraction
  (no third-party logos copied), authored to the same 16×16 viewBox so
  swapping them feels visually consistent.

  Stroke + fill both use `currentColor` so the parent decides the tint
  via `color: var(--token)`. Two consequences worth knowing:
    1. The icon WILL match the surrounding text colour by default.
    2. Theme switching is automatic — no per-theme overrides needed.

  Sizes default to 14px to fit in inline label rows; bump via `size`.
-->
<script lang="ts">
  import type { ConnectionType } from '$lib/api';

  export let type: ConnectionType | string | undefined = undefined;
  export let size = 14;
  // Optional aria-label. When omitted, the icon is decorative; when
  // present, it's announced as the broker type.
  export let label = '';

  // Pre-compute the aria attributes once so the template stays clean.
  // SVG's TypeScript types want `aria-hidden` as Booleanish ('true' |
  // 'false' | boolean), not a generic string — splitting the two
  // conditional paths keeps the inferred type narrow.
  $: hidden = label ? undefined : (true as const);
  $: role = label ? ('img' as const) : undefined;
</script>

{#if type === 'kafka'}
  <!-- Kafka: stylised radial spokes (commit-log + replicas idea). -->
  <svg
    viewBox="0 0 16 16"
    width={size}
    height={size}
    fill="none"
    stroke="currentColor"
    stroke-width="1.4"
    stroke-linecap="round"
    aria-label={label || undefined}
    aria-hidden={hidden}
    {role}
  >
    <circle cx="8" cy="8" r="1.6" />
    <circle cx="3" cy="4" r="1.2" />
    <circle cx="3" cy="12" r="1.2" />
    <circle cx="13" cy="8" r="1.2" />
    <path d="M4.1 4.7L6.6 7M4.1 11.3L6.6 9M9.5 8H11.8" />
  </svg>
{:else if type === 'rabbitmq'}
  <!-- RabbitMQ: queue + bunny ears (long verticals + arched top). -->
  <svg
    viewBox="0 0 16 16"
    width={size}
    height={size}
    fill="none"
    stroke="currentColor"
    stroke-width="1.4"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-label={label || undefined}
    aria-hidden={hidden}
    {role}
  >
    <path d="M3 2v6h2V2M7 2v6h2V2" />
    <path d="M3 8h10v5a1 1 0 01-1 1H4a1 1 0 01-1-1V8z" />
    <circle cx="11" cy="11" r="0.9" fill="currentColor" />
  </svg>
{:else if type === 'ibm'}
  <!-- IBM MQ: layered enterprise blocks (mainframe-ish). -->
  <svg
    viewBox="0 0 16 16"
    width={size}
    height={size}
    fill="none"
    stroke="currentColor"
    stroke-width="1.4"
    stroke-linecap="round"
    aria-label={label || undefined}
    aria-hidden={hidden}
    {role}
  >
    <path d="M2 4h12M2 7h12M2 10h12M2 13h12" />
    <path d="M5 4v9M11 4v9" opacity="0.55" />
  </svg>
{:else if type === 'mqtt'}
  <!-- MQTT: telemetry pulse (broadcast arcs). -->
  <svg
    viewBox="0 0 16 16"
    width={size}
    height={size}
    fill="none"
    stroke="currentColor"
    stroke-width="1.4"
    stroke-linecap="round"
    aria-label={label || undefined}
    aria-hidden={hidden}
    {role}
  >
    <circle cx="3" cy="13" r="1.3" fill="currentColor" />
    <path d="M3 9.5a3.5 3.5 0 013.5 3.5" />
    <path d="M3 5.5A7.5 7.5 0 0110.5 13" />
    <path d="M3 1.5a11.5 11.5 0 0111.5 11.5" opacity="0.6" />
  </svg>
{:else if type === 'nats'}
  <!-- NATS: subject-based pub-sub (parallel arrows). -->
  <svg
    viewBox="0 0 16 16"
    width={size}
    height={size}
    fill="none"
    stroke="currentColor"
    stroke-width="1.4"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-label={label || undefined}
    aria-hidden={hidden}
    {role}
  >
    <path d="M2 5h9M2 11h9" />
    <path d="M9 3l3 2-3 2M9 9l3 2-3 2" />
  </svg>
{:else if type === 'amqp10'}
  <!-- AMQP 1.0: envelope + flow direction (Service Bus shape). -->
  <svg
    viewBox="0 0 16 16"
    width={size}
    height={size}
    fill="none"
    stroke="currentColor"
    stroke-width="1.4"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-label={label || undefined}
    aria-hidden={hidden}
    {role}
  >
    <rect x="2" y="3.5" width="12" height="9" rx="1.5" />
    <path d="M2.5 4.5l5.5 4 5.5-4" />
    <path d="M5 11h3" />
  </svg>
{:else}
  <!-- Fallback — generic queue glyph used when type is unknown. Keeps
       the layout stable without leaking "missing icon" placeholders into
       production builds. -->
  <svg
    viewBox="0 0 16 16"
    width={size}
    height={size}
    fill="none"
    stroke="currentColor"
    stroke-width="1.4"
    stroke-linecap="round"
    aria-label={label || undefined}
    aria-hidden={hidden}
    {role}
  >
    <rect x="2" y="5" width="12" height="6" rx="1" />
    <path d="M5 5v6M8 5v6M11 5v6" opacity="0.5" />
  </svg>
{/if}
