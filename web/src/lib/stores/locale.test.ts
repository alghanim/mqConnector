import { describe, it, expect } from 'vitest';
import { get } from 'svelte/store';
import { locale, t } from './locale';

describe('locale store', () => {
  it('returns english strings by default', () => {
    expect(t(get(locale), 'login.title')).toContain('Sign in');
  });

  it('switches direction when locale flips to ar', () => {
    locale.set('ar');
    expect(document.documentElement.getAttribute('dir')).toBe('rtl');
    expect(document.documentElement.getAttribute('lang')).toBe('ar');
  });

  it('returns arabic strings after switch', () => {
    locale.set('ar');
    expect(t(get(locale), 'login.title')).toContain('تسجيل');
  });

  it('returns the key unchanged on a miss', () => {
    expect(t(get(locale), 'this.key.does.not.exist')).toBe('this.key.does.not.exist');
  });

  it('persists to localStorage', () => {
    locale.set('ar');
    expect(localStorage.getItem('mqc-locale')).toBe('ar');
  });
});
