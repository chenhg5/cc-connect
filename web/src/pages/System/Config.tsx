import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { FileCode, RefreshCw, RotateCcw, ScrollText } from 'lucide-react';
import { Card, Button } from '@/components/ui';
import { getConfig, restartSystem, reloadConfig } from '@/api/status';

export default function SystemConfig() {
  const { t } = useTranslation();
  const [config, setConfig] = useState<any>(null);
  const [loading, setLoading] = useState(true);
  const [actionMsg, setActionMsg] = useState('');

  const fetchConfig = useCallback(async () => {
    setLoading(true);
    try {
      const data = await getConfig();
      setConfig(data);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchConfig();
  }, [fetchConfig]);

  const handleRestart = async () => {
    if (!confirm(t('system.restartConfirm'))) return;
    try {
      await restartSystem();
      setActionMsg(t('common.success'));
    } catch (e: any) {
      setActionMsg(e.message);
    }
  };

  const handleReload = async () => {
    if (!confirm(t('system.reloadConfirm'))) return;
    try {
      await reloadConfig();
      setActionMsg(t('common.success'));
      fetchConfig();
    } catch (e: any) {
      setActionMsg(e.message);
    }
  };

  return (
    <div className="space-y-4 animate-fade-in">
      {/* Actions */}
      <div className="flex flex-wrap gap-3">
        <Button variant="secondary" onClick={handleReload}><RefreshCw size={16} /> {t('system.reload')}</Button>
        <Button variant="danger" onClick={handleRestart}><RotateCcw size={16} /> {t('system.restart')}</Button>
        <Link to="/system/logs">
          <Button variant="secondary"><ScrollText size={16} /> {t('system.logs')}</Button>
        </Link>
      </div>

      {actionMsg && (
        <div className="text-sm text-accent bg-accent/10 border border-accent/20 rounded-lg px-4 py-2">{actionMsg}</div>
      )}

      {/* Config */}
      <Card>
        <div className="flex items-center gap-2 mb-3">
          <FileCode size={16} className="text-gray-400" />
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white">{t('system.config')}</h3>
        </div>
        {loading ? (
          <div className="text-gray-400 animate-pulse text-sm">Loading...</div>
        ) : (
          <pre className="text-xs text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800/50 rounded-lg p-4 overflow-auto max-h-[60vh] font-mono">
            {JSON.stringify(config, null, 2)}
          </pre>
        )}
      </Card>
    </div>
  );
}
