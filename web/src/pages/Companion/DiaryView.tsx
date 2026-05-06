import { useState, useEffect, useCallback } from 'react';
import { Plus, ChevronLeft, Loader2, WifiOff, Trash2, BookOpen, Send, X } from 'lucide-react';
import DiaryCalendar from '@/components/Companion/DiaryCalendar';
import { listDiary, createDiary, deleteDiary, type AeliosDiaryEntry, type AeliosDiaryType } from '@/api/aelios';
import { cn, formatLocalDate } from '@/lib/utils';

type DiarySubView = 'calendar' | 'day';

export default function DiaryView() {
  const [subView, setSubView] = useState<DiarySubView>('calendar');
  const [selectedDate, setSelectedDate] = useState(formatLocalDate());
  const [dayTab, setDayTab] = useState<'work' | 'life'>('work');

  // Entry dates for calendar dots — loaded from all diary entries
  const [entryDates, setEntryDates] = useState<string[]>([]);

  const loadEntryDates = useCallback(async () => {
    try {
      const res = await listDiary();
      const dates = new Set<string>();
      for (const e of res.entries || []) {
        dates.add(e.date);
      }
      setEntryDates(Array.from(dates));
    } catch {
      // silent — calendar just won't show dots
    }
  }, []);

  useEffect(() => { loadEntryDates(); }, [loadEntryDates]);

  const handlePickDate = (date: string) => {
    setSelectedDate(date);
    setSubView('day');
  };

  if (subView === 'day') {
    return (
      <DiaryDayView
        date={selectedDate}
        dayTab={dayTab}
        setDayTab={setDayTab}
        onBack={() => { setSubView('calendar'); loadEntryDates(); }}
      />
    );
  }

  return (
    <div className="flex flex-col h-full bg-[#f6f1e7]">
      {/* Header */}
      <div className="flex-shrink-0 px-5 pt-4 pb-3 flex items-end justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl font-medium text-[#1a1915] tracking-tight leading-none">
            Diary
          </h1>
          <p className="text-[11px] font-mono text-[#9e9590] mt-1 tracking-wider uppercase">
            {entryDates.length} days with entries
          </p>
        </div>
        <button
          onClick={() => handlePickDate(formatLocalDate())}
          className="w-9 h-9 rounded-full border border-[#e5dfd5] bg-[#fffcf5] flex items-center justify-center text-[#6b6560] hover:bg-[#ede8db] transition-colors"
        >
          <Plus size={16} />
        </button>
      </div>

      {/* Calendar */}
      <div className="px-4 pb-4">
        <DiaryCalendar selectedDate={selectedDate} onPickDate={handlePickDate} entryDates={entryDates} />
      </div>

      {/* Hint */}
      <div className="flex-1 flex items-start justify-center pt-8 px-8 text-center">
        <p className="text-xs font-mono text-[#9e9590]">
          Tap a date to view or add entries
        </p>
      </div>
    </div>
  );
}

// ── Day Detail View ───────────────────────────────────────────

