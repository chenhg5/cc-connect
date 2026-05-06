import { Outlet, useNavigate, useLocation } from 'react-router-dom';
import BottomTab from '@/components/Companion/BottomTab';
import { cn } from '@/lib/utils';

const tabToPath: Record<string, string> = {
  chat: '/companion',
  diary: '/companion/diary',
  timeline: '/companion/timeline',
  saved: '/companion/saved',
  settings: '/companion/settings',
};

const pathToTab: Record<string, string> = {
  '/companion': 'chat',
  '/companion/diary': 'diary',
  '/companion/timeline': 'timeline',
  '/companion/saved': 'saved',
  '/companion/settings': 'settings',
};

export default function CompanionLayout() {
  const navigate = useNavigate();
  const location = useLocation();

  const currentTab = pathToTab[location.pathname] || 'chat';

  const handleTab = (id: string) => {
    const path = tabToPath[id] || '/companion';
    navigate(path);
  };

  return (
    <div
      className={cn(
        'flex flex-col h-dvh overflow-hidden',
        'bg-[#f6f1e7]',
      )}
    >
      {/* Content area */}
      <div className="flex-1 overflow-hidden flex flex-col min-h-0">
        <Outlet />
      </div>

      {/* Bottom tab bar */}
      <BottomTab active={currentTab} onTab={handleTab} />
    </div>
  );
}
