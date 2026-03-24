import { NavLink } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  LayoutDashboard,
  FolderKanban,
  MessageSquare,
  Clock,
  Cable,
  Settings,
  Sun,
  Moon,
  Monitor,
  LogOut,
  ChevronLeft,
  ChevronRight,
  Languages,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { useThemeStore } from '@/store/theme';
import { useAuthStore } from '@/store/auth';
import { useState } from 'react';

const navItems = [
  { key: 'dashboard', path: '/', icon: LayoutDashboard },
  { key: 'projects', path: '/projects', icon: FolderKanban },
  { key: 'sessions', path: '/sessions', icon: MessageSquare },
  { key: 'cron', path: '/cron', icon: Clock },
  { key: 'bridge', path: '/bridge', icon: Cable },
  { key: 'system', path: '/system', icon: Settings },
];

const languages = [
  { code: 'en', label: 'English' },
  { code: 'zh', label: '中文' },
  { code: 'zh-TW', label: '繁體中文' },
  { code: 'ja', label: '日本語' },
  { code: 'es', label: 'Español' },
];

export default function Sidebar() {
  const { t, i18n } = useTranslation();
  const { theme, setTheme } = useThemeStore();
  const logout = useAuthStore((s) => s.logout);
  const [collapsed, setCollapsed] = useState(false);
  const [langOpen, setLangOpen] = useState(false);

  const changeLang = (code: string) => {
    i18n.changeLanguage(code);
    localStorage.setItem('cc_lang', code);
    setLangOpen(false);
  };

  const themeIcons = { light: Sun, dark: Moon, system: Monitor };
  const nextTheme = { light: 'dark' as const, dark: 'system' as const, system: 'light' as const };
  const ThemeIcon = themeIcons[theme];

  return (
    <aside
      className={cn(
        'h-screen flex flex-col border-r transition-all duration-300 ease-out',
        'bg-white/75 backdrop-blur-xl border-gray-200/80',
        'dark:bg-[rgba(0,0,0,0.85)] dark:backdrop-blur-xl dark:border-white/[0.08]',
        collapsed ? 'w-16' : 'w-60'
      )}
    >
      <div
        className={cn(
          'flex items-center gap-3 px-4 h-16 border-b transition-colors',
          'border-gray-200/80 dark:border-white/[0.08]'
        )}
      >
        <div
          className={cn(
            'w-8 h-8 rounded-xl flex items-center justify-center shrink-0',
            'bg-gray-900/90 dark:bg-white/10 ring-1 ring-black/5 dark:ring-white/10'
          )}
        >
          <div className="w-3 h-3 rounded-full bg-accent shadow-[0_0_12px_rgba(66,255,156,0.45)]" />
        </div>
        {!collapsed && (
          <span className="font-semibold text-gray-900 dark:text-white text-sm tracking-tight">
            CC-Connect
          </span>
        )}
      </div>

      <nav className="flex-1 py-4 space-y-1 px-2 overflow-y-auto">
        {navItems.map(({ key, path, icon: Icon }) => (
          <NavLink
            key={key}
            to={path}
            end={path === '/'}
            className={({ isActive }) =>
              cn(
                'flex items-center gap-3 px-3 py-2.5 rounded-xl text-sm font-medium transition-all duration-200',
                isActive
                  ? 'bg-accent/15 text-gray-900 dark:text-white ring-1 ring-accent/35 shadow-[0_0_20px_-8px_rgba(66,255,156,0.5)]'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100/80 dark:hover:bg-white/[0.06] hover:text-gray-900 dark:hover:text-white'
              )
            }
          >
            <Icon size={20} className="shrink-0" />
            {!collapsed && <span>{t(`nav.${key}`)}</span>}
          </NavLink>
        ))}
      </nav>

      <div
        className={cn(
          'border-t p-2 space-y-1',
          'border-gray-200/80 dark:border-white/[0.08]'
        )}
      >
        <div className="relative">
          <button
            type="button"
            onClick={() => setLangOpen(!langOpen)}
            className={cn(
              'flex items-center gap-3 w-full px-3 py-2 rounded-xl text-sm transition-all duration-200',
              'text-gray-600 dark:text-gray-400',
              'hover:bg-gray-100/80 dark:hover:bg-white/[0.06]'
            )}
          >
            <Languages size={20} className="shrink-0" />
            {!collapsed && (
              <span>{languages.find((l) => l.code === i18n.language)?.label || 'English'}</span>
            )}
          </button>
          {langOpen && (
            <div
              className={cn(
                'absolute bottom-full left-0 mb-1 w-48 rounded-xl py-1 z-50 overflow-hidden',
                'bg-white/95 backdrop-blur-xl border border-gray-200/80 shadow-xl shadow-black/10',
                'dark:bg-[rgba(0,0,0,0.88)] dark:border-white/[0.1] dark:shadow-black/40'
              )}
            >
              {languages.map((l) => (
                <button
                  key={l.code}
                  type="button"
                  onClick={() => changeLang(l.code)}
                  className={cn(
                    'w-full text-left px-3 py-2 text-sm transition-colors duration-150',
                    i18n.language === l.code
                      ? 'text-accent font-medium bg-accent/10'
                      : 'text-gray-700 dark:text-gray-300 hover:bg-gray-100/80 dark:hover:bg-white/[0.06]'
                  )}
                >
                  {l.label}
                </button>
              ))}
            </div>
          )}
        </div>

        <button
          type="button"
          onClick={() => setTheme(nextTheme[theme])}
          className={cn(
            'flex items-center gap-3 w-full px-3 py-2 rounded-xl text-sm transition-all duration-200',
            'text-gray-600 dark:text-gray-400',
            'hover:bg-gray-100/80 dark:hover:bg-white/[0.06]'
          )}
        >
          <ThemeIcon size={20} className="shrink-0" />
          {!collapsed && <span>{t(`theme.${theme}`)}</span>}
        </button>

        <button
          type="button"
          onClick={logout}
          className={cn(
            'flex items-center gap-3 w-full px-3 py-2 rounded-xl text-sm transition-all duration-200',
            'text-gray-600 dark:text-gray-400',
            'hover:bg-red-500/10 hover:text-red-600 dark:hover:text-red-400'
          )}
        >
          <LogOut size={20} className="shrink-0" />
          {!collapsed && <span>{t('login.logout')}</span>}
        </button>

        <button
          type="button"
          onClick={() => setCollapsed(!collapsed)}
          className={cn(
            'flex items-center justify-center w-full px-3 py-2 rounded-xl transition-colors duration-200',
            'text-gray-400 hover:bg-gray-100/80 dark:hover:bg-white/[0.06]'
          )}
        >
          {collapsed ? <ChevronRight size={18} /> : <ChevronLeft size={18} />}
        </button>
      </div>
    </aside>
  );
}
