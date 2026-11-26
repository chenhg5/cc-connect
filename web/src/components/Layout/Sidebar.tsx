import { NavLink, useLocation } from 'react-router-dom';
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
import { useEffect, useRef, useState, type KeyboardEvent } from 'react';

const navItems = [
  { key: 'dashboard', path: '/', icon: LayoutDashboard },
  { key: 'projects', path: '/projects', icon: FolderKanban },
  { key: 'providers', path: '/providers', icon: Plug },
  { key: 'skills', path: '/skills', icon: Puzzle },
  { key: 'chat', path: '/chat', icon: MessageSquare },
  { key: 'cron', path: '/cron', icon: Clock },
  { key: 'system', path: '/system', icon: Settings },
];

interface SidebarProps {
  id: string;
  /** Off-canvas drawer state on mobile (< md). Ignored on desktop. */
  mobileOpen: boolean;
  onClose: () => void;
  /** `isActiveRoute` is true when the clicked link already points at the current route. */
  onNavigate: (isActiveRoute: boolean) => void;
}

export default function Sidebar({ id, mobileOpen, onClose, onNavigate }: SidebarProps) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState(false);
  const { pathname } = useLocation();
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const asideRef = useRef<HTMLElement>(null);

  // Move focus into the drawer when it opens on mobile.
  useEffect(() => {
    if (mobileOpen) closeButtonRef.current?.focus();
  }, [mobileOpen]);

  // Keep Tab / Shift+Tab inside the drawer while it is open as a modal dialog.
  const handleKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    if (!mobileOpen || e.key !== 'Tab' || !asideRef.current) return;
    const focusables = Array.from(
      asideRef.current.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ),
    ).filter((el) => el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden');
    if (focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const active = document.activeElement;
    if (e.shiftKey && (active === first || !asideRef.current.contains(active))) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && (active === last || !asideRef.current.contains(active))) {
      e.preventDefault();
      first.focus();
    }
  };

  return (
    <aside
      ref={asideRef}
      id={id}
      role={mobileOpen ? 'dialog' : undefined}
      aria-modal={mobileOpen || undefined}
      aria-label={mobileOpen ? t('nav.navigation') : undefined}
      onKeyDown={handleKeyDown}
      className={cn(
        // Mobile: fixed off-canvas drawer above the backdrop (z-[60]) and page-level overlays.
        'fixed inset-y-0 left-0 z-[70] w-64 max-w-[85vw] flex flex-col border-r',
        'duration-300 ease-out',
        // Visibility is transitioned only while closing so `visible` applies at t=0 on open
        // (focus can move in immediately) and the closed drawer is not keyboard-focusable.
        mobileOpen
          ? 'translate-x-0 visible transition-transform shadow-2xl shadow-black/30'
          : '-translate-x-full invisible pointer-events-none transition-[transform,visibility]',
        // Desktop: permanent in-flow sidebar with the collapse toggle.
        'md:static md:z-auto md:h-full md:max-w-none md:translate-x-0 md:visible md:pointer-events-auto md:shadow-none',
        'md:transition-all md:duration-300 md:ease-out',
        collapsed ? 'md:w-16' : 'md:w-56',
        'bg-white/90 backdrop-blur-xl border-gray-200/80 md:bg-white/75',
        'dark:bg-[rgba(0,0,0,0.92)] dark:backdrop-blur-xl dark:border-white/[0.08] md:dark:bg-[rgba(0,0,0,0.85)]',
      )}
    >
      {/* Brand */}
      <div
        className={cn(
          'flex items-center px-4 h-14 border-b transition-colors shrink-0',
          'border-gray-200/80 dark:border-white/[0.08]',
          'justify-between',
          collapsed ? 'md:justify-center' : 'md:justify-start',
        )}
      >
        {collapsed && (
          <span className="hidden md:inline text-base font-bold tracking-tighter text-gray-900 dark:text-white">
            CC
          </span>
        )}
        <span
          className={cn(
            'text-base font-bold tracking-tight text-gray-900 dark:text-white',
            collapsed && 'md:hidden',
          )}
        >
          CC<span className="text-accent">-</span>Connect
        </span>
        <button
          ref={closeButtonRef}
          type="button"
          onClick={onClose}
          aria-label={t('nav.closeMenu')}
          className={cn(
            'md:hidden -mr-2 p-2.5 rounded-lg transition-colors duration-200',
            'text-gray-500 dark:text-gray-400',
            'hover:bg-gray-100/90 dark:hover:bg-white/[0.08] hover:text-gray-800 dark:hover:text-white',
          )}
        >
          <X size={18} />
        </button>
      </div>

      {/* Navigation */}
      <nav className="flex-1 py-4 space-y-1 px-2 overflow-y-auto overscroll-contain">
        {navItems.map(({ key, path, icon: Icon }) => (
          <NavLink
            key={key}
            to={path}
            end={path === '/'}
            onClick={() => onNavigate(path === pathname)}
            className={({ isActive }) =>
              cn(
                'flex items-center gap-3 px-3 py-3 md:py-2.5 rounded-xl text-sm font-medium transition-all duration-200',
                isActive
                  ? 'bg-accent/12 text-accent ring-1 ring-accent/25'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100/80 dark:hover:bg-white/[0.06] hover:text-gray-900 dark:hover:text-white',
              )
            }
          >
            <Icon size={18} className="shrink-0" />
            <span className={cn(collapsed && 'md:hidden')}>{t(`nav.${key}`)}</span>
          </NavLink>
        ))}
      </nav>

      {/* Collapse toggle (desktop only) */}
      <div className={cn('hidden md:block border-t p-2', 'border-gray-200/80 dark:border-white/[0.08]')}>
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
