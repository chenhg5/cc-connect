import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Cable, Wifi } from 'lucide-react';
import { Card, Badge, EmptyState } from '@/components/ui';
import { listBridgeAdapters, type BridgeAdapter } from '@/api/bridge';
import { formatTime } from '@/lib/utils';

export default function BridgeAdapters() {
  const { t } = useTranslation();
  const [adapters, setAdapters] = useState<BridgeAdapter[]>([]);
  const [loading, setLoading] = useState(true);

  const fetchData = useCallback(async () => {
    setLoading(true);
    try {
      const data = await listBridgeAdapters();
      setAdapters(data.adapters || []);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
    const handler = () => fetchData();
    window.addEventListener('cc:refresh', handler);
    return () => window.removeEventListener('cc:refresh', handler);
  }, [fetchData]);

  if (loading && adapters.length === 0) {
    return <div className="flex items-center justify-center h-64 text-gray-400 animate-pulse">Loading...</div>;
  }

  if (adapters.length === 0) {
    return <EmptyState message={t('bridge.noAdapters')} icon={Cable} />;
  }

  return (
    <div className="space-y-4 animate-fade-in">
      {adapters.map((a, i) => (
        <Card key={i}>
          <div className="flex items-start justify-between">
            <div className="flex items-center gap-3">
              <div className="w-10 h-10 rounded-lg bg-blue-100 dark:bg-blue-900/30 flex items-center justify-center">
                <Wifi size={20} className="text-blue-500" />
              </div>
              <div>
                <div className="flex items-center gap-2">
                  <span className="font-medium text-gray-900 dark:text-white">{a.platform}</span>
                  <Badge variant="info">{a.project}</Badge>
                </div>
                <div className="flex flex-wrap gap-1 mt-1">
                  {a.capabilities?.map((c) => <Badge key={c} variant="default">{c}</Badge>)}
                </div>
              </div>
            </div>
            <span className="text-xs text-gray-400">{formatTime(a.connected_at)}</span>
          </div>
        </Card>
      ))}
    </div>
  );
}
