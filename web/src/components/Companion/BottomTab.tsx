import { MessageSquare, BookOpen, Clock, Bookmark, Settings } from 'lucide-react';
import { cn } from '@/lib/utils';

interface TabItem {
  id: string;
  label: string;
  icon: React.ElementType;
}

const tabs: TabItem[] = [
  { id: 'chat', label: 'Chat', icon: MessageSquare },
  { id: 'diary', label: 'Diary', icon: BookOpen },
  { id: 'timeline', label: 'Timeline', icon: Clock },
  { id: 'saved', label: 'Saved', icon: Bookmark },
  { id: 'settings', label: 'Settings', icon: Settings },
];

interface BottomTabProps {
  active: string;
  onTab: (id: string) => void;
}

export default function BottomTab({ active, onTab }: BottomTabProps) {
  return (
    <nav
      className={cn(
        'flex-shrink-0 border-t backdrop-blur-md',
        'bg-[#f6f1e7]/90 border-[#e5dfd5]',
      )}
    >
      <div className="grid grid-cols-5">
        {tabs.map((tab) => {
          const Icon = tab.icon;
          const isActive = active === tab.id;
          return (
            <button
              key={tab.id}
              onClick={() => onTab(tab.id)}
              className={cn(
                'flex flex-col items-center gap-1 py-2 px-1 transition-colors duration-200',
                isActive ? 'text-[#1a1915]' : 'text-[#9e9590]',
              )}
            >
              <Icon
                size={20}
                strokeWidth={isActive ? 2 : 1.5}
                className="transition-all duration-200"
              />
              <span
                className={cn(
                  'text-[10px] tracking-wide',
                  isActive ? 'font-semibold' : 'font-medium',
                )}
              >
                {tab.label}
              </span>
            </button>
          );
        })}
      </div>
    </nav>
  );
}
