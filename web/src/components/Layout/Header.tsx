import { useTranslation } from 'react-i18next';
import { useState, useRef, useEffect } from 'react';
import {
  RefreshCw, Sun, Moon, Monitor, LogOut, Languages, ChevronDown,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { useThemeStore } from '@/store/theme';
import { useAuthStore } from '@/store/auth';

const languages = [
  { code: 'en', label: 'EN' },
  { code: 'zh', label: '中文' },
  { code: 'zh-TW', label: '繁體' },
  { code: 'ja', label: '日本語' },
  { code: 'es', label: 'ES' },
];

export default function Header() {
  const { t, i18n } = useTranslation();
  const { theme, setTheme } = useThemeStore();
  const logout = useAuthStore((s) => s.logout);
  const [spinning, setSpinning] = useState(false);
  const [langOpen, setLangOpen] = useState(false);
  const langRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (langRef.current && !langRef.current.contains(e.target as Node)) setLangOpen(false);
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);

  const handleRefresh = () => {
    setSpinning(true);
    window.dispatchEvent(new CustomEvent('cc:refresh'));
    setTimeout(() => setSpinning(false), 1000);
  };

  const themeIcons = { light: Sun, dark: Moon, system: Monitor };
  const nextTheme = { light: 'dark' as const, dark: 'system' as const, system: 'light' as const };
  const ThemeIcon = themeIcons[theme];

  const changeLang = (code: string) => {
    i18n.changeLanguage(code);
    localStorage.setItem('cc_lang', code);
    setLangOpen(false);
  };

  const btnCls = cn(
    'p-2 rounded-lg transition-all duration-200',
    'text-gray-500 dark:text-gray-400',
    'hover:bg-gray-100/90 dark:hover:bg-white/[0.08] hover:text-gray-800 dark:hover:text-white',
  );

  return (
    <header
      className={cn(
        'h-14 flex items-center justify-end gap-1 px-4 shrink-0 relative z-20',
        'border-b border-gray-200/80 dark:border-white/[0.08]',
        'bg-white/70 backdrop-blur-xl dark:bg-[rgba(0,0,0,0.72)]',
      )}
    >
      <button type="button" onClick={handleRefresh} className={btnCls} aria-label={t('common.refresh')}>
        <RefreshCw size={16} className={spinning ? 'animate-spin' : ''} />
      </button>

      {/* Language */}
      <div className="relative" ref={langRef}>
        <button type="button" onClick={() => setLangOpen(!langOpen)} className={cn(btnCls, 'flex items-center gap-1')}>
          <Languages size={16} />
          <span className="text-xs hidden sm:inline">{languages.find(l => l.code === i18n.language)?.label}</span>
        </button>
        {langOpen && (
          <div className={cn(
            'absolute right-0 top-full mt-1 w-36 rounded-xl py-1 z-50 overflow-hidden',
            'bg-white/95 backdrop-blur-xl border border-gray-200/80 shadow-xl shadow-black/10',
            'dark:bg-[rgba(0,0,0,0.88)] dark:border-white/[0.1] dark:shadow-black/40',
          )}>
            {languages.map(l => (
              <button
                key={l.code}
                type="button"
                onClick={() => changeLang(l.code)}
                className={cn(
                  'w-full text-left px-3 py-1.5 text-sm transition-colors',
                  i18n.language === l.code
                    ? 'text-accent font-medium bg-accent/10'
                    : 'text-gray-700 dark:text-gray-300 hover:bg-gray-100/80 dark:hover:bg-white/[0.06]',
                )}
              >
                {l.label}
              </button>
            ))}
          </div>
        )}
      </div>

      {/* Theme */}
      <button type="button" onClick={() => setTheme(nextTheme[theme])} className={btnCls} aria-label="Theme">
        <ThemeIcon size={16} />
      </button>

      {/* Logout */}
      <button
        type="button"
        onClick={logout}
        className={cn(
          'p-2 rounded-lg transition-all duration-200',
          'text-gray-400 hover:bg-red-500/10 hover:text-red-600 dark:hover:text-red-400',
        )}
        aria-label={t('login.logout')}
      >
        <LogOut size={16} />
      </button>
    </header>
  );
}
