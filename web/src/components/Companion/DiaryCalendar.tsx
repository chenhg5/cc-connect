import { useState, useMemo } from 'react';
import { ChevronLeft, ChevronRight } from 'lucide-react';
import { cn, formatLocalDate } from '@/lib/utils';

const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

interface DiaryCalendarProps {
  selectedDate: string;
  onPickDate: (date: string) => void;
  /** Dates (YYYY-MM-DD) that have diary entries — shown with a dot. */
  entryDates?: string[];
}

export default function DiaryCalendar({ selectedDate, onPickDate, entryDates = [] }: DiaryCalendarProps) {
  const now = new Date();
  const [viewYear, setViewYear] = useState(now.getFullYear());
  const [viewMonth, setViewMonth] = useState(now.getMonth() + 1); // 1-indexed

  const firstDay = new Date(viewYear, viewMonth - 1, 1).getDay();
  const daysInMonth = new Date(viewYear, viewMonth, 0).getDate();

  const cells: (number | null)[] = [];
  for (let i = 0; i < firstDay; i++) cells.push(null);
  for (let d = 1; d <= daysInMonth; d++) cells.push(d);
  while (cells.length % 7) cells.push(null);

  const todayStr = formatLocalDate();
  const todayDay = todayStr === `${viewYear}-${String(viewMonth).padStart(2, '0')}`
    ? now.getDate()
    : -1;

  const entryDateSet = useMemo(() => new Set(entryDates), [entryDates]);
  const selDay = selectedDate ? parseInt(selectedDate.slice(-2), 10) : 0;

  const monthNames = [
    '', 'January', 'February', 'March', 'April', 'May', 'June',
    'July', 'August', 'September', 'October', 'November', 'December',
  ];

  const prevMonth = () => {
    if (viewMonth === 1) {
      setViewMonth(12);
      setViewYear(viewYear - 1);
    } else {
      setViewMonth(viewMonth - 1);
    }
  };

  const nextMonth = () => {
    if (viewMonth === 12) {
      setViewMonth(1);
      setViewYear(viewYear + 1);
    } else {
      setViewMonth(viewMonth + 1);
    }
  };

  return (
    <div>
      {/* Month navigation */}
      <div className="flex items-center justify-between px-1 pb-3">
        <button
          onClick={prevMonth}
          className="w-8 h-8 rounded-full flex items-center justify-center text-[#6b6560] hover:bg-[#ede8db] transition-colors"
        >
          <ChevronLeft size={18} />
        </button>
        <div className="flex items-baseline gap-1.5">
          <span className="font-serif text-lg text-[#1a1915]">
            {monthNames[viewMonth]}
          </span>
          <span className="text-xs font-mono text-[#9e9590]">
            {viewYear}
          </span>
        </div>
        <button
          onClick={nextMonth}
          className="w-8 h-8 rounded-full flex items-center justify-center text-[#6b6560] hover:bg-[#ede8db] transition-colors"
        >
          <ChevronRight size={18} />
        </button>
      </div>

      {/* Weekday headers */}
      <div className="grid grid-cols-7 gap-1 mb-1">
        {WEEKDAYS.map((d) => (
          <div
            key={d}
            className="text-center text-[10px] font-mono text-[#9e9590] py-1 tracking-wider"
          >
            {d}
          </div>
        ))}
      </div>

      {/* Day grid */}
      <div className="grid grid-cols-7 gap-1">
        {cells.map((d, i) => {
          if (d === null) {
            return <div key={i} className="aspect-square" />;
          }

          const dateStr = `${viewYear}-${String(viewMonth).padStart(2, '0')}-${String(d).padStart(2, '0')}`;
          const isSelected = d === selDay && dateStr === selectedDate;
          const isToday = d === todayDay;
          const has = entryDateSet.has(dateStr);

          return (
            <button
              key={i}
              onClick={() => onPickDate(dateStr)}
              className={cn(
                'aspect-square rounded-lg flex flex-col items-center justify-center transition-all duration-150',
                'text-base font-serif font-medium cursor-pointer hover:bg-[#ede8db]',
                isSelected
                  ? 'bg-[#1a1915] text-[#f6f1e7]'
                  : isToday
                    ? 'bg-[#ede8db] text-[#1a1915]'
                    : 'text-[#1a1915]',
              )}
            >
              {d}
              {has && !isSelected && (
                <span className="w-1 h-1 rounded-full bg-[#e85d3a] mt-0.5" />
              )}
              {has && isSelected && (
                <span className="w-1 h-1 rounded-full bg-[#f6f1e7] mt-0.5" />
              )}
            </button>
          );
        })}
      </div>
    </div>
  );
}
