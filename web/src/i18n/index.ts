import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import en from './locales/en.json';
import zh from './locales/zh.json';

// Only English and Simplified Chinese are supported. Every zh-* variant
// (including a stale cc_lang=zh-TW or a zh-TW browser locale) maps to
// Simplified Chinese so the UI matches the backend language; other unknown
// values fall back to English instead of rendering a blank page.
const raw = (localStorage.getItem('cc_lang') || navigator.language || 'en').toLowerCase();
const saved = raw.startsWith('zh') ? 'zh' : 'en';

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
