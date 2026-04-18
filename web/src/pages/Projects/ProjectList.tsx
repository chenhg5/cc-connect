import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { Server, Heart, ArrowRight, FolderKanban, Plus, Smartphone, Settings2 } from 'lucide-react';
import { Card, Badge, Button, Input, Modal, EmptyState } from '@/components/ui';
import { listProjects, type ProjectSummary } from '@/api/projects';
import PlatformSetupQR from './PlatformSetupQR';
import PlatformManualForm from './PlatformManualForm';
import { platformMeta } from '@/lib/platformMeta';

const AGENT_OPTIONS = [
  { key: 'claudecode', label: 'Claude Code' },
  { key: 'codex', label: 'Codex' },
  { key: 'gemini', label: 'Gemini CLI' },
  { key: 'cursor', label: 'Cursor' },
  { key: 'acp', label: 'ACP (Generic)' },
  { key: 'acp:openclaw', label: 'OpenClaw (ACP)' },
  { key: 'opencode', label: 'OpenCode' },
  { key: 'qoder', label: 'Qoder' },
];

const PLATFORM_OPTIONS: { key: string; label: string; color: string; qr?: boolean }[] = [
  { key: 'feishu', label: 'Feishu / Lark', color: 'bg-blue-50 dark:bg-blue-900/30 text-blue-600 dark:text-blue-400', qr: true },
  { key: 'weixin', label: 'WeChat', color: 'bg-green-50 dark:bg-green-900/30 text-green-600 dark:text-green-400', qr: true },
  { key: 'telegram', label: 'Telegram', color: 'bg-sky-50 dark:bg-sky-900/30 text-sky-600 dark:text-sky-400' },
  { key: 'discord', label: 'Discord', color: 'bg-indigo-50 dark:bg-indigo-900/30 text-indigo-600 dark:text-indigo-400' },
  { key: 'slack', label: 'Slack', color: 'bg-purple-50 dark:bg-purple-900/30 text-purple-600 dark:text-purple-400' },
  { key: 'dingtalk', label: 'DingTalk', color: 'bg-orange-50 dark:bg-orange-900/30 text-orange-600 dark:text-orange-400' },
  { key: 'wecom', label: 'WeChat Work', color: 'bg-emerald-50 dark:bg-emerald-900/30 text-emerald-600 dark:text-emerald-400' },
  { key: 'qq', label: 'QQ (OneBot)', color: 'bg-cyan-50 dark:bg-cyan-900/30 text-cyan-600 dark:text-cyan-400' },
  { key: 'qqbot', label: 'QQ Bot (Official)', color: 'bg-cyan-50 dark:bg-cyan-900/30 text-cyan-600 dark:text-cyan-400' },
  { key: 'line', label: 'LINE', color: 'bg-lime-50 dark:bg-lime-900/30 text-lime-600 dark:text-lime-400' },
  { key: 'weibo', label: 'Weibo (微博)', color: 'bg-red-50 dark:bg-red-900/30 text-red-600 dark:text-red-400' },
];

