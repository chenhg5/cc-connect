import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { ArrowLeft, Filter } from 'lucide-react';
import { Card, Button, Badge } from '@/components/ui';
import { getLogs } from '@/api/status';

const levelColors: Record<string, string> = {
  debug: 'text-gray-400',
  info: 'text-blue-400',
  warn: 'text-amber-400',
  error: 'text-red-400',
};

const levelBadge: Record<string, 'default' | 'info' | 'warning' | 'danger'> = {
  debug: 'default',
  info: 'info',
  warn: 'warning',
  error: 'danger',
};

export default function SystemLogs() {
  const { t } = useTranslation();
  const [entries, setEntries] = useState<any[]>([]);
  const [level, setLevel] = useState('info');
  const [limit, setLimit] = useState('100');
  const [loading, setLoading] = useState(true);

  const fetchLogs = useCallback(async () => {
    setLoading(true);
    try {
      const data = await getLogs({ level, limit });
      setEntries(data.entries || []);
    } finally {
      setLoading(false);
    }
  }, [level, limit]);

  useEffect(() => {
    fetchLogs();
  }, [fetchLogs]);

  return (
    <div className="space-y-4 animate-fade-in">
      <div className="flex items-center gap-3">
        <Link to="/system" className="p-2 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-800 transition-colors">
          <ArrowLeft size={18} className="text-gray-400" />
        </Link>
        <h2 className="text-lg font-semibold text-gray-900 dark:text-white">{t('system.logs')}</h2>
      </div>

      {/* Filters */}
      <div className="flex items-center gap-3">
        <div className="flex items-center gap-2">
          <Filter size={14} className="text-gray-400" />
          <select
            value={level}
            onChange={(e) => setLevel(e.target.value)}
            className="px-3 py-1.5 text-sm rounded-lg border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-800 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/50"
          >
            <option value="debug">Debug</option>
            <option value="info">Info</option>
            <option value="warn">Warn</option>
            <option value="error">Error</option>
          </select>
        </div>
        <select
          value={limit}
          onChange={(e) => setLimit(e.target.value)}
          className="px-3 py-1.5 text-sm rounded-lg border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-800 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/50"
        >
          <option value="50">50</option>
          <option value="100">100</option>
          <option value="500">500</option>
          <option value="1000">1000</option>
        </select>
        <Button size="sm" variant="secondary" onClick={fetchLogs}>{t('common.refresh')}</Button>
      </div>

      {/* Log entries */}
      <Card>
        {loading ? (
          <div className="text-gray-400 animate-pulse text-sm">Loading...</div>
        ) : entries.length === 0 ? (
          <p className="text-sm text-gray-500 text-center py-8">{t('common.noData')}</p>
        ) : (
          <div className="space-y-1 max-h-[65vh] overflow-y-auto font-mono text-xs">
            {entries.map((entry, i) => (
              <div key={i} className="flex items-start gap-3 py-1.5 border-b border-gray-100 dark:border-gray-800/50 last:border-0">
                <span className="text-gray-400 shrink-0 w-36">{entry.time?.slice(0, 19)}</span>
                <Badge variant={levelBadge[entry.level] || 'default'}>{entry.level}</Badge>
                <span className={`${levelColors[entry.level] || 'text-gray-500'} flex-1`}>{entry.message}</span>
              </div>
            ))}
          </div>
        )}
      </Card>
    </div>
  );
}
