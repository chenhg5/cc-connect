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
  Plug,
  Puzzle,
  X,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { useState } from 'react';

const navItems = [
  { key: 'dashboard', path: '/', icon: LayoutDashboard },
  { key: 'projects', path: '/projects', icon: FolderKanban },
  { key: 'providers', path: '/providers', icon: Plug },
  { key: 'skills', path: '/skills', icon: Puzzle },
  { key: 'sessions', path: '/sessions', icon: MessageSquare },
  { key: 'chat', path: '/chat', icon: MessageSquare },
  { key: 'cron', path: '/cron', icon: Clock },
  { key: 'system', path: '/system', icon: Settings },
];

interface SidebarProps {
  mobileOpen?: boolean;
  onMobileClose?: () => void;
}

export default function Sidebar({ mobileOpen = false, onMobileClose }: SidebarProps) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState(false);

  const sidebar = (
    <aside
      className={cn(
        'h-screen flex flex-col border-r transition-all duration-300 ease-out',
        'bg-surface/78 backdrop-blur-xl border-black/[0.08]',
        'dark:bg-surface/78 dark:backdrop-blur-xl dark:border-white/[0.1]',
        collapsed ? 'lg:w-16' : 'lg:w-56',
        'w-72 max-w-[82vw]',
      )}
    >
      {/* Brand */}
      <div
        className={cn(
          'flex items-center px-4 h-14 border-b transition-colors shrink-0',
          'border-black/[0.08] dark:border-white/[0.1]',
          collapsed ? 'justify-center' : 'gap-0',
        )}
      >
        {collapsed ? (
          <span className="text-base font-bold tracking-tighter text-gray-900 dark:text-white">
            CC
          </span>
        ) : (
          <span className="text-base font-bold tracking-tight text-gray-900 dark:text-white">
            CC<span className="text-accent">-</span>Connect
          </span>
        )}
        <button
          type="button"
          onClick={onMobileClose}
          className="ml-auto p-1.5 rounded-lg text-gray-400 hover:bg-gray-100 dark:hover:bg-white/[0.06] lg:hidden"
          aria-label="Close navigation"
        >
          <X size={18} />
        </button>
      </div>

      {/* Navigation */}
      <nav className="flex-1 py-4 space-y-1 px-2 overflow-y-auto">
        {navItems.map(({ key, path, icon: Icon }) => (
          <NavLink
            key={key}
            to={path}
            end={path === '/'}
            onClick={onMobileClose}
            className={({ isActive }) =>
              cn(
                'flex items-center gap-3 px-3 py-2.5 rounded-xl text-sm font-medium transition-all duration-200',
                isActive
                  ? 'bg-accent/[0.14] text-accent ring-1 ring-accent/30'
                  : 'text-ink/70 hover:bg-ink/[0.06] hover:text-ink',
              )
            }
          >
            <Icon size={18} className="shrink-0" />
            {!collapsed && <span>{t(`nav.${key}`)}</span>}
          </NavLink>
        ))}
      </nav>

      {/* Collapse toggle */}
      <div className={cn('border-t p-2', 'border-black/[0.08] dark:border-white/[0.1]')}>
        <button
          type="button"
          onClick={() => setCollapsed(!collapsed)}
          className={cn(
            'flex items-center justify-center w-full px-3 py-2 rounded-xl transition-colors duration-200',
            'text-ink/45 hover:bg-ink/[0.06] hover:text-ink',
          )}
        >
          {collapsed ? <ChevronRight size={18} /> : <ChevronLeft size={18} />}
        </button>
      </div>
    </aside>
  );

  return (
    <>
      <div className="hidden lg:block">{sidebar}</div>
      {mobileOpen && (
        <div className="fixed inset-0 z-50 lg:hidden">
          <div className="absolute inset-0 bg-ink/[0.07] dark:bg-black/25" onClick={onMobileClose} />
          <div className="absolute inset-y-0 left-0 shadow-2xl shadow-black/25">
            {sidebar}
          </div>
        </div>
      )}
    </>
  );
}
