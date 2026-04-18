import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useParams, Link, useNavigate } from 'react-router-dom';
import {
  ArrowLeft, Plug, Heart, Settings, Layers, Zap, Pause, Play,
  Trash2, Plus, Check, Clock, ExternalLink, Link2,
} from 'lucide-react';
import { Card, Badge, Button, Input, Modal, EmptyState } from '@/components/ui';
import { getProject, updateProject, deleteProject, type ProjectDetail as ProjectDetailType } from '@/api/projects';
import { listProviders, addProvider, removeProvider, activateProvider, type Provider, listGlobalProviders, type GlobalProvider, saveProviderRefs } from '@/api/providers';
import { getHeartbeat, pauseHeartbeat, resumeHeartbeat, triggerHeartbeat, setHeartbeatInterval, type HeartbeatStatus } from '@/api/heartbeat';
import { restartSystem } from '@/api/status';
import { formatTime, cn } from '@/lib/utils';
import PlatformSetupQR from './PlatformSetupQR';
import PlatformManualForm from './PlatformManualForm';
import { platformMeta } from '@/lib/platformMeta';

const PLATFORM_OPTIONS: { key: string; label: string; color: string; abbr: string; qr?: boolean }[] = [
  { key: 'feishu', label: 'Feishu / Lark', abbr: 'FS', color: 'bg-blue-50 dark:bg-blue-900/30 text-blue-600 dark:text-blue-400', qr: true },
  { key: 'weixin', label: 'WeChat', abbr: 'WX', color: 'bg-green-50 dark:bg-green-900/30 text-green-600 dark:text-green-400', qr: true },
  { key: 'telegram', label: 'Telegram', abbr: 'TG', color: 'bg-sky-50 dark:bg-sky-900/30 text-sky-600 dark:text-sky-400' },
  { key: 'discord', label: 'Discord', abbr: 'DC', color: 'bg-indigo-50 dark:bg-indigo-900/30 text-indigo-600 dark:text-indigo-400' },
  { key: 'slack', label: 'Slack', abbr: 'SK', color: 'bg-purple-50 dark:bg-purple-900/30 text-purple-600 dark:text-purple-400' },
  { key: 'dingtalk', label: 'DingTalk', abbr: 'DT', color: 'bg-orange-50 dark:bg-orange-900/30 text-orange-600 dark:text-orange-400' },
  { key: 'wecom', label: 'WeChat Work', abbr: 'WC', color: 'bg-emerald-50 dark:bg-emerald-900/30 text-emerald-600 dark:text-emerald-400' },
  { key: 'qq', label: 'QQ (OneBot)', abbr: 'QQ', color: 'bg-cyan-50 dark:bg-cyan-900/30 text-cyan-600 dark:text-cyan-400' },
  { key: 'qqbot', label: 'QQ Bot (Official)', abbr: 'QB', color: 'bg-cyan-50 dark:bg-cyan-900/30 text-cyan-600 dark:text-cyan-400' },
  { key: 'line', label: 'LINE', abbr: 'LN', color: 'bg-lime-50 dark:bg-lime-900/30 text-lime-600 dark:text-lime-400' },
  { key: 'weibo', label: 'Weibo (微博)', abbr: 'WB', color: 'bg-red-50 dark:bg-red-900/30 text-red-600 dark:text-red-400' },
];

const isQRPlatform = (type: string) => type === 'feishu' || type === 'lark' || type === 'weixin';

type Tab = 'overview' | 'providers' | 'heartbeat' | 'settings';

