import { useState } from 'react';
import { Outlet } from 'react-router-dom';
import Sidebar from './Sidebar';
import Header from './Header';
import Footer from './Footer';
import { cn } from '@/lib/utils';

export default function Layout() {
  const [mobileNavigationOpen, setMobileNavigationOpen] = useState(false);

  return (
    <div
      className={cn(
        'flex h-screen overflow-hidden',
        'bg-gray-100 dark:bg-[#0a0a0c]',
      )}
    >
      <Sidebar
        mobileOpen={mobileNavigationOpen}
        onMobileClose={() => setMobileNavigationOpen(false)}
      />
      <div className="flex-1 flex flex-col overflow-hidden min-w-0">
        <Header onOpenNavigation={() => setMobileNavigationOpen(true)} />
        <main className="flex-1 overflow-y-auto p-3 sm:p-4 md:p-6 flex flex-col min-h-0">
          <div className="flex-1 flex flex-col">
            <Outlet />
          </div>
          <Footer />
        </main>
      </div>
    </div>
  );
}
