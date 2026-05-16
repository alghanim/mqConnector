<!--
  Avatar — circular initials chip. We don't store user photos, so this
  generates a stable colour from the username hash and shows up to two
  initials. Used in the profile dropdown, audit log, member tables.
-->
<script lang="ts">
  export let name = '';
  export let sub = '';
  export let size: 'sm' | 'md' | 'lg' = 'md';

  $: initials = computeInitials(name || sub);

  function computeInitials(s: string): string {
    if (!s) return '?';
    const parts = s
      .replace(/[._-]+/g, ' ')
      .split(/\s+/)
      .filter(Boolean);
    if (parts.length === 0) return s.charAt(0).toUpperCase();
    if (parts.length === 1) return parts[0].charAt(0).toUpperCase();
    return (parts[0].charAt(0) + parts[parts.length - 1].charAt(0)).toUpperCase();
  }

  // Stable per-user hue derived from the sub/name; restricted to the
  // brand-friendly band (golds/copper) — never hot pink, never neon.
  $: hue = stableHue(sub || name);
  function stableHue(s: string): number {
    let h = 0;
    for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
    // Map into 25°–55° (warm copper → gold band).
    return 25 + (h % 30);
  }

  $: sizeCls = size === 'sm' ? 'h-7 w-7 text-[11px]' : size === 'lg' ? 'h-10 w-10 text-sm' : 'h-8 w-8 text-xs';
</script>

<span
  class="inline-flex items-center justify-center rounded-full font-semibold tracking-wide select-none {sizeCls}"
  style:background-color="hsl({hue} 35% 35% / 0.9)"
  style="color: #FDF6E8; box-shadow: inset 0 0 0 1px rgba(255,255,255,0.08);"
  aria-hidden="true"
>
  {initials}
</span>
