import { useTranslation } from 'react-i18next';
import { useLocation } from 'react-router-dom';
import { RefreshCw } from 'lucide-react';
import { useState } from 'react';
import { cn } from '@/lib/utils';

const routeTitles: Record<string, string> = {
  '/': 'nav.dashboard',
  '/projects': 'nav.projects',
  '/chat': 'nav.chat',
  '/cron': 'nav.cron',
  '/bridge': 'nav.bridge',
  '/system': 'nav.system',
};

export default function Header() {
  const { t } = useTranslation();
  const { pathname } = useLocation();
  const [spinning, setSpinning] = useState(false);

  const titleKey =
    Object.entries(routeTitles).find(([path]) =>
      path === '/' ? pathname === '/' : pathname.startsWith(path)
    )?.[1] || 'nav.dashboard';

  const handleRefresh = () => {
    setSpinning(true);
    window.dispatchEvent(new CustomEvent('cc:refresh'));
    setTimeout(() => setSpinning(false), 1000);
  };

  return (
    <header
      className={cn(
        'h-14 flex items-center justify-between px-6 shrink-0',
        'border-b border-gray-200/80 dark:border-white/[0.08]',
        'bg-white/70 backdrop-blur-xl dark:bg-[rgba(0,0,0,0.72)] dark:backdrop-blur-xl'
      )}
    >
      <h1 className="text-lg font-semibold text-gray-900 dark:text-white tracking-tight">
        {t(titleKey)}
      </h1>
      <button
        type="button"
        onClick={handleRefresh}
        className={cn(
          'p-2 rounded-xl transition-all duration-200',
          'text-gray-500 dark:text-gray-400',
          'hover:bg-gray-100/90 dark:hover:bg-white/[0.08] hover:text-gray-800 dark:hover:text-white',
          'focus:outline-none focus-visible:ring-2 focus-visible:ring-accent/40'
        )}
        aria-label={t('common.refresh')}
      >
        <RefreshCw size={18} className={spinning ? 'animate-spin' : ''} />
      </button>
    </header>
  );
}
