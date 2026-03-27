import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { FileCode, RefreshCw, RotateCcw, Settings2, ChevronDown, ChevronRight } from 'lucide-react';
import { Card, Button } from '@/components/ui';
import { restartSystem, reloadConfig } from '@/api/status';
import api from '@/api/client';
import GlobalSettings from './GlobalSettings';

export default function SystemConfig() {
  const { t } = useTranslation();
  const [content, setContent] = useState('');
  const [format, setFormat] = useState<'toml' | 'json'>('toml');
  const [loading, setLoading] = useState(true);
  const [actionMsg, setActionMsg] = useState('');
  const [showRaw, setShowRaw] = useState(false);

  const fetchConfig = useCallback(async () => {
    setLoading(true);
    try {
      const text = await api.raw('/config');
      const trimmed = text.trim();
      if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
        try {
          const obj = JSON.parse(trimmed);
          setContent(JSON.stringify(obj, null, 2));
          setFormat('json');
        } catch {
          setContent(text);
          setFormat('toml');
        }
      } else {
        setContent(text);
        setFormat('toml');
      }
    } catch {
      setContent('');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchConfig();
    const handler = () => fetchConfig();
    window.addEventListener('cc:refresh', handler);
    return () => window.removeEventListener('cc:refresh', handler);
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
    <div className="space-y-6 animate-fade-in">
      {/* Actions */}
      <div className="flex flex-wrap gap-3">
        <Button variant="secondary" onClick={handleReload}><RefreshCw size={16} /> {t('system.reload')}</Button>
        <Button variant="danger" onClick={handleRestart}><RotateCcw size={16} /> {t('system.restart')}</Button>
      </div>

      {actionMsg && (
        <div className="text-sm text-accent bg-accent/10 border border-accent/20 rounded-lg px-4 py-2">{actionMsg}</div>
      )}

      {/* Global Settings */}
      <div>
        <div className="flex items-center gap-2 mb-4">
          <Settings2 size={16} className="text-gray-400" />
          <h2 className="text-sm font-semibold text-gray-900 dark:text-white">{t('settings.title', 'Global Settings')}</h2>
        </div>
        <GlobalSettings />
      </div>

      {/* Raw Config (collapsible) */}
      <Card>
        <button
          type="button"
          onClick={() => setShowRaw(!showRaw)}
          className="flex items-center gap-2 w-full text-left"
        >
          {showRaw ? <ChevronDown size={16} className="text-gray-400" /> : <ChevronRight size={16} className="text-gray-400" />}
          <FileCode size={16} className="text-gray-400" />
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white">{t('system.rawConfig', 'Raw Config')}</h3>
          <span className="text-[10px] font-mono text-gray-400 bg-gray-100 dark:bg-gray-800 px-1.5 py-0.5 rounded uppercase">
            {format}
          </span>
        </button>
        {showRaw && (
          <div className="mt-3">
            {loading ? (
              <div className="text-gray-400 animate-pulse text-sm">Loading...</div>
            ) : (
              <pre className="text-xs text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-900/50 rounded-lg p-4 overflow-auto max-h-[65vh] font-mono leading-relaxed whitespace-pre">
                {content || t('common.noData')}
              </pre>
            )}
          </div>
        )}
      </Card>
    </div>
  );
}
