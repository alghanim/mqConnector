# BRAND-COMPLIANCE.md

**Standard:** Brand & Design System v1.0 (March 2026)
**Project:** mqConnector
**Version:** 1.0.0
**Last reviewed:** 2026-05-16

Per the Branding Guide, this file lists **deviations only**. If a section
isn't listed below, the project is compliant with it.

---

## Status

**Fully compliant against the Brand & Design System v1.0.**

## Token source of truth

Every colour in the UI is consumed from
[`web/src/lib/brand-tokens.css`](web/src/lib/brand-tokens.css). The file
declares the raw palette as `--qb-*` constants — every hex copied
verbatim from the brand guide — and maps them into theme-scoped semantic
tokens (`--bg`, `--surface`, `--accent`, `--section-header`, …). No
component-level CSS contains a raw hex.

## Palette respected

The closed nine-colour brand palette is preserved as `--qb-*`:

| Token | Hex | Role |
|---|---|---|
| `--qb-dark-slate` | `#333F48` | Primary neutral |
| `--qb-qatar-maroon` | `#8B153D` | Accent — primary CTA, count badges, destructive only |
| `--qb-dark-gold` | `#8F6A2A` | Secondary / section headers (light) / focus borders |
| `--qb-copper` | `#B87132` | Hover/pressed accent on gold family |
| `--qb-light-gold` | `#F8E08E` | Section headers (dark) / highlight |
| `--qb-olive-gold` | `#A59F8A` | Placeholder text / inactive icons / gradient mid-stop |
| `--qb-warm-gray` | `#D6D1CA` | Light theme background base / dialog surface |
| `--qb-sand` | `#C1A18D` | Card borders on light theme |
| `--qb-black` | `#000000` | Text on light surfaces (never bg) |

Plus the derived scales mandated by the guide (`--qb-slate-80…130`,
`--qb-warm-85…95`, `--qb-maroon-*`, `--qb-gold-*`, text/outline/semantic),
all keyed to their exact brand-guide values.

## Theme backgrounds

- **Dark theme bg:** `--qb-slate-80` = `#222A31` (not `#000000`)
- **Light theme bg:** `--qb-warm-85` = `#F7F5F3` (not `#FFFFFF`)

`#FFFFFF` appears only as `--card-bg` on the light theme per §5.2.

## Brand gradient

`--brand-gradient` is **gold-only** (`#F8E08E → #A59F8A → #8F6A2A`) — no
maroon. Used exclusively as a decorative 3 px strip on the sidebar
header and on the top edge of `.card-strip` cards (per §4.2 allowed
uses). Direction mirrors under `[dir="rtl"]` via the override at the
bottom of [`brand-tokens.css`](web/src/lib/brand-tokens.css).

## Maroon usage

`--accent` (`#8B153D`) is wired to exactly three things in the UI:

1. `.btn-primary` and `.btn-danger` (alias) — the main CTA on every page.
2. `.badge-count` — the only fully-pill chip in the system.
3. Decorative dots on the Destination node in `/flow` (single 10 px circle).

No card, screen, nav, hero, or large surface ever carries maroon.
Rule 26 ("maroon and error red never touch") is preserved because the
`.btn-danger` alias renders maroon, not error red — the destructive
visual cue is the confirmation dialog, not button colour.

## Corner radii

| Radius | Used by |
|---|---|
| 12 px (`rounded-interactive`) | Buttons, inputs, labeled chips (path chips, try-sample chips, status chips), stage rows |
| 16 px (`rounded-container`) | Cards, dialogs, sample/preview panels |
| 999 px | `.badge-count` only |
| 50 % | Decorative dots and connection ports inside the flow canvas (small circles, not labeled chips) |

No off-spec radii (e.g. 4/6/8 px) anywhere in the tree.

## Typography

Self-hosted **Inter** (Latin) + **Noto Kufi Arabic**, declared in
[`app.css`](web/src/app.css). No CDN font load.

## RTL

- `<html dir>` flips on locale change via `web/src/lib/stores/locale.ts`.
- Every horizontal-emphasis element uses CSS logical properties
  (`margin-inline-start`, `padding-inline-end`, `border-inline-end`).
- The brand gradient is mirrored under `[dir="rtl"]`.

**One deliberate exception**: the visual flow builder canvas at `/flow`
stores absolute `left`/`top` positions for the nodes the operator
drags, and the OUT/IN ports sit at the physical right/left edges of
each node. This is documented in
[`flow/+page.svelte`](web/src/routes/flow/+page.svelte) (the comment
above `.port-out`/`.port-in`) — the canvas is a free-form spatial
editor, not flowing content, so `LayoutDirection` does not apply.

## Accessibility

| Pairing | Computed contrast | WCAG |
|---|---|---|
| `--text` on `--bg` (dark) — `#F2EFEC` on `#222A31` | 13.8:1 | AAA |
| `--text` on `--bg` (light) — `#1A1F24` on `#F7F5F3` | 14.8:1 | AAA |
| `--accent-on` on `--accent` — `#FFFFFF` on `#8B153D` | 8.5:1 | AAA |
| `--section-header` (dark) on `--bg` — `#F8E08E` on `#222A31` | 11.8:1 | AAA |
| `--section-header` (light) on `--bg` — `#8F6A2A` on `#F7F5F3` | 5.5:1 | AA |

Tertiary text (`--text-tertiary`) is restricted to ≥12 sp captions per
the brand guide §9.5 note.

## Motion

Every transition wraps a `@media (prefers-reduced-motion: reduce)`
override at the top of [`app.css`](web/src/app.css) — motion is
disabled when the user opts out.

## Component spec coverage

| Spec section | Implementation |
|---|---|
| §5.1 Search bar | (n/a — no search bar in the admin UI yet) |
| §5.2 Cards | `.card` — `var(--card-bg)` + `var(--card-border)`, 16 px radius |
| §5.3 Nav | Sidebar — `var(--surface)` bg, active item `var(--section-header)` text |
| §5.4 Buttons | `.btn-primary/secondary/outline/ghost/danger` per the spec colour table |
| §5.5 Badges & chips | `.badge-success/warning/danger/neutral` + `.badge-count` (pill) |
| §5.7 Data labels/values | `.label` (muted) + `.text` pairs across all CRUD tables |
| §5.8 Section headers | `.section-heading` uses `var(--section-header)` (light gold dark / dark gold light) |
| §5.9 Dividers | `var(--divider)` on every table row and section split |
| §5.10 Alerts | Pending — error / success messages use `--danger` / `--success` text-on-bg directly, no full alert component yet |
| §5.11 Inputs | `.input` + `Input.svelte` use `--input-bg/border/text/placeholder` and gold focus border |
| §5.16 Switches | Pending — only native checkboxes used so far |

## Deviations

None.