function DiaryDayView({
  date,
  dayTab,
  setDayTab,
  onBack,
}: {
  date: string;
  dayTab: 'work' | 'life';
  setDayTab: (tab: 'work' | 'life') => void;
  onBack: () => void;
}) {
  const [entries, setEntries] = useState<AeliosDiaryEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Composer state
  const [composerOpen, setComposerOpen] = useState(false);
  const [newContent, setNewContent] = useState('');
  const [newType, setNewType] = useState<AeliosDiaryType>('work');
  const [submitting, setSubmitting] = useState(false);

  const fetchEntries = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await listDiary({ date });
      setEntries(res.entries || []);
    } catch (e: any) {
      setError(e?.message || 'Failed to load diary');
    } finally {
      setLoading(false);
    }
  }, [date]);

  useEffect(() => { fetchEntries(); }, [fetchEntries]);

  // Filter entries by tab
  const filtered = entries.filter(e => {
    if (dayTab === 'work') return e.type === 'work' || e.type === 'manual' || e.type === 'daily_summary';
    return e.type === 'life';
  });

  // Handle create
  const handleCreate = async () => {
    if (!newContent.trim() || submitting) return;
    setSubmitting(true);
    try {
      const now = new Date();
      const time = `${String(now.getHours()).padStart(2, '0')}:${String(now.getMinutes()).padStart(2, '0')}`;
      const entry = await createDiary({
        type: newType,
        content: newContent.trim(),
        date,
        time,
      });
      setEntries(prev => [...prev, entry]);
      setNewContent('');
      setComposerOpen(false);
    } catch {
      // keep content so user can retry
    } finally {
      setSubmitting(false);
    }
  };

  // Handle delete
  const handleDelete = async (id: string) => {
    setEntries(prev => prev.filter(e => e.id !== id));
    try {
      await deleteDiary(id);
    } catch {
      fetchEntries();
    }
  };

  // Date label
  const dateLabel = (() => {
    try {
      const d = new Date(date + 'T00:00:00');
      return d.toLocaleDateString('en', { month: 'long', day: 'numeric' });
    } catch {
      return date;
    }
  })();

  const weekDay = (() => {
    try {
      const d = new Date(date + 'T00:00:00');
      return d.toLocaleDateString('en', { weekday: 'short' });
    } catch {
      return '';
    }
  })();

  return (
    <div className="flex flex-col h-full bg-[#f6f1e7]">
      {/* Back button */}
      <div className="flex-shrink-0 px-4 pt-3">
        <button
          onClick={onBack}
          className="inline-flex items-center gap-1 text-xs font-mono text-[#9e9590] hover:text-[#6b6560] transition-colors"
        >
          <ChevronLeft size={14} />
          Diary
        </button>
      </div>

      {/* Header */}
      <div className="flex-shrink-0 px-5 pt-2 pb-3 flex items-end justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl font-medium text-[#1a1915] tracking-tight leading-none">
            {dateLabel}
          </h1>
          <p className="text-[11px] font-mono text-[#9e9590] mt-1 tracking-wider uppercase">
            {weekDay} · {date}
          </p>
        </div>
        <button
          onClick={() => setComposerOpen(!composerOpen)}
          className="w-9 h-9 rounded-full border border-[#e5dfd5] bg-[#fffcf5] flex items-center justify-center text-[#6b6560] hover:bg-[#ede8db] transition-colors"
        >
          {composerOpen ? <X size={16} /> : <Plus size={16} />}
        </button>
      </div>

      {/* Segmented control */}
      <div className="px-4 pb-3">
        <div className="flex gap-1 p-0.5 rounded-lg bg-[#ede8db] border border-[#e5dfd5]">
          {(['work', 'life'] as const).map((tab) => (
            <button
              key={tab}
              onClick={() => setDayTab(tab)}
              className={cn(
                'flex-1 py-1.5 rounded-md text-xs font-medium transition-all duration-200',
                dayTab === tab
                  ? 'bg-[#fffcf5] text-[#1a1915] shadow-sm'
                  : 'text-[#9e9590]',
              )}
            >
              {tab === 'work' ? 'Work' : 'Life'}
            </button>
          ))}
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto px-5 pb-4">
        {/* Loading */}
        {loading && (
          <div className="flex flex-col items-center justify-center py-16">
            <Loader2 size={20} className="text-[#9e9590] animate-spin" />
          </div>
        )}

        {/* Error */}
        {!loading && error && (
          <div className="flex flex-col items-center justify-center py-16 px-8 text-center">
            <WifiOff size={24} className="text-[#b5b0a8] mb-2" />
            <p className="text-xs text-[#6b6560]">{error}</p>
            <button onClick={fetchEntries} className="mt-2 text-xs font-mono text-[#e85d3a] hover:underline">
              Retry
            </button>
          </div>
        )}

        {/* Empty */}
        {!loading && !error && filtered.length === 0 && (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <BookOpen size={24} className="text-[#b5b0a8] mb-2" />
            <p className="text-xs text-[#9e9590]">No {dayTab} entries for this day</p>
          </div>
        )}

        {/* Entries */}
        {!loading && !error && filtered.map((entry) => (
          <div key={entry.id} className="flex gap-3.5 pb-4 group">
            <div className="flex-shrink-0 w-10 pt-0.5">
              <span className="text-[11px] font-mono font-semibold text-[#e85d3a] tracking-wide">
                {entry.time || ''}
              </span>
            </div>
            <div className="flex-1 relative">
              <div className="absolute -left-3.5 top-1.5 bottom-0 w-px bg-[#e5dfd5]" />
              <p className="text-sm leading-relaxed text-[#6b6560]">
                {entry.content}
              </p>
            </div>
            <button
              onClick={() => handleDelete(entry.id)}
              className="flex-shrink-0 p-1 rounded text-[#b5b0a8] hover:text-[#e85d3a] opacity-0 group-hover:opacity-100 transition-all"
              title="Delete"
            >
              <Trash2 size={13} />
            </button>
          </div>
        ))}

        {/* Inline composer */}
        {composerOpen && (
          <div className="mt-2 p-3 rounded-xl bg-[#fffcf5] border border-[#e5dfd5]">
            <div className="flex gap-1 mb-2">
              {(['work', 'life', 'manual'] as const).map((t) => (
                <button
                  key={t}
                  onClick={() => setNewType(t)}
                  className={cn(
                    'px-2.5 py-1 rounded-md text-[10px] font-mono font-medium transition-colors',
                    newType === t
                      ? 'bg-[#1a1915] text-[#f6f1e7]'
                      : 'bg-[#ede8db] text-[#6b6560] hover:bg-[#d4cfc5]',
                  )}
                >
                  {t}
                </button>
              ))}
            </div>
            <textarea
              value={newContent}
              onChange={(e) => setNewContent(e.target.value)}
              placeholder="Write something..."
              rows={3}
              className="w-full bg-transparent resize-none outline-none border-0 text-sm text-[#1a1915] placeholder:text-[#b5b0a8] min-h-[60px]"
            />
            <div className="flex justify-end mt-2">
              <button
                onClick={handleCreate}
                disabled={!newContent.trim() || submitting}
                className={cn(
                  'inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors',
                  newContent.trim()
                    ? 'bg-[#1a1915] text-[#f6f1e7]'
                    : 'bg-[#ede8db] text-[#b5b0a8] cursor-not-allowed',
                )}
              >
                {submitting ? <Loader2 size={12} className="animate-spin" /> : <Send size={12} />}
                Save
              </button>
            </div>
          </div>
        )}

        {/* Fallback add button (when composer closed and entries exist) */}
        {!composerOpen && !loading && (
          <button
            onClick={() => setComposerOpen(true)}
            className="mt-1 w-full p-3.5 text-center rounded-xl border border-dashed border-[#d4cfc5] text-xs font-mono text-[#9e9590] hover:bg-[#fffcf5] transition-colors"
          >
            + Add entry
          </button>
        )}
      </div>
    </div>
  );
}
