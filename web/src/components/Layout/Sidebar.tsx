import { NavLink } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  LayoutDashboard,
  FolderKanban,
  MessageSquare,
  Clock,
  Settings,
  ChevronLeft,
  ChevronRight,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { useState } from 'react';

const navItems = [
  { key: 'dashboard', path: '/', icon: LayoutDashboard },
  { key: 'projects', path: '/projects', icon: FolderKanban },
  { key: 'chat', path: '/chat', icon: MessageSquare },
  { key: 'cron', path: '/cron', icon: Clock },
  { key: 'system', path: '/system', icon: Settings },
];

export default function Sidebar() {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState(false);

  return (
    <aside
      className={cn(
        'h-screen flex flex-col border-r transition-all duration-300 ease-out',
        'bg-white/75 backdrop-blur-xl border-gray-200/80',
        'dark:bg-[rgba(0,0,0,0.85)] dark:backdrop-blur-xl dark:border-white/[0.08]',
        collapsed ? 'w-16' : 'w-56',
      )}
    >
      {/* Logo */}
      <div
        className={cn(
          'flex items-center gap-3 px-4 h-14 border-b transition-colors shrink-0',
          'border-gray-200/80 dark:border-white/[0.08]',
        )}
      >
        <div
          className={cn(
            'w-8 h-8 rounded-xl flex items-center justify-center shrink-0',
            'bg-gray-900/90 dark:bg-white/10 ring-1 ring-black/5 dark:ring-white/10',
          )}
        >
          <div className="w-3 h-3 rounded-full bg-accent dark:shadow-[0_0_12px_rgb(var(--color-accent)/0.45)]" />
        </div>
        {!collapsed && (
          <span className="font-semibold text-gray-900 dark:text-white text-sm tracking-tight">
            CC-Connect
          </span>
        )}
      </div>

      {/* Navigation */}
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
                  ? 'bg-accent/12 text-accent ring-1 ring-accent/25'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100/80 dark:hover:bg-white/[0.06] hover:text-gray-900 dark:hover:text-white',
              )
            }
          >
            <Icon size={18} className="shrink-0" />
            {!collapsed && <span>{t(`nav.${key}`)}</span>}
          </NavLink>
        ))}
      </nav>

      {/* Collapse toggle */}
      <div className={cn('border-t p-2', 'border-gray-200/80 dark:border-white/[0.08]')}>
        <button
          type="button"
          onClick={() => setCollapsed(!collapsed)}
          className={cn(
            'flex items-center justify-center w-full px-3 py-2 rounded-xl transition-colors duration-200',
            'text-gray-400 hover:bg-gray-100/80 dark:hover:bg-white/[0.06]',
          )}
        >
          {collapsed ? <ChevronRight size={18} /> : <ChevronLeft size={18} />}
        </button>
      </div>
    </aside>
  );
}
