import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import en from './locales/en.json';
import zh from './locales/zh.json';

// Only English and Simplified Chinese are supported. A stale cc_lang=ja/es/
// zh-TW value falls back to English instead of rendering a blank page.
const raw = localStorage.getItem('cc_lang') || navigator.language.split('-')[0] || 'en';
const saved = raw === 'zh' ? 'zh' : 'en';

i18n.use(initReactI18next).init({
  resources: {
    en: { translation: en },
    zh: { translation: zh },
  },
  lng: saved,
  fallbackLng: 'en',
  interpolation: { escapeValue: false },
});

export default i18n;
