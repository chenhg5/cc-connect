import { cn } from '@/lib/utils';
import type { ReactNode } from 'react';

interface CardProps {
  children: ReactNode;
  className?: string;
  hover?: boolean;
}

export function Card({ children, className, hover }: CardProps) {
  return (
    <div
      className={cn(
        'rounded-xl p-5 transition-all duration-200 animate-float-in',
        'bg-white/80 backdrop-blur-md border border-gray-200/90',
        'dark:bg-[rgba(0,0,0,0.55)] dark:backdrop-blur-xl dark:border-white/[0.08]',
        hover &&
          'hover:-translate-y-1 hover:shadow-lg hover:shadow-black/5 dark:hover:shadow-black/30 cursor-pointer',
        className
      )}
    >
      {children}
    </div>
  );
}

interface StatCardProps {
  label: string;
  value: string | number;
  accent?: boolean;
}

export function StatCard({ label, value, accent }: StatCardProps) {
  return (
    <Card hover>
      <p className="text-xs font-medium text-gray-500 dark:text-gray-400 uppercase tracking-wide">
        {label}
      </p>
      <p
        className={cn(
          'text-2xl font-bold mt-1',
          accent ? 'text-accent' : 'text-gray-900 dark:text-white'
        )}
      >
        {value}
      </p>
    </Card>
  );
}
