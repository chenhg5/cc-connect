import { useState, useEffect, useCallback } from 'react';
import { Search, Loader2, WifiOff, Bookmark } from 'lucide-react';
import SavedCard from '@/components/Companion/SavedCard';
import { listSaved, deleteSaved, type AeliosSavedEntry } from '@/api/aelios';
import { cn } from '@/lib/utils';

export default function SavedView() {
  const [entries, setEntries] = useState<AeliosSavedEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchEntries = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await listSaved();
      setEntries(res.entries || []);
    } catch (e: any) {
      setError(e?.message || 'Failed to load saved items');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { fetchEntries(); }, [fetchEntries]);

  const handleDelete = useCallback(async (id: string) => {
    // Optimistic remove
    setEntries(prev => prev.filter(e => e.id !== id));
    try {
      await deleteSaved(id);
    } catch {
      // Re-fetch to restore if delete failed
      fetchEntries();
    }
  }, [fetchEntries]);

  // Group entries by month
  const grouped: { month: string; items: AeliosSavedEntry[] }[] = [];
  const monthMap = new Map<string, AeliosSavedEntry[]>();
  for (const entry of entries) {
    try {
      const d = new Date(entry.created_at);
      const key = `${d.getFullYear()} · ${d.toLocaleString('en', { month: 'long' })}`;
      if (!monthMap.has(key)) monthMap.set(key, []);
      monthMap.get(key)!.push(entry);
    } catch {
      const key = 'Unknown';
      if (!monthMap.has(key)) monthMap.set(key, []);
      monthMap.get(key)!.push(entry);
    }
  }
  for (const [month, items] of monthMap) {
    grouped.push({ month, items });
  }

  return (
    <div className="flex flex-col h-full bg-[#f6f1e7]">
      {/* Header */}
      <div className="flex-shrink-0 px-5 pt-4 pb-3 flex items-end justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl font-medium text-[#1a1915] tracking-tight leading-none">
            Saved
          </h1>
          <p className="text-[11px] font-mono text-[#9e9590] mt-1 tracking-wider uppercase">
            {loading ? 'Loading...' : `${entries.length} items`}
          </p>
        </div>
        <button className="w-9 h-9 rounded-full border border-[#e5dfd5] bg-[#fffcf5] flex items-center justify-center text-[#6b6560] hover:bg-[#ede8db] transition-colors">
          <Search size={16} />
        </button>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto px-4 pb-4">
        {/* Loading */}
        {loading && (
          <div className="flex flex-col items-center justify-center py-20">
            <Loader2 size={24} className="text-[#9e9590] animate-spin" />
            <p className="mt-3 text-xs font-mono text-[#9e9590]">Loading saved items...</p>
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
              <Bookmark size={24} className="text-[#b5b0a8]" />
            </div>
            <p className="text-sm text-[#6b6560]">No saved items yet</p>
            <p className="text-[11px] font-mono text-[#9e9590] mt-1">
              Bookmark messages from Chat to save them here
            </p>
          </div>
        )}

        {/* Entries grouped by month */}
        {!loading && !error && grouped.map((group) => (
          <div key={group.month}>
            <div className="flex items-baseline gap-2 pt-3 pb-2">
              <span className="font-serif text-lg font-medium text-[#1a1915]">
                {group.month}
              </span>
              <span className="text-[10px] font-mono text-[#9e9590] tracking-wider">
                {group.items.length} items
              </span>
            </div>
            {group.items.map((item) => (
              <SavedCard key={item.id} item={item} onDelete={handleDelete} />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}