export default function ProjectDetail() {
  const { t } = useTranslation();
  const { name } = useParams<{ name: string }>();
  const [tab, setTab] = useState<Tab>('overview');
  const [project, setProject] = useState<ProjectDetailType | null>(null);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [activeProvider, setActiveProvider] = useState('');
  const [heartbeat, setHeartbeatState] = useState<HeartbeatStatus | null>(null);
  const [loading, setLoading] = useState(true);

  // Settings form
  const [language, setLanguage] = useState('');
  const [adminFrom, setAdminFrom] = useState('');
  const [disabledCmds, setDisabledCmds] = useState('');
  const [workDir, setWorkDir] = useState('');
  const [agentMode, setAgentMode] = useState('');
  const [showCtxIndicator, setShowCtxIndicator] = useState(true);
  const [replyFooter, setReplyFooter] = useState(true);
  const [injectSender, setInjectSender] = useState(false);
  const [platformAllowFrom, setPlatformAllowFrom] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);

  // Global providers & refs
  const [globalProviders, setGlobalProviders] = useState<GlobalProvider[]>([]);
  const [providerRefs, setProviderRefs] = useState<string[]>([]);
  const [savingRefs, setSavingRefs] = useState(false);

  // Add provider modal
  const [showAddProvider, setShowAddProvider] = useState(false);
  const [addMode, setAddMode] = useState<'pick' | 'custom'>('pick');
  const [newProvider, setNewProvider] = useState({ name: '', api_key: '', base_url: '', model: '' });

  // Interval modal
  const [showInterval, setShowInterval] = useState(false);
  const [newInterval, setNewInterval] = useState('30');

  // Add platform
  const [showAddPlatform, setShowAddPlatform] = useState(false);
  const [addPlatType, setAddPlatType] = useState('');
  const [showRestartModal, setShowRestartModal] = useState(false);

  // Delete project
  const navigate = useNavigate();
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const handleDeleteProject = async () => {
    if (!name) return;
    setDeleting(true);
    try {
      const res = await deleteProject(name);
      setShowDeleteConfirm(false);
      if (res.restart_required && window.confirm(t('setup.restartAfterDelete'))) {
        await restartSystem();
        // Wait for service to come back up before navigating
        await waitForService(8000);
      }
      navigate('/projects');
    } catch (e: any) {
      alert(e?.message || String(e));
    } finally {
      setDeleting(false);
    }
  };

  const waitForService = (maxMs: number) =>
    new Promise<void>((resolve) => {
      const start = Date.now();
      const poll = () => {
        fetch('/api/v1/status')
          .then((r) => { if (r.ok) resolve(); else throw new Error(); })
          .catch(() => {
            if (Date.now() - start > maxMs) { resolve(); return; }
            setTimeout(poll, 500);
          });
      };
      setTimeout(poll, 1500);
    });

  const fetchAll = useCallback(async () => {
    if (!name) return;
    try {
      setLoading(true);
      const [proj, provs, hb, gp] = await Promise.allSettled([
        getProject(name),
        listProviders(name),
        getHeartbeat(name),
        listGlobalProviders(),
      ]);
      if (proj.status === 'fulfilled') {
        setProject(proj.value);
        setLanguage(proj.value.settings?.language || '');
        setAdminFrom(proj.value.settings?.admin_from || '');
        setDisabledCmds(proj.value.settings?.disabled_commands?.join(', ') || '');
        setWorkDir(proj.value.work_dir || '');
        setAgentMode(proj.value.agent_mode || 'default');
        setShowCtxIndicator(proj.value.show_context_indicator !== false);
        setReplyFooter(proj.value.reply_footer !== false);
        setInjectSender(proj.value.inject_sender === true);
        setProviderRefs(proj.value.provider_refs || []);
        const afMap: Record<string, string> = {};
        proj.value.platform_configs?.forEach(pc => {
          if (pc.allow_from !== undefined) afMap[pc.type] = pc.allow_from;
        });
        setPlatformAllowFrom(afMap);
      }
      if (provs.status === 'fulfilled') {
        setProviders(provs.value.providers || []);
        setActiveProvider(provs.value.active_provider || '');
      }
      if (hb.status === 'fulfilled') setHeartbeatState(hb.value);
      if (gp.status === 'fulfilled') {
        setGlobalProviders(gp.value.providers || []);
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
        language,
        admin_from: adminFrom,
        disabled_commands: disabledCmds.split(',').map(s => s.trim()).filter(Boolean),
        work_dir: workDir,
        mode: agentMode,
        show_context_indicator: showCtxIndicator,
        reply_footer: replyFooter,
        inject_sender: injectSender,
        platform_allow_from: platformAllowFrom,
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
            <div className="flex items-center justify-between mb-3">
              <h3 className="text-sm font-semibold text-gray-900 dark:text-white">{t('projects.platforms')}</h3>
              <Button size="sm" onClick={() => { setShowAddPlatform(true); setAddPlatType(''); }}>
                <Plus size={14} /> {t('setup.addPlatform', 'Add platform')}
              </Button>
            </div>
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

      {tab === 'providers' && (() => {
        const globalNames = new Set(globalProviders.map(g => g.name));
        const isGlobal = (pName: string) => globalNames.has(pName) && providerRefs.includes(pName);
        const unlinkedGlobals = globalProviders.filter(g => !providerRefs.includes(g.name));
        return (
        <div className="space-y-4">
          {/* Header */}
          <div className="flex justify-between items-center">
            <h3 className="text-sm font-semibold text-gray-900 dark:text-white">{t('providers.title')}</h3>
            <Button size="sm" onClick={() => { setAddMode('pick'); setShowAddProvider(true); }}><Plus size={14} /> {t('providers.add')}</Button>
          </div>

          {/* Unified provider list */}
          {providers.length === 0 ? (
            <Card>
              <div className="py-6 text-center">
                <Plug size={32} className="mx-auto text-gray-300 dark:text-gray-600 mb-2" />
                <p className="text-sm text-gray-500 dark:text-gray-400">{t('providers.emptyProject', 'No providers configured for this project.')}</p>
                <p className="text-xs text-gray-400 dark:text-gray-500 mt-1">{t('providers.emptyProjectHint', 'Link a global provider or add a custom one.')}</p>
              </div>
            </Card>
          ) : (
            <div className="space-y-2">
              {providers.map((p) => (
                <div
                  key={p.name}
                  className={cn(
                    'flex items-center justify-between px-4 py-3 rounded-xl border transition-all',
                    p.active
                      ? 'border-emerald-200 dark:border-emerald-500/20 bg-emerald-50/50 dark:bg-emerald-900/10'
                      : 'border-gray-200 dark:border-gray-700/60 bg-white dark:bg-gray-800/40',
                  )}
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium text-gray-900 dark:text-white">{p.name}</span>
                      {p.active && <Badge variant="success">{t('providers.active')}</Badge>}
                      {isGlobal(p.name) && (
                        <Link to="/providers" className="inline-flex items-center gap-0.5 text-[10px] text-gray-400 hover:text-accent transition-colors">
                          <Link2 size={10} /> {t('providers.global', 'global')}
                        </Link>
                      )}
                    </div>
                    <p className="text-xs text-gray-500 dark:text-gray-400 mt-0.5 truncate">
                      {p.model}{p.base_url ? ` · ${p.base_url}` : ''}
                    </p>
                  </div>
                  <div className="flex items-center gap-1.5 shrink-0 ml-3">
                    {!p.active && (
                      <Button size="sm" variant="ghost" onClick={() => { activateProvider(name!, p.name).then(fetchAll); }}>
                        <Zap size={14} /> {t('providers.activate')}
                      </Button>
                    )}
                    {!p.active && (
                      isGlobal(p.name) ? (
                        <Button
                          size="sm"
                          variant="ghost"
                          className="text-gray-400 hover:text-red-500"
                          onClick={async () => {
                            const next = providerRefs.filter(r => r !== p.name);
                            setSavingRefs(true);
                            try {
                              await saveProviderRefs(name!, next);
                              await fetchAll();
                            } finally { setSavingRefs(false); }
                          }}
                        >
                          <Trash2 size={14} />
                        </Button>
                      ) : (
                        <Button size="sm" variant="ghost" className="text-gray-400 hover:text-red-500" onClick={() => { removeProvider(name!, p.name).then(fetchAll); }}>
                          <Trash2 size={14} />
                        </Button>
                      )
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}

          {/* Add Provider Modal */}
          <Modal open={showAddProvider} onClose={() => setShowAddProvider(false)} title={t('providers.add')}>
            <div className="space-y-4">
              {/* Toggle */}
              <div className="flex rounded-lg bg-gray-100 dark:bg-gray-800 p-0.5">
                <button
                  className={cn('flex-1 px-3 py-1.5 rounded-md text-xs font-medium transition-all', addMode === 'pick' ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-white shadow-sm' : 'text-gray-500')}
                  onClick={() => setAddMode('pick')}
                >{t('providers.linkGlobal', 'Link global')}</button>
                <button
                  className={cn('flex-1 px-3 py-1.5 rounded-md text-xs font-medium transition-all', addMode === 'custom' ? 'bg-white dark:bg-gray-700 text-gray-900 dark:text-white shadow-sm' : 'text-gray-500')}
                  onClick={() => setAddMode('custom')}
                >{t('providers.addCustom', 'Add custom')}</button>
              </div>

              {addMode === 'pick' ? (
                unlinkedGlobals.length === 0 ? (
                  <div className="py-4 text-center">
                    <p className="text-sm text-gray-500 dark:text-gray-400">{t('providers.allLinked', 'All global providers are already linked.')}</p>
                    <Link to="/providers" className="inline-flex items-center gap-1 mt-2 text-xs text-accent hover:underline">
                      {t('providers.manageGlobal', 'Manage global providers')} <ExternalLink size={11} />
                    </Link>
                  </div>
                ) : (
                  <div className="space-y-1.5 max-h-64 overflow-y-auto">
                    {unlinkedGlobals.map(gp => (
                      <button
                        key={gp.name}
                        className="flex items-center justify-between w-full px-3 py-2.5 rounded-lg border border-gray-200 dark:border-gray-700 hover:border-accent/40 hover:bg-accent/5 transition-all text-left"
                        onClick={async () => {
                          const next = [...providerRefs, gp.name];
                          setSavingRefs(true);
                          try {
                            await saveProviderRefs(name!, next);
                            await fetchAll();
                          } finally { setSavingRefs(false); }
                          setShowAddProvider(false);
                        }}
                      >
                        <div className="min-w-0">
                          <div className="text-sm font-medium text-gray-900 dark:text-white">{gp.name}</div>
                          <div className="text-xs text-gray-500 dark:text-gray-400 truncate">{gp.model}{gp.base_url ? ` · ${gp.base_url}` : ''}</div>
                        </div>
                        <Plus size={16} className="shrink-0 text-gray-400" />
                      </button>
                    ))}
                  </div>
                )
              ) : (
                <div className="space-y-3">
                  <Input label={t('providers.name')} value={newProvider.name} onChange={(e) => setNewProvider({...newProvider, name: e.target.value})} />
                  <Input label="API Key" type="password" value={newProvider.api_key} onChange={(e) => setNewProvider({...newProvider, api_key: e.target.value})} />
                  <Input label={t('providers.baseUrl')} value={newProvider.base_url} onChange={(e) => setNewProvider({...newProvider, base_url: e.target.value})} placeholder="https://api.example.com" />
                  <Input label={t('providers.model')} value={newProvider.model} onChange={(e) => setNewProvider({...newProvider, model: e.target.value})} />
                  <div className="flex justify-end gap-2 pt-1">
                    <Button variant="secondary" onClick={() => setShowAddProvider(false)}>{t('common.cancel')}</Button>
                    <Button onClick={handleAddProvider}>{t('providers.add')}</Button>
                  </div>
                </div>
              )}
            </div>
          </Modal>
        </div>
        );
      })()}

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
        <div className="space-y-4">
        {/* Agent settings */}
        <Card>
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-4">{t('projects.agentSettings', 'Agent')}</h3>
          <div className="space-y-4 max-w-lg">
            <Input label={t('projects.workDir', 'Working directory')} value={workDir} onChange={(e) => setWorkDir(e.target.value)} placeholder="/path/to/project" />
            <div>
              <label className="block text-xs font-medium text-gray-600 dark:text-gray-400 mb-1">
                {t('projects.agentMode', 'Permission mode')}
              </label>
              <select
                value={agentMode}
                onChange={(e) => setAgentMode(e.target.value)}
                className="w-full px-3 py-2 text-sm rounded-lg border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-800 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/50"
              >
                <option value="default">default</option>
                <option value="acceptEdits">acceptEdits (edit)</option>
                <option value="plan">plan</option>
                <option value="bypassPermissions">bypassPermissions (yolo)</option>
                <option value="dontAsk">dontAsk</option>
              </select>
            </div>
          </div>
        </Card>

        {/* General settings */}
        <Card>
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-4">{t('projects.generalSettings', 'General')}</h3>
          <div className="space-y-4 max-w-lg">
            <div className="flex items-center justify-between">
              <div>
                <label className="text-sm font-medium text-gray-700 dark:text-gray-300">{t('projects.showCtxIndicator', 'Context indicator')}</label>
                <p className="text-[11px] text-gray-400 mt-0.5">{t('projects.showCtxIndicatorHint', 'Show [ctx: ~N%] suffix on replies')}</p>
              </div>
              <button
                onClick={() => setShowCtxIndicator(!showCtxIndicator)}
                className={cn('w-10 h-6 rounded-full transition-colors', showCtxIndicator ? 'bg-accent' : 'bg-gray-300 dark:bg-gray-700')}
              >
                <div className={cn('w-4 h-4 bg-white rounded-full transition-transform mx-1', showCtxIndicator ? 'translate-x-4' : 'translate-x-0')} />
              </button>
            </div>
            <div className="flex items-center justify-between">
              <div>
                <label className="text-sm font-medium text-gray-700 dark:text-gray-300">{t('projects.replyFooter', 'Reply footer')}</label>
                <p className="text-[11px] text-gray-400 mt-0.5">{t('projects.replyFooterHint', 'Append model/usage metadata to replies')}</p>
              </div>
              <button
                onClick={() => setReplyFooter(!replyFooter)}
                className={cn('w-10 h-6 rounded-full transition-colors', replyFooter ? 'bg-accent' : 'bg-gray-300 dark:bg-gray-700')}
              >
                <div className={cn('w-4 h-4 bg-white rounded-full transition-transform mx-1', replyFooter ? 'translate-x-4' : 'translate-x-0')} />
              </button>
            </div>
            <div className="flex items-center justify-between">
              <div>
                <label className="text-sm font-medium text-gray-700 dark:text-gray-300">{t('projects.injectSender', 'Inject sender')}</label>
                <p className="text-[11px] text-gray-400 mt-0.5">{t('projects.injectSenderHint', 'Prepend sender identity to messages sent to agent')}</p>
              </div>
              <button
                onClick={() => setInjectSender(!injectSender)}
                className={cn('w-10 h-6 rounded-full transition-colors', injectSender ? 'bg-accent' : 'bg-gray-300 dark:bg-gray-700')}
              >
                <div className={cn('w-4 h-4 bg-white rounded-full transition-transform mx-1', injectSender ? 'translate-x-4' : 'translate-x-0')} />
              </button>
            </div>
            <Input label={t('projects.language')} value={language} onChange={(e) => setLanguage(e.target.value)} placeholder="en, zh, ja..." />
            <Input label={t('projects.adminFrom')} value={adminFrom} onChange={(e) => setAdminFrom(e.target.value)} placeholder="user1,user2 or *" />
            <Input label={t('projects.disabledCommands')} value={disabledCmds} onChange={(e) => setDisabledCmds(e.target.value)} placeholder="restart, upgrade, cron" />
          </div>
        </Card>

        {/* Per-platform allow_from */}
        {project.platform_configs && project.platform_configs.length > 0 && (
        <Card>
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-4">{t('projects.platformAccess', 'Platform access control')}</h3>
          <div className="space-y-3 max-w-lg">
            {project.platform_configs.map(pc => (
              <Input
                key={pc.type}
                label={`${pc.type} — ${t('fields.allowFrom')}`}
                value={platformAllowFrom[pc.type] ?? pc.allow_from ?? ''}
                onChange={(e) => setPlatformAllowFrom(prev => ({ ...prev, [pc.type]: e.target.value }))}
                placeholder='user1,user2 or *'
              />
            ))}
          </div>
        </Card>
        )}

        <div className="max-w-lg">
          <Button loading={saving} onClick={handleSaveSettings}>{t('common.save')}</Button>
        </div>
        <Card>
          <h3 className="text-sm font-semibold text-red-600 dark:text-red-400 mb-3">{t('projects.dangerZone', 'Danger Zone')}</h3>
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm text-gray-700 dark:text-gray-300">{t('projects.deleteTitle')}</p>
              <p className="text-xs text-gray-500 dark:text-gray-400 mt-0.5">{t('projects.deleteHint', 'Remove this project from config. Requires restart.')}</p>
            </div>
            <Button variant="danger" size="sm" onClick={() => setShowDeleteConfirm(true)}>
              <Trash2 size={14} /> {t('common.delete')}
            </Button>
          </div>
        </Card>
        </div>
      )}

      {/* Delete confirmation */}
      <Modal open={showDeleteConfirm} onClose={() => setShowDeleteConfirm(false)} title={t('projects.deleteTitle')}>
        <div className="space-y-4 py-2">
          <p className="text-sm text-gray-600 dark:text-gray-400">
            {t('projects.deleteConfirm', { name })}
          </p>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowDeleteConfirm(false)}>{t('common.cancel')}</Button>
            <Button variant="danger" onClick={handleDeleteProject} disabled={deleting}>
              {deleting ? t('common.deleting', 'Deleting...') : t('common.delete')}
            </Button>
          </div>
        </div>
      </Modal>

      {/* Add Platform Modal */}
      <Modal open={showAddPlatform} onClose={() => setShowAddPlatform(false)} title={t('setup.addPlatform', 'Add platform')}>
        {!addPlatType ? (
          <div className="space-y-3 py-2">
            <p className="text-sm text-gray-500 dark:text-gray-400 mb-2">
              {t('setup.choosePlatform', 'Choose a platform to connect:')}
            </p>
            <div className="grid grid-cols-2 gap-2 max-h-80 overflow-y-auto">
              {PLATFORM_OPTIONS.map(({ key, label, color, qr, abbr }) => (
                <button
                  key={key}
                  onClick={() => setAddPlatType(key)}
                  className="flex items-center gap-2.5 p-3 rounded-xl border border-gray-200 dark:border-gray-700 hover:border-accent/50 hover:bg-accent/5 transition-all text-left"
                >
                  <div className={`w-9 h-9 rounded-lg ${color} flex items-center justify-center shrink-0 font-bold text-xs`}>
                    {abbr}
                  </div>
                  <div className="min-w-0">
                    <div className="text-sm font-medium text-gray-900 dark:text-white truncate">{label}</div>
                    <div className="text-[11px] text-gray-400">
                      {qr ? t('setup.scanToConnect', 'Scan QR code') : t('setup.manualSetup', 'Manual setup')}
                    </div>
                  </div>
                </button>
              ))}
            </div>
          </div>
        ) : isQRPlatform(addPlatType) ? (
          <PlatformSetupQR
            platformType={addPlatType as 'feishu' | 'weixin'}
            projectName={name!}
            onComplete={() => {
              setShowAddPlatform(false);
              setShowRestartModal(true);
            }}
            onCancel={() => setAddPlatType('')}
          />
        ) : platformMeta[addPlatType] ? (
          <PlatformManualForm
            platformType={addPlatType}
            projectName={name!}
            onComplete={() => {
              setShowAddPlatform(false);
              setShowRestartModal(true);
            }}
            onCancel={() => setAddPlatType('')}
          />
        ) : (
          <div className="space-y-4 py-4 text-center">
            <p className="text-sm text-gray-600 dark:text-gray-400">
              {t('setup.manualHint', 'For {{platform}}, please configure credentials in config.toml and restart the service.', { platform: PLATFORM_OPTIONS.find(o => o.key === addPlatType)?.label || addPlatType })}
            </p>
            <Button variant="secondary" onClick={() => setAddPlatType('')}>{t('common.back')}</Button>
          </div>
        )}
      </Modal>

      {/* Restart Required Modal */}
      <Modal open={showRestartModal} onClose={() => setShowRestartModal(false)} title={t('setup.restartRequired', 'Restart required')}>
        <div className="space-y-4 py-2">
          <p className="text-sm text-gray-600 dark:text-gray-400">
            {t('setup.restartHint', 'Restart the service for the new platform to take effect.')}
          </p>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => { setShowRestartModal(false); fetchAll(); }}>
              {t('setup.later', 'Later')}
            </Button>
            <Button onClick={async () => { await restartSystem(); setShowRestartModal(false); fetchAll(); }}>
              {t('setup.restartNow', 'Restart now')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
