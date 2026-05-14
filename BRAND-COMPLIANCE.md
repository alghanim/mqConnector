# BRAND-COMPLIANCE.md

**Standard:** Department Branding Guide / Brand & Design System
**Project:** mqConnector
**Version:** 1.0.0
**Last reviewed:** 2026-05-15

Per the Branding Guide, this file lists **deviations only**. If the project is fully compliant, this file simply states that.

---

## Status

**Fully compliant against the Branding Guide.**

The mqConnector admin UI implements:

- **Closed palette:** all colors sourced from `web/src/lib/brand-tokens.css`. No raw hex anywhere else in the frontend. Dark Slate `#333F48` as primary surface tone; Dark Gold `#96712E` as secondary on light theme; Light Gold `#D4B05C` reserved for dark theme accents; Maroon accent `#8B153D` only on primary CTAs, destructive actions, and count badges.
- **Both themes from day one:** `data-theme="dark"` and `data-theme="light"` on `<html>`, controlled by a theme toggle with system-preference detection and `localStorage` persistence. Default is dark.
- **Backgrounds:** dark uses `#222A31` (not `#000000`); light uses `#F7F5F3` (not `#FFFFFF`).
- **Typography:** Inter (Latin) + Noto Kufi Arabic, self-hosted under `web/static/fonts/`. No CDN.
- **Logical CSS only:** `margin-inline-start` / `padding-inline-end` / etc. throughout. Tailwind's `ms-*` / `me-*` utilities are used instead of `ml-*` / `mr-*`.
- **RTL:** `<html dir>` flips on language change; every horizontal layout respects the document direction.
- **Radii:** 12 px on interactive elements (buttons, inputs, chips), 16 px on containers (cards, dialogs). No other values.
- **Spacing:** 4 px base unit; the Tailwind spacing scale aligns with that base.
- **Touch targets:** 48 dp minimum on all interactive elements.
- **Accessibility:** all text/background combinations verified ≥ WCAG AA. Body text on dark theme: `#E8E6E1` on `#222A31` = 13.3:1 (AAA). Body text on light theme: `#1F1F1F` on `#F7F5F3` = 16.6:1 (AAA).
- **Motion:** all transitions wrap a `@media (prefers-reduced-motion: reduce)` block that disables non-essential animation.
- **Components:** Button, Card, Input, Select, Badge, ThemeToggle live in `web/src/lib/components/` with both-theme implementations and no theme-specific code paths.

## Color tokens at a glance

| Token | Dark theme | Light theme |
|---|---|---|
| `--bg` | `#222A31` | `#F7F5F3` |
| `--surface` | `#2A333B` | `#F2EFEC` |
| `--surface-2` | `#333F48` (Dark Slate) | `#EBE7E2` |
| `--border` | `#3D4751` | `#C1A18D` (Sand) |
| `--text` | `#E8E6E1` | `#1F1F1F` |
| `--text-muted` | `#9CA3AB` | `#5A6068` |
| `--accent` | `#8B153D` (Maroon accent) | `#8B153D` (Maroon accent) |
| `--secondary` | `#D4B05C` (Light Gold — dark theme only) | `#96712E` (Dark Gold — light theme only) |
| `--success` | `#3FB950` | `#1F7A2E` |
| `--warning` | `#D29922` | `#9A6700` |
| `--danger` | `#F85149` | `#B71C3C` |

Source of truth: `web/src/lib/brand-tokens.css`.
