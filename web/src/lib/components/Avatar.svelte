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

  // Stable per-user colour pulled from THREE palette tokens — never an
  // off-palette HSL. Three is plenty to visually disambiguate avatars
  // sitting next to each other in tables and dropdowns; using a wider
  // continuous hue range would put the avatar outside the closed
  // palette (DO/DON'T #1).
  $: tone = stableTone(sub || name);
  const TONES = ['copper', 'dark-gold', 'olive'] as const;
  function stableTone(s: string): (typeof TONES)[number] {
    let h = 0;
    for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
    return TONES[h % TONES.length];
  }

  $: sizeCls = size === 'sm' ? 'h-7 w-7 text-[11px]' : size === 'lg' ? 'h-10 w-10 text-sm' : 'h-8 w-8 text-xs';
</script>

<span
  class="avatar inline-flex items-center justify-center rounded-full font-semibold tracking-wide select-none {sizeCls}"
  data-tone={tone}
  aria-hidden="true"
>
  {initials}
</span>

<style>
  /*
   * Avatar background is one of three brand palette tokens, selected
   * deterministically from the username hash. Text colour is light-gold
   * container (an in-palette near-white), giving the right contrast on
   * each gold/copper background per §9.3. The 1px inner stroke is the
   * gold border token at low alpha so adjacent avatars read distinctly
   * without inventing a new colour.
   */
  .avatar {
    color: var(--palette-gold-container-light);
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--palette-dark-gold) 20%, transparent);
  }
  .avatar[data-tone='copper']    { background-color: var(--palette-copper); }
  .avatar[data-tone='dark-gold'] { background-color: var(--palette-dark-gold); }
  .avatar[data-tone='olive']     { background-color: var(--palette-olive-gold); }
</style>
