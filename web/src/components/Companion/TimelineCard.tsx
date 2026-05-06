import { cn } from '@/lib/utils';
import { MessageSquare, BookOpen, Bookmark, Zap, Bot, FileText, Brain } from 'lucide-react';
import type { AeliosTimelineEntry, AeliosTimelineType } from '@/api/aelios';

const kindMeta: Record<
  AeliosTimelineType,
  { label: string; color: string; bg: string; icon: React.ElementType }
> = {
  chat_summary: {
    label: 'CHAT',
    color: 'text-[#6b6560]',
    bg: 'bg-[#ede8db]',
    icon: MessageSquare,
  },
  agent_task: {
    label: 'TASK',
    color: 'text-[#6b6560]',
    bg: 'bg-[#ede8db]',
    icon: Bot,
  },
  favorite: {
    label: 'SAVED',
    color: 'text-[#2d6a4f]',
    bg: 'bg-[#edf5f0]',
    icon: Bookmark,
  },
  diary: {
    label: 'DIARY',
    color: 'text-[#e85d3a]',
    bg: 'bg-[#fdf0ec]',
    icon: BookOpen,
  },
  memory_update: {
    label: 'MEMORY',
    color: 'text-[#6b6560]',
    bg: 'bg-[#ede8db]',
    icon: Brain,
  },
  system_event: {
    label: 'EVENT',
    color: 'text-[#6b6560]',
    bg: 'bg-[#ede8db]',
    icon: Zap,
  },
  file_result: {
    label: 'FILE',
    color: 'text-[#6b6560]',
    bg: 'bg-[#ede8db]',
    icon: FileText,
  },
};

function formatTime(iso: string): string {
  try {
    const d = new Date(iso);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  } catch {
    return '';
  }
}

interface TimelineCardProps {
  item: AeliosTimelineEntry;
}

export default function TimelineCard({ item }: TimelineCardProps) {
  const meta = kindMeta[item.type] || kindMeta.system_event;
  const Icon = meta.icon;

  return (
    <div
      className={cn(
        'rounded-xl p-3 mb-2 flex gap-3',
        'bg-[#fffcf5] border border-[#e5dfd5]',
      )}
    >
      <div className="flex-shrink-0 pt-0.5">
        <span className="font-mono text-[11px] font-semibold text-[#1a1915] tracking-wide">
          {formatTime(item.created_at)}
        </span>
      </div>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-1.5 mb-1">
          <span
            className={cn(
              'inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[9px] font-mono font-bold tracking-widest',
              meta.bg,
              meta.color,
            )}
          >
            <Icon size={10} />
            {meta.label}
          </span>
        </div>
        <p className="text-sm leading-relaxed text-[#6b6560]">
          {item.content}
        </p>
        {item.source && (
          <span className="mt-1.5 inline-block text-[10px] font-mono text-[#9e9590] tracking-wider">
            {item.source}
          </span>
        )}
      </div>
    </div>
  );
}
