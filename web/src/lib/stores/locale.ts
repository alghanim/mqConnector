// Locale + direction store. Two languages supported — en (LTR) and ar (RTL).
// Direction is bound to the document so logical CSS properties take effect.
import { writable } from 'svelte/store';
import { browser } from '$app/environment';

export type Locale = 'en' | 'ar';

const strings: Record<Locale, Record<string, string>> = {
  en: {
    'app.title': 'mqConnector',
    'app.subtitle': 'Message-queue bridge & pipeline runner',
    'nav.overview': 'Overview',
    'nav.connections': 'Connections',
    'nav.pipelines': 'Pipelines',
    'nav.dlq': 'Dead-letter queue',
    'nav.metrics': 'Metrics',
    'nav.logout': 'Sign out',
    'login.title': 'Sign in to mqConnector',
    'login.username': 'Username',
    'login.password': 'Password',
    'login.submit': 'Sign in',
    'login.error': 'Invalid credentials',
    'common.save': 'Save',
    'common.cancel': 'Cancel',
    'common.delete': 'Delete',
    'common.retry': 'Retry',
    'common.add': 'Add',
    'common.edit': 'Edit',
    'common.search': 'Search',
    'common.theme.light': 'Light',
    'common.theme.dark': 'Dark',
    'common.lang.english': 'English',
    'common.lang.arabic': 'العربية'
  },
  ar: {
    'app.title': 'موصل قوائم الرسائل',
    'app.subtitle': 'جسر قوائم الرسائل ومحرك التدفقات',
    'nav.overview': 'نظرة عامة',
    'nav.connections': 'الاتصالات',
    'nav.pipelines': 'التدفقات',
    'nav.dlq': 'الرسائل الفاشلة',
    'nav.metrics': 'القياسات',
    'nav.logout': 'تسجيل الخروج',
    'login.title': 'تسجيل الدخول',
    'login.username': 'اسم المستخدم',
    'login.password': 'كلمة المرور',
    'login.submit': 'دخول',
    'login.error': 'بيانات الاعتماد غير صحيحة',
    'common.save': 'حفظ',
    'common.cancel': 'إلغاء',
    'common.delete': 'حذف',
    'common.retry': 'إعادة المحاولة',
    'common.add': 'إضافة',
    'common.edit': 'تعديل',
    'common.search': 'بحث',
    'common.theme.light': 'فاتح',
    'common.theme.dark': 'داكن',
    'common.lang.english': 'English',
    'common.lang.arabic': 'العربية'
  }
};

function initial(): Locale {
  if (!browser) return 'en';
  const stored = localStorage.getItem('mqc-locale') as Locale | null;
  if (stored === 'en' || stored === 'ar') return stored;
  return 'en';
}

function createLocale() {
  const { subscribe, set } = writable<Locale>(initial());
  return {
    subscribe,
    set(value: Locale) {
      if (browser) {
        const dir = value === 'ar' ? 'rtl' : 'ltr';
        document.documentElement.setAttribute('lang', value);
        document.documentElement.setAttribute('dir', dir);
        localStorage.setItem('mqc-locale', value);
        localStorage.setItem('mqc-dir', dir);
      }
      set(value);
    }
  };
}

export const locale = createLocale();

// t() is a one-arg key→string lookup. Components subscribe to the locale
// store and re-invoke t() reactively.
export function t(loc: Locale, key: string): string {
  return strings[loc][key] || key;
}
