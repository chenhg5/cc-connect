import { InboxIcon } from 'lucide-react';
import type { ElementType } from 'react';

interface EmptyStateProps {
  message: string;
  icon?: ElementType<{ size?: number; strokeWidth?: number; className?: string }>;
}

export function EmptyState({ message, icon: Icon = InboxIcon }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-gray-400 dark:text-gray-500">
      <Icon size={48} strokeWidth={1} className="mb-4 opacity-80" />
      <p className="text-sm">{message}</p>
    </div>
  );
}
