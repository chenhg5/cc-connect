import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useParams, Link } from 'react-router-dom';
import {
  ArrowLeft, Plug, Heart, Settings, Layers, Zap, Pause, Play,
  Trash2, Plus, Check, Clock,
} from 'lucide-react';
import { Card, Badge, Button, Input, Modal, EmptyState } from '@/components/ui';
import { getProject, updateProject, type ProjectDetail as ProjectDetailType } from '@/api/projects';
import { listProviders, addProvider, removeProvider, activateProvider, listModels, setModel, type Provider } from '@/api/providers';
import { getHeartbeat, pauseHeartbeat, resumeHeartbeat, triggerHeartbeat, setHeartbeatInterval, type HeartbeatStatus } from '@/api/heartbeat';
import { formatTime } from '@/lib/utils';
import { cn } from '@/lib/utils';

type Tab = 'overview' | 'providers' | 'heartbeat' | 'settings';

export default function ProjectDetail() {
  const { t } = useTranslation();
  const { name } = useParams<{ name: string }>();
  const [tab, setTab] = useState<Tab>('overview');
  const [project, setProject] = useState<ProjectDetailType | null>(null);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [activeProvider, setActiveProvider] = useState('');
  const [heartbeat, setHeartbeatState] = useState<HeartbeatStatus | null>(null);
  const [models, setModels] = useState<string[]>([]);
  const [currentModel, setCurrentModel] = useState('');
  const [loading, setLoading] = useState(true);

  // Settings form
  const [quiet, setQuiet] = useState(false);
  const [language, setLanguage] = useState('');
  const [adminFrom, setAdminFrom] = useState('');
  const [disabledCmds, setDisabledCmds] = useState('');
  const [saving, setSaving] = useState(false);

  // Add provider modal
  const [showAddProvider, setShowAddProvider] = useState(false);
  const [newProvider, setNewProvider] = useState({ name: '', api_key: '', base_url: '', model: '' });

  // Interval modal
  const [showInterval, setShowInterval] = useState(false);
  const [newInterval, setNewInterval] = useState('30');

  const fetchAll = useCallback(async () => {
    if (!name) return;
    try {
      setLoading(true);
      const [proj, provs, hb, mdls] = await Promise.allSettled([
        getProject(name),
        listProviders(name),
        getHeartbeat(name),
        listModels(name),
      ]);
      if (proj.status === 'fulfilled') {
        setProject(proj.value);
        setQuiet(proj.value.settings?.quiet || false);
        setLanguage(proj.value.settings?.language || '');
        setAdminFrom(proj.value.settings?.admin_from || '');
        setDisabledCmds(proj.value.settings?.disabled_commands?.join(', ') || '');
      }
      if (provs.status === 'fulfilled') {
        setProviders(provs.value.providers || []);
        setActiveProvider(provs.value.active_provider || '');
      }
      if (hb.status === 'fulfilled') setHeartbeatState(hb.value);
      if (mdls.status === 'fulfilled') {
        setModels(mdls.value.models || []);
        setCurrentModel(mdls.value.current || '');
      }
    } finally {
      setLoading(false);
    }
  }, [name]);

  useEffect(() => {
    fetchAll();
    const handler = () => fetchAll();
    window.addEventListener('cc:refresh', handler);
    return () => window.removeEventListener('cc:refresh', handler);
  }, [fetchAll]);

  const handleSaveSettings = async () => {
    if (!name) return;
    setSaving(true);
    try {
      await updateProject(name, {
        quiet,
        language,
        admin_from: adminFrom,
        disabled_commands: disabledCmds.split(',').map(s => s.trim()).filter(Boolean),
      });
      await fetchAll();
    } finally {
      setSaving(false);
    }
  };

  const handleAddProvider = async () => {
    if (!name || !newProvider.name) return;
    await addProvider(name, newProvider);
    setShowAddProvider(false);
    setNewProvider({ name: '', api_key: '', base_url: '', model: '' });
    fetchAll();
  };

  const handleSetInterval = async () => {
    if (!name) return;
    await setHeartbeatInterval(name, parseInt(newInterval));
    setShowInterval(false);
    fetchAll();
  };

  const tabs: { key: Tab; icon: React.ElementType }[] = [
    { key: 'overview', icon: Layers },
    { key: 'providers', icon: Zap },
    { key: 'heartbeat', icon: Heart },
    { key: 'settings', icon: Settings },
  ];

  if (loading && !project) {
    return <div className="flex items-center justify-center h-64 text-gray-400 animate-pulse">Loading...</div>;
  }

  return (
    <div className="space-y-6 animate-fade-in">
      {/* Back + title */}
      <div className="flex items-center gap-3">
        <Link to="/projects" className="p-2 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-800 transition-colors">
          <ArrowLeft size={18} className="text-gray-400" />
        </Link>
        <h2 className="text-xl font-bold text-gray-900 dark:text-white">{name}</h2>
        {project && <Badge variant="info">{project.agent_type}</Badge>}
      </div>

      {/* Tabs */}
      <div className="flex gap-2">
        {tabs.map(({ key, icon: Icon }) => (
          <button
            key={key}
            onClick={() => setTab(key)}
            className={cn(
              'flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-all',
              tab === key
                ? 'bg-gray-900 dark:bg-gray-700 text-white shadow-md'
                : 'bg-gray-100 dark:bg-gray-800 text-gray-500 dark:text-gray-400 hover:bg-gray-200 dark:hover:bg-gray-700'
            )}
          >
            <Icon size={16} />
            {t(`projects.tabs.${key}`)}
          </button>
        ))}
      </div>

      {/* Tab content */}
      {tab === 'overview' && project && (
        <div className="space-y-4">
          <Card>
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-3">{t('projects.platforms')}</h3>
            <div className="flex flex-wrap gap-2">
              {project.platforms?.map((p) => (
                <Badge key={p.type} variant={p.connected ? 'success' : 'danger'}>
                  <Plug size={12} className="mr-1" /> {p.type} {p.connected ? '✓' : '✗'}
                </Badge>
              ))}
            </div>
          </Card>
          <Card>
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-3">{t('sessions.title')}</h3>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              {project.sessions_count} {t('nav.sessions').toLowerCase()}
            </p>
            {project.active_session_keys?.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1">
                {project.active_session_keys.map((k) => (
                  <Badge key={k} variant="default">{k}</Badge>
                ))}
              </div>
            )}
          </Card>
        </div>
      )}

      {tab === 'providers' && (
        <div className="space-y-4">
          <div className="flex justify-between items-center">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">{t('providers.title')}</h3>
            <Button size="sm" onClick={() => setShowAddProvider(true)}><Plus size={14} /> {t('providers.add')}</Button>
          </div>
          {providers.length === 0 ? (
            <EmptyState message={t('common.noData')} />
          ) : (
            <div className="space-y-2">
              {providers.map((p) => (
                <Card key={p.name}>
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="flex items-center gap-2">
                        <span className="font-medium text-gray-900 dark:text-white">{p.name}</span>
                        {p.active && <Badge variant="success">{t('providers.active')}</Badge>}
                      </div>
                      <p className="text-xs text-gray-500 mt-1">{p.model} {p.base_url ? `· ${p.base_url}` : ''}</p>
                    </div>
                    <div className="flex gap-2">
                      {!p.active && (
                        <Button size="sm" variant="secondary" onClick={() => { activateProvider(name!, p.name).then(fetchAll); }}>
                          <Check size={14} /> {t('providers.activate')}
                        </Button>
                      )}
                      {!p.active && (
                        <Button size="sm" variant="danger" onClick={() => { removeProvider(name!, p.name).then(fetchAll); }}>
                          <Trash2 size={14} />
                        </Button>
                      )}
                    </div>
                  </div>
                </Card>
              ))}
            </div>
          )}

          {/* Models */}
          {models.length > 0 && (
            <Card>
              <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-3">{t('providers.models')}</h3>
              <div className="flex flex-wrap gap-2">
                {models.map((m) => (
                  <button
                    key={m}
                    onClick={() => { setModel(name!, m).then(fetchAll); }}
                    className={cn(
                      'px-3 py-1.5 rounded-lg text-xs font-medium transition-all',
                      m === currentModel
                        ? 'bg-accent/20 text-accent border border-accent/30'
                        : 'bg-gray-100 dark:bg-gray-800 text-gray-600 dark:text-gray-400 hover:bg-gray-200 dark:hover:bg-gray-700'
                    )}
                  >
                    {m}
                  </button>
                ))}
              </div>
            </Card>
          )}

          {/* Add Provider Modal */}
          <Modal open={showAddProvider} onClose={() => setShowAddProvider(false)} title={t('providers.add')}>
            <div className="space-y-3">
              <Input label={t('providers.name')} value={newProvider.name} onChange={(e) => setNewProvider({...newProvider, name: e.target.value})} />
              <Input label="API Key" type="password" value={newProvider.api_key} onChange={(e) => setNewProvider({...newProvider, api_key: e.target.value})} />
              <Input label={t('providers.baseUrl')} value={newProvider.base_url} onChange={(e) => setNewProvider({...newProvider, base_url: e.target.value})} placeholder="https://api.example.com" />
              <Input label={t('providers.model')} value={newProvider.model} onChange={(e) => setNewProvider({...newProvider, model: e.target.value})} />
              <div className="flex justify-end gap-2 pt-2">
                <Button variant="secondary" onClick={() => setShowAddProvider(false)}>{t('common.cancel')}</Button>
                <Button onClick={handleAddProvider}>{t('providers.add')}</Button>
              </div>
            </div>
          </Modal>
        </div>
      )}

      {tab === 'heartbeat' && (
        <div className="space-y-4">
          {!heartbeat ? (
            <EmptyState message={t('common.noData')} />
          ) : (
            <>
              <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
                <Card><p className="text-xs text-gray-500">{t('heartbeat.status')}</p><p className="text-lg font-bold text-gray-900 dark:text-white mt-1">{heartbeat.paused ? t('heartbeat.paused') : t('heartbeat.running')}</p></Card>
                <Card><p className="text-xs text-gray-500">{t('heartbeat.interval')}</p><p className="text-lg font-bold text-gray-900 dark:text-white mt-1">{heartbeat.interval_mins}m</p></Card>
                <Card><p className="text-xs text-gray-500">{t('heartbeat.runCount')}</p><p className="text-lg font-bold text-gray-900 dark:text-white mt-1">{heartbeat.run_count}</p></Card>
                <Card><p className="text-xs text-gray-500">{t('heartbeat.errorCount')}</p><p className="text-lg font-bold text-gray-900 dark:text-white mt-1">{heartbeat.error_count}</p></Card>
              </div>
              <Card>
                <div className="space-y-2 text-sm">
                  <p className="text-gray-500">{t('heartbeat.lastRun')}: <span className="text-gray-900 dark:text-white">{formatTime(heartbeat.last_run)}</span></p>
                  <p className="text-gray-500">{t('heartbeat.skippedBusy')}: <span className="text-gray-900 dark:text-white">{heartbeat.skipped_busy}</span></p>
                  {heartbeat.last_error && <p className="text-red-500">{heartbeat.last_error}</p>}
                </div>
              </Card>
              <div className="flex gap-2">
                {heartbeat.paused ? (
                  <Button onClick={() => { resumeHeartbeat(name!).then(fetchAll); }}><Play size={14} /> {t('heartbeat.resume')}</Button>
                ) : (
                  <Button variant="secondary" onClick={() => { pauseHeartbeat(name!).then(fetchAll); }}><Pause size={14} /> {t('heartbeat.pause')}</Button>
                )}
                <Button variant="secondary" onClick={() => { triggerHeartbeat(name!).then(fetchAll); }}><Heart size={14} /> {t('heartbeat.trigger')}</Button>
                <Button variant="secondary" onClick={() => setShowInterval(true)}><Clock size={14} /> {t('heartbeat.setInterval')}</Button>
              </div>
            </>
          )}
          <Modal open={showInterval} onClose={() => setShowInterval(false)} title={t('heartbeat.setInterval')}>
            <div className="space-y-3">
              <Input label={`${t('heartbeat.interval')} (min)`} type="number" value={newInterval} onChange={(e) => setNewInterval(e.target.value)} />
              <div className="flex justify-end gap-2 pt-2">
                <Button variant="secondary" onClick={() => setShowInterval(false)}>{t('common.cancel')}</Button>
                <Button onClick={handleSetInterval}>{t('common.save')}</Button>
              </div>
            </div>
          </Modal>
        </div>
      )}

      {tab === 'settings' && project && (
        <Card>
          <div className="space-y-4 max-w-lg">
            <div className="flex items-center justify-between">
              <label className="text-sm font-medium text-gray-700 dark:text-gray-300">{t('projects.quiet')}</label>
              <button
                onClick={() => setQuiet(!quiet)}
                className={cn('w-10 h-6 rounded-full transition-colors', quiet ? 'bg-accent' : 'bg-gray-300 dark:bg-gray-700')}
              >
                <div className={cn('w-4 h-4 bg-white rounded-full transition-transform mx-1', quiet ? 'translate-x-4' : 'translate-x-0')} />
              </button>
            </div>
            <Input label={t('projects.language')} value={language} onChange={(e) => setLanguage(e.target.value)} placeholder="en, zh, ja..." />
            <Input label={t('projects.adminFrom')} value={adminFrom} onChange={(e) => setAdminFrom(e.target.value)} placeholder="user1,user2 or *" />
            <Input label={t('projects.disabledCommands')} value={disabledCmds} onChange={(e) => setDisabledCmds(e.target.value)} placeholder="restart, upgrade, cron" />
            <Button loading={saving} onClick={handleSaveSettings}>{t('common.save')}</Button>
          </div>
        </Card>
      )}
    </div>
  );
}
