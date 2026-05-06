import { cn } from '@/lib/utils';
import { ExternalLink, Quote, Trash2, Loader2 } from 'lucide-react';
import { useState } from 'react';
import type { AeliosSavedEntry } from '@/api/aelios';

const kindMeta: Record<
  AeliosSavedEntry['type'],
  { label: string; color: string; icon: React.ElementType }
> = {
  link: {
    label: 'LINK',
    color: 'text-[#2d6a4f]',
    icon: ExternalLink,
  },
  text: {
    label: 'TEXT',
    color: 'text-[#e85d3a]',
    icon: Quote,
  },
};

function formatSavedTime(iso: string): string {
  try {
    const d = new Date(iso);
    const now = new Date();
    const diffMs = now.getTime() - d.getTime();
    const diffMins = Math.floor(diffMs / 60000);
    if (diffMins < 1) return 'just now';
    if (diffMins < 60) return `${diffMins}m ago`;
    const diffHours = Math.floor(diffMins / 60);
    if (diffHours < 24) return `${diffHours}h ago`;
    const diffDays = Math.floor(diffHours / 24);
    if (diffDays < 7) return `${diffDays}d ago`;
    return d.toLocaleDateString('en', { month: 'short', day: 'numeric' });
  } catch {
    return iso;
  }
}

interface SavedCardProps {
  item: AeliosSavedEntry;
  onDelete: (id: string) => Promise<void>;
}

export default function SavedCard({ item, onDelete }: SavedCardProps) {
  const meta = kindMeta[item.type] || kindMeta.text;
  const Icon = meta.icon;
  const [deleting, setDeleting] = useState(false);

  const handleDelete = async () => {
    if (deleting) return;
    setDeleting(true);
    try {
      await onDelete(item.id);
    } finally {
      setDeleting(false);
    }
  };

  return (
    <div
      className={cn(
        'rounded-xl p-3 mb-2 flex gap-3 items-start group',
        'bg-[#fffcf5] border border-[#e5dfd5]',
      )}
    >
      <div
        className={cn(
          'flex-shrink-0 w-9 h-9 rounded-lg flex items-center justify-center',
          'bg-[#f6f1e7] border border-[#e5dfd5]',
          meta.color,
        )}
      >
        <Icon size={16} />
      </div>
      <div className="flex-1 min-w-0">
        <p className="text-sm font-medium text-[#1a1915] leading-snug break-words">
          {item.content}
        </p>
        <div className="mt-1 flex items-center gap-2 text-[10px] font-mono text-[#9e9590] tracking-wider">
          <span className={cn('font-bold', meta.color)}>{meta.label}</span>
          {item.source && <span>{item.source}</span>}
          <span>{formatSavedTime(item.created_at)}</span>
        </div>
      </div>
      <button
        onClick={handleDelete}
        disabled={deleting}
        className={cn(
          'flex-shrink-0 p-1.5 rounded-lg transition-all',
          'text-[#b5b0a8] hover:text-[#e85d3a] hover:bg-[#fdf0ec]',
          'opacity-0 group-hover:opacity-100 focus:opacity-100',
        )}
        title="Delete"
      >
        {deleting ? <Loader2 size={14} className="animate-spin" /> : <Trash2 size={14} />}
      </button>
    </div>
  );
}