export default function ProjectList() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [loading, setLoading] = useState(true);

  // Add project wizard state
  const [showWizard, setShowWizard] = useState(false);
  const [wizStep, setWizStep] = useState<'name' | 'platform' | 'qr' | 'form' | 'done'>('name');
  const [newProjName, setNewProjName] = useState('');
  const [newWorkDir, setNewWorkDir] = useState('');
  const [newAgentType, setNewAgentType] = useState('claudecode');
  const [selectedPlat, setSelectedPlat] = useState('');

  const fetch = useCallback(async () => {
    try {
      setLoading(true);
      const data = await listProjects();
      setProjects(data.projects || []);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetch();
    const handler = () => fetch();
    window.addEventListener('cc:refresh', handler);
    return () => window.removeEventListener('cc:refresh', handler);
  }, [fetch]);

  const openWizard = () => {
    setShowWizard(true);
    setWizStep('name');
    setNewProjName('');
    setNewWorkDir('');
    setNewAgentType('claudecode');
    setSelectedPlat('');
  };

  const isQRPlatform = (type: string) => type === 'feishu' || type === 'lark' || type === 'weixin';

  const handlePlatformSelect = (key: string) => {
    setSelectedPlat(key);
    if (isQRPlatform(key)) {
      setWizStep('qr');
    } else if (platformMeta[key]) {
      setWizStep('form');
    } else {
      setWizStep('done');
    }
  };

  const handleQRComplete = () => {
    setShowWizard(false);
    fetch();
  };

  const handleManualDone = async () => {
    // For non-QR platforms, use feishu EnsureProject to create the project skeleton,
    // then the user configures platform details from the project detail page.
    // We use the feishu save endpoint with empty credentials just to create the project.
    // Actually, let's guide the user to the project detail page to configure.
    setShowWizard(false);
    fetch();
    navigate(`/projects/${newProjName}`);
  };

  if (loading && projects.length === 0) {
    return <div className="flex items-center justify-center h-64 text-gray-400 animate-pulse">Loading...</div>;
  }

  return (
    <div className="animate-fade-in space-y-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-bold text-gray-900 dark:text-white">{t('projects.title')}</h2>
        <Button size="sm" onClick={openWizard}>
          <Plus size={14} /> {t('setup.addProject', 'Add project')}
        </Button>
      </div>

      {projects.length === 0 ? (
        <EmptyState message={t('projects.noProjects')} icon={FolderKanban} />
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
          {projects.map((p) => (
            <Link key={p.name} to={`/projects/${p.name}`}>
              <Card hover className="h-full flex flex-col">
                <div className="flex items-start justify-between mb-3">
                  <div className="flex items-center gap-2">
                    <Server size={18} className="text-gray-400" />
                    <h3 className="font-semibold text-gray-900 dark:text-white">{p.name}</h3>
                  </div>
                  <ArrowRight size={16} className="text-gray-300 dark:text-gray-600" />
                </div>
                <div className="flex flex-wrap gap-1.5 mb-3">
                  <Badge variant="info">{p.agent_type}</Badge>
                  {p.platforms?.slice(0, 3).map((pl) => <Badge key={pl}>{pl}</Badge>)}
                  {(p.platforms?.length ?? 0) > 3 && (
                    <Badge>+{p.platforms!.length - 3}</Badge>
                  )}
                </div>
                <div className="flex items-center justify-between text-xs text-gray-500 dark:text-gray-400 mt-auto pt-3 border-t border-gray-100 dark:border-gray-800">
                  <span>{p.sessions_count} {t('nav.sessions').toLowerCase()}</span>
                  {p.heartbeat_enabled && (
                    <span className="flex items-center gap-1 text-emerald-500"><Heart size={12} /> {t('heartbeat.title')}</span>
                  )}
                </div>
              </Card>
            </Link>
          ))}
        </div>
      )}

      {/* Add Project Wizard Modal */}
      <Modal
        open={showWizard}
        onClose={() => setShowWizard(false)}
        title={t('setup.addProject', 'Add project')}
      >
        {wizStep === 'name' && (
          <div className="space-y-4 py-2">
            <Input
              label={t('setup.projectName', 'Project name')}
              value={newProjName}
              onChange={(e) => setNewProjName(e.target.value.replace(/[^a-zA-Z0-9_-]/g, ''))}
              placeholder="my-project"
              autoFocus
            />
            <Input
              label={t('setup.workDir', 'Working directory')}
              value={newWorkDir}
              onChange={(e) => setNewWorkDir(e.target.value)}
              placeholder="/path/to/project"
            />
            <div>
              <label className="block text-xs font-medium text-gray-600 dark:text-gray-400 mb-1">
                {t('setup.agentType', 'Agent type')}
              </label>
              <select
                value={newAgentType}
                onChange={(e) => setNewAgentType(e.target.value)}
                className="w-full px-3 py-2 text-sm rounded-lg border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-800 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/50"
              >
                {AGENT_OPTIONS.map(a => (
                  <option key={a.key} value={a.key}>{a.label}</option>
                ))}
              </select>
            </div>
            <div className="flex justify-end gap-2 pt-2">
              <Button variant="secondary" onClick={() => setShowWizard(false)}>{t('common.cancel')}</Button>
              <Button disabled={!newProjName.trim() || !newWorkDir.trim()} onClick={() => setWizStep('platform')}>
                {t('setup.next', 'Next')}
              </Button>
            </div>
          </div>
        )}

        {wizStep === 'platform' && (
          <div className="space-y-3 py-2">
            <p className="text-sm text-gray-500 dark:text-gray-400 mb-2">
              {t('setup.choosePlatform', 'Choose a platform to connect:')}
            </p>
            <div className="grid grid-cols-2 gap-2 max-h-80 overflow-y-auto">
              {PLATFORM_OPTIONS.map(({ key, label, color, qr }) => (
                <button
                  key={key}
                  onClick={() => handlePlatformSelect(key)}
                  className="flex items-center gap-2.5 p-3 rounded-xl border border-gray-200 dark:border-gray-700 hover:border-accent/50 hover:bg-accent/5 transition-all text-left"
                >
                  <div className={`w-9 h-9 rounded-lg ${color} flex items-center justify-center shrink-0`}>
                    {qr ? <Smartphone size={16} /> : <Settings2 size={16} />}
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
            <div className="flex justify-start pt-2">
              <Button variant="secondary" size="sm" onClick={() => setWizStep('name')}>{t('common.back')}</Button>
            </div>
          </div>
        )}

        {wizStep === 'qr' && isQRPlatform(selectedPlat) && (
          <PlatformSetupQR
            platformType={selectedPlat as 'feishu' | 'weixin'}
            projectName={newProjName}
            workDir={newWorkDir}
            agentType={newAgentType}
            onComplete={handleQRComplete}
            onCancel={() => setWizStep('platform')}
          />
        )}

        {wizStep === 'form' && platformMeta[selectedPlat] && (
          <PlatformManualForm
            platformType={selectedPlat}
            projectName={newProjName}
            workDir={newWorkDir}
            agentType={newAgentType}
            onComplete={() => {
              setShowWizard(false);
              fetch();
            }}
            onCancel={() => setWizStep('platform')}
          />
        )}

        {wizStep === 'done' && !isQRPlatform(selectedPlat) && (
          <div className="space-y-4 py-4 text-center">
            <Settings2 size={40} className="mx-auto text-gray-400" />
            <p className="text-sm text-gray-600 dark:text-gray-400">
              {t('setup.manualHint', 'For {{platform}}, please configure credentials in config.toml or via the project detail page after creating the project.', { platform: PLATFORM_OPTIONS.find(o => o.key === selectedPlat)?.label || selectedPlat })}
            </p>
            <div className="flex justify-center gap-2">
              <Button variant="secondary" onClick={() => setWizStep('platform')}>{t('common.back')}</Button>
            </div>
          </div>
        )}
      </Modal>
    </div>
  );
}
