import type { Config } from 'tailwindcss';
import forms from '@tailwindcss/forms';

// Every color must come from the brand tokens defined in
// src/lib/brand-tokens.css. We expose them as Tailwind colors so utility
// classes like `bg-surface text-accent` work, but Tailwind never sees a hex
// value directly — only var() references.
const config: Config = {
  content: ['./src/**/*.{html,svelte,ts,js}'],
  theme: {
    extend: {
      colors: {
        bg: 'var(--bg)',
        surface: 'var(--surface)',
        'surface-2': 'var(--surface-2)',
        border: 'var(--border)',
        text: 'var(--text)',
        'text-muted': 'var(--text-muted)',
        accent: 'var(--accent)',
        'accent-on': 'var(--accent-on)',
        secondary: 'var(--secondary)',
        success: 'var(--success)',
        warning: 'var(--warning)',
        danger: 'var(--danger)'
      },
      borderRadius: {
        // Brand spec: 12px on interactive, 16px on containers, pill for badges.
        interactive: '12px',
        container: '16px'
      },
      fontFamily: {
        sans: ['Inter Variable', 'Inter', 'Noto Kufi Arabic', 'system-ui', 'sans-serif']
      },
      boxShadow: {
        card: '0 1px 2px rgba(0,0,0,0.06), 0 4px 12px rgba(0,0,0,0.08)'
      },
      minHeight: {
        // 48dp minimum touch target.
        touch: '48px'
      },
      minWidth: {
        touch: '48px'
      }
    }
  },
  plugins: [forms]
};

export default config;
