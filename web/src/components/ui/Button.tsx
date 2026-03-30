import { cn } from '@/lib/utils';
import type { ButtonHTMLAttributes, ReactNode } from 'react';

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'danger' | 'ghost';
  size?: 'sm' | 'md' | 'lg';
  children: ReactNode;
  loading?: boolean;
}

const variants = {
  primary: 'bg-accent text-white dark:text-black hover:bg-accent-dim font-medium dark:shadow-[0_0_20px_-6px_rgba(66,255,156,0.55)]',
  secondary:
    'bg-gray-100/90 dark:bg-white/[0.08] text-gray-700 dark:text-gray-300 hover:bg-gray-200/90 dark:hover:bg-white/[0.12] border border-transparent dark:border-white/[0.06]',
  danger: 'bg-red-500/10 text-red-600 dark:text-red-400 hover:bg-red-500/20',
  ghost:
    'text-gray-500 dark:text-gray-400 hover:bg-gray-100/80 dark:hover:bg-white/[0.06] hover:text-gray-700 dark:hover:text-gray-200',
};

const sizes = {
  sm: 'px-3 py-1.5 text-xs rounded-lg',
  md: 'px-4 py-2 text-sm rounded-lg',
  lg: 'px-6 py-2.5 text-sm rounded-xl',
};

export function Button({
  variant = 'primary',
  size = 'md',
  className,
  children,
  loading,
  disabled,
  ...props
}: ButtonProps) {
  return (
    <button
      className={cn(
        'inline-flex items-center justify-center gap-2 font-medium transition-all duration-200 disabled:opacity-50 disabled:pointer-events-none',
        variants[variant],
        sizes[size],
        className
      )}
      disabled={disabled || loading}
      {...props}
    >
      {loading && (
        <svg className="animate-spin h-4 w-4" viewBox="0 0 24 24" aria-hidden>
          <circle
            className="opacity-25"
            cx="12"
            cy="12"
            r="10"
            stroke="currentColor"
            strokeWidth="4"
            fill="none"
          />
          <path
            className="opacity-75"
            fill="currentColor"
            d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
          />
        </svg>
      )}
      {children}
    </button>
  );
}
