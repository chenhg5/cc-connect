import { useCallback, useEffect, useRef, useState } from 'react';
import { Outlet, useLocation } from 'react-router-dom';
import Sidebar from './Sidebar';
import Header from './Header';
import Footer from './Footer';
import { cn } from '@/lib/utils';

const SIDEBAR_DRAWER_ID = 'app-sidebar-drawer';
const DESKTOP_QUERY = '(min-width: 768px)';

export default function Layout() {
  const { pathname } = useLocation();
  const [mobileOpen, setMobileOpen] = useState(false);
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const restoreFocusRef = useRef(false);
  const wasOpenRef = useRef(false);

  const closeDrawer = useCallback((restoreFocus = false) => {
    restoreFocusRef.current = restoreFocus;
    setMobileOpen(false);
  }, []);

  // Post-commit focus restore: runs after `inert` has been removed from the content area.
  useEffect(() => {
    if (wasOpenRef.current && !mobileOpen && restoreFocusRef.current) {
      menuButtonRef.current?.focus();
    }
    wasOpenRef.current = mobileOpen;
    if (!mobileOpen) restoreFocusRef.current = false;
  }, [mobileOpen]);

  // Close on route change.
  useEffect(() => {
    closeDrawer(false);
  }, [pathname, closeDrawer]);

  // Close when the viewport crosses into desktop so no stale drawer/backdrop lingers.
  useEffect(() => {
    const mql = window.matchMedia(DESKTOP_QUERY);
    const onChange = (e: MediaQueryListEvent) => {
      if (e.matches) closeDrawer(false);
    };
    mql.addEventListener('change', onChange);
    return () => mql.removeEventListener('change', onChange);
  }, [closeDrawer]);

  // Close on Escape while open.
  useEffect(() => {
    if (!mobileOpen) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeDrawer(true);
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [mobileOpen, closeDrawer]);

  return (
    <div
      className={cn(
        'flex h-screen supports-[height:100dvh]:h-dvh overflow-hidden',
        'bg-gradient-to-br from-gray-100 via-white to-gray-100',
        'dark:from-gray-950 dark:via-[#0a0a0c] dark:to-gray-950',
      )}
    >
      {/* Mobile drawer backdrop */}
      <div
        aria-hidden="true"
        onClick={() => closeDrawer(false)}
        className={cn(
          'fixed inset-0 z-[60] touch-none md:hidden bg-black/40 backdrop-blur-sm',
          'transition-opacity duration-300 ease-out',
          mobileOpen ? 'opacity-100' : 'opacity-0 pointer-events-none',
        )}
      />
      <Sidebar
        id={SIDEBAR_DRAWER_ID}
        mobileOpen={mobileOpen}
        onClose={() => closeDrawer(true)}
        onNavigate={(isActiveRoute) => closeDrawer(isActiveRoute)}
      />
      <div inert={mobileOpen} className="flex-1 flex flex-col overflow-hidden min-w-0">
        <Header
          menuButtonRef={menuButtonRef}
          menuOpen={mobileOpen}
          menuControlsId={SIDEBAR_DRAWER_ID}
          onMenuClick={() => setMobileOpen((open) => !open)}
        />
        <main className="flex-1 overflow-y-auto p-4 md:p-6 flex flex-col min-h-0">
          <div className="flex-1 flex flex-col">
            <Outlet />
          </div>
          <Footer />
        </main>
      </div>
    </div>
  );
}
