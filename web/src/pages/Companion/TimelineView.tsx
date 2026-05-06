import { useState, useEffect, useCallback } from 'react';
import { Search, Loader2, WifiOff, Clock } from 'lucide-react';
import TimelineCard from '@/components/Companion/TimelineCard';
import { listTimeline, type AeliosTimelineEntry } from '@/api/aelios';
import { cn, formatLocalDate } from '@/lib/utils';

type Granularity = 'day' | 'week' | 'month';

export default function TimelineView() {
  const [gran, setGran] = useState<Granularity>('day');
  const [entries, setEntries] = useState<AeliosTimelineEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchEntries = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const params = gran === 'day' ? { date: formatLocalDate() } : undefined;
      const res = await listTimeline(params);
      setEntries(res.entries || []);
    } catch (e: any) {
      setError(e?.message || 'Failed to load timeline');
    } finally {
      setLoading(false);
    }
  }, [gran]);

  useEffect(() => { fetchEntries(); }, [fetchEntries]);

  // Group entries by date
  const grouped: { date: string; items: AeliosTimelineEntry[] }[] = [];
  const dateMap = new Map<string, AeliosTimelineEntry[]>();
  for (const entry of entries) {
    // Derive display date from date field or created_at
    const rawDate = entry.date || entry.created_at.slice(0, 10);
    let displayDate: string;
    try {
      const d = new Date(rawDate);
      const today = formatLocalDate();
      const yesterday = formatLocalDate(new Date(Date.now() - 86400000));
      if (rawDate === today) displayDate = 'Today';
      else if (rawDate === yesterday) displayDate = 'Yesterday';
      else displayDate = d.toLocaleDateString('en', { month: 'short', day: 'numeric' });
    } catch {
      displayDate = rawDate;
    }
    if (!dateMap.has(displayDate)) dateMap.set(displayDate, []);
    dateMap.get(displayDate)!.push(entry);
  }
  for (const [date, items] of dateMap) {
    grouped.push({ date, items });
  }

  return (
    <div className="flex flex-col h-full bg-[#f6f1e7]">
      {/* Header */}
      <div className="flex-shrink-0 px-5 pt-4 pb-3 flex items-end justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl font-medium text-[#1a1915] tracking-tight leading-none">
            Timeline
          </h1>
          <p className="text-[11px] font-mono text-[#9e9590] mt-1 tracking-wider uppercase">
            {loading ? 'Loading...' : `${entries.length} entries`}
          </p>
        </div>
        <button className="w-9 h-9 rounded-full border border-[#e5dfd5] bg-[#fffcf5] flex items-center justify-center text-[#6b6560] hover:bg-[#ede8db] transition-colors">
          <Search size={16} />
        </button>
      </div>

      {/* Granularity switcher */}
      <div className="px-4 pb-3">
        <div className="flex gap-1 p-0.5 rounded-lg bg-[#ede8db] border border-[#e5dfd5]">
          {([['day', 'Day'], ['week', 'Week'], ['month', 'Month']] as const).map(([id, label]) => (
            <button
              key={id}
              onClick={() => setGran(id)}
              className={cn(
                'flex-1 py-1.5 rounded-md text-[11px] font-medium transition-all duration-200',
                gran === id
                  ? 'bg-[#fffcf5] text-[#1a1915] shadow-sm'
                  : 'text-[#9e9590]',
              )}
            >
              {label}
            </button>
          ))}
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto px-4 pb-4">
        {/* Loading */}
        {loading && (
          <div className="flex flex-col items-center justify-center py-20">
            <Loader2 size={24} className="text-[#9e9590] animate-spin" />
            <p className="mt-3 text-xs font-mono text-[#9e9590]">Loading timeline...</p>
          </div>
        )}

        {/* Error */}
        {!loading && error && (
          <div className="flex flex-col items-center justify-center py-20 px-8 text-center">
            <WifiOff size={28} className="text-[#b5b0a8] mb-3" />
            <p className="text-sm text-[#6b6560]">{error}</p>
            <button
              onClick={fetchEntries}
              className="mt-3 text-xs font-mono text-[#e85d3a] hover:underline"
            >
              Retry
            </button>
          </div>
        )}

        {/* Empty */}
        {!loading && !error && entries.length === 0 && (
          <div className="flex flex-col items-center justify-center py-20 text-center">
            <div className="w-14 h-14 rounded-2xl bg-[#ede8db] border border-[#e5dfd5] flex items-center justify-center mb-4">
              <Clock size={24} className="text-[#b5b0a8]" />
            </div>
            <p className="text-sm text-[#6b6560]">No timeline entries yet</p>
            <p className="text-[11px] font-mono text-[#9e9590] mt-1">
              Events will appear here as they happen
            </p>
          </div>
        )}

        {/* Timeline entries grouped by date */}
        {!loading && !error && grouped.map((group) => (
          <div key={group.date}>
            <div className="sticky top-0 z-10 flex items-baseline gap-2 pt-3 pb-2 bg-[#f6f1e7]">
              <span className="font-serif text-lg font-medium text-[#1a1915]">{group.date}</span>
              <span className="text-[10px] font-mono text-[#9e9590] tracking-wider">
                {group.items.length} entries
              </span>
            </div>
            {group.items.map((item) => (
              <TimelineCard key={item.id} item={item} />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}
