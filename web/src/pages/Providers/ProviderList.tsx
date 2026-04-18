import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Plug, Plus, Trash2, Pencil, ExternalLink, Star, Sparkles, X, Eye, EyeOff, Check,
} from 'lucide-react';
import { Card, Button, Badge, Modal, Input } from '@/components/ui';
import {
  listGlobalProviders, addGlobalProvider, updateGlobalProvider, removeGlobalProvider,
  fetchProviderPresets,
  type GlobalProvider, type ProviderPreset, type ProviderModel,
} from '@/api/providers';
import { cn } from '@/lib/utils';

type Tab = 'providers' | 'presets';

export default function ProviderList() {
  const { t, i18n } = useTranslation();
  const [tab, setTab] = useState<Tab>('providers');
  const [providers, setProviders] = useState<GlobalProvider[]>([]);
  const [presets, setPresets] = useState<ProviderPreset[]>([]);
  const [loading, setLoading] = useState(true);
  const [presetsLoading, setPresetsLoading] = useState(false);
  const [showAddModal, setShowAddModal] = useState(false);
  const [editProvider, setEditProvider] = useState<GlobalProvider | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const data = await listGlobalProviders();
      setProviders(data.providers || []);
    } catch { /* empty */ }
    setLoading(false);
  }, []);

  const loadPresets = useCallback(async () => {
    setPresetsLoading(true);
    try {
      const data = await fetchProviderPresets();
      setPresets(data.providers || []);
    } catch { /* empty */ }
    setPresetsLoading(false);
  }, []);

  useEffect(() => { refresh(); }, [refresh]);
  useEffect(() => {
    if (tab === 'presets' && presets.length === 0) loadPresets();
  }, [tab, presets.length, loadPresets]);

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await removeGlobalProvider(deleteTarget);
      await refresh();
    } catch { /* empty */ }
    setDeleteTarget(null);
  };

  const handleAddFromPreset = (preset: ProviderPreset) => {
    const agentTypes = Object.keys(preset.agents || {});
    const firstAt = agentTypes[0] || 'claudecode';
    const firstAc = preset.agents?.[firstAt];

    const endpoints: Record<string, string> = {};
    const agentModels: Record<string, string> = {};
    const agentModelLists: Record<string, ProviderModel[]> = {};
    let codex: GlobalProvider['codex'];

    for (const [at, cfg] of Object.entries(preset.agents || {})) {
      if (at !== firstAt && cfg.base_url) endpoints[at] = cfg.base_url;
      if (at !== firstAt && cfg.model) agentModels[at] = cfg.model;
      const models = cfg.models?.map(m => ({ model: m }));
      if (models?.length && at !== firstAt) agentModelLists[at] = models;
      if (at === firstAt && models?.length) { /* stored in top-level */ }
      if (at === 'codex' && cfg.codex_config?.wire_api) {
        codex = { wire_api: cfg.codex_config.wire_api, http_headers: cfg.codex_config.http_headers };
      }
    }

    setEditProvider({
      name: preset.name,
      base_url: firstAc?.base_url || '',
      model: firstAc?.model || '',
      thinking: preset.thinking || '',
      models: firstAc?.models?.map(m => ({ model: m })),
      agent_types: agentTypes,
      endpoints: Object.keys(endpoints).length ? endpoints : undefined,
      agent_models: Object.keys(agentModels).length ? agentModels : undefined,
      agent_model_lists: Object.keys(agentModelLists).length ? agentModelLists : undefined,
      codex,
      _preset: preset,
    } as any);
    setShowAddModal(true);
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold text-gray-900 dark:text-white">
            {t('globalProviders.title')}
          </h1>
          <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">
            {t('globalProviders.subtitle')}
          </p>
        </div>
        <Button onClick={() => { setEditProvider(null); setShowAddModal(true); }}>
          <Plus size={16} className="mr-1.5" /> {t('globalProviders.add')}
        </Button>
      </div>

      {/* Tabs */}
      <div className="flex gap-1 p-1 rounded-xl bg-gray-100 dark:bg-white/[0.06] w-fit">
        {(['providers', 'presets'] as const).map(key => (
          <button
            key={key}
            onClick={() => setTab(key)}
            className={cn(
              'px-4 py-1.5 rounded-lg text-sm font-medium transition-all',
              tab === key
                ? 'bg-white dark:bg-white/10 text-gray-900 dark:text-white shadow-sm'
                : 'text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300',
            )}
          >
            {t(`globalProviders.tab.${key}`)}
          </button>
        ))}
      </div>

      {/* Content */}
      {tab === 'providers' && (
        <ProviderGrid
          providers={providers}
          loading={loading}
          onEdit={p => { setEditProvider(p); setShowAddModal(true); }}
          onDelete={name => setDeleteTarget(name)}
          t={t}
        />
      )}
      {tab === 'presets' && (
        <PresetGrid
          presets={presets}
          loading={presetsLoading}
          existingNames={new Set(providers.map(p => p.name))}
          onAdd={handleAddFromPreset}
          onRefresh={loadPresets}
          t={t}
          lang={i18n.language || 'en'}
        />
      )}

      {/* Add/Edit Modal */}
      {showAddModal && (
        <ProviderFormModal
          provider={editProvider}
          onClose={() => setShowAddModal(false)}
          onSave={async (p) => {
            if (editProvider?.name && providers.some(x => x.name === editProvider.name)) {
              await updateGlobalProvider(editProvider.name, p);
            } else {
              await addGlobalProvider(p);
            }
            setShowAddModal(false);
            await refresh();
          }}
          t={t}
        />
      )}

      {/* Delete confirm */}
      <Modal open={!!deleteTarget} onClose={() => setDeleteTarget(null)} title={t('common.confirmDelete')}>
        <p className="text-sm text-gray-500 dark:text-gray-400 mb-4">
          {t('globalProviders.deleteHint', { name: deleteTarget })}
        </p>
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={() => setDeleteTarget(null)}>{t('common.cancel')}</Button>
          <Button variant="danger" onClick={handleDelete}>{t('common.delete')}</Button>
        </div>
      </Modal>
    </div>
  );
}

/* ── Provider Grid ── */

function ProviderGrid({
  providers, loading, onEdit, onDelete, t,
}: {
  providers: GlobalProvider[];
  loading: boolean;
  onEdit: (p: GlobalProvider) => void;
  onDelete: (name: string) => void;
  t: (k: string) => string;
}) {
  if (loading) return <p className="text-sm text-gray-400">{t('common.loading')}</p>;
  if (providers.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-20 text-center">
        <Plug size={40} className="text-gray-300 dark:text-gray-600 mb-3" />
        <p className="text-sm font-medium text-gray-500 dark:text-gray-400">{t('globalProviders.empty')}</p>
        <p className="mt-1 text-xs text-gray-400 dark:text-gray-500">{t('globalProviders.emptyHint')}</p>
      </div>
    );
  }
  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {providers.map(p => (
        <Card key={p.name} className="group relative">
          <div className="flex items-start justify-between">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <Plug size={16} className="text-accent shrink-0" />
                <h3 className="font-medium text-gray-900 dark:text-white truncate">{p.name}</h3>
              </div>
              {p.base_url && (
                <p className="mt-1 text-xs text-gray-400 dark:text-gray-500 truncate">{p.base_url}</p>
              )}
              {p.model && (
                <Badge className="mt-2">{p.model}</Badge>
              )}
              {p.models && p.models.length > 0 && (
                <ModelBadges models={p.models.map(m => m.alias || m.model)} limit={3} />
              )}
              {p.agent_types && p.agent_types.length > 0 && (
                <div className="mt-2 flex flex-wrap gap-1">
                  {p.agent_types.map(a => (
                    <Badge key={a} variant="info" className="text-xs">{a}</Badge>
                  ))}
                </div>
              )}
              {p.thinking && (
                <p className="mt-1.5 text-xs text-amber-600 dark:text-amber-400">thinking: {p.thinking}</p>
              )}
            </div>
            <div className="flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
              <button
                onClick={() => onEdit(p)}
                className="p-1.5 rounded-lg hover:bg-gray-100 dark:hover:bg-white/[0.06] text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
              >
                <Pencil size={14} />
              </button>
              <button
                onClick={() => onDelete(p.name)}
                className="p-1.5 rounded-lg hover:bg-red-50 dark:hover:bg-red-500/10 text-gray-400 hover:text-red-500"
              >
                <Trash2 size={14} />
              </button>
            </div>
          </div>
        </Card>
      ))}
    </div>
  );
}

/* ── Presets Grid ── */

function PresetGrid({
  presets, loading, existingNames, onAdd, onRefresh, t, lang,
}: {
  presets: ProviderPreset[];
  loading: boolean;
  existingNames: Set<string>;
  onAdd: (p: ProviderPreset) => void;
  onRefresh: () => void;
  t: (k: string, opts?: any) => string;
  lang: string;
}) {
  if (loading) return <p className="text-sm text-gray-400">{t('common.loading')}</p>;
  if (presets.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-20 text-center">
        <Sparkles size={40} className="text-gray-300 dark:text-gray-600 mb-3" />
        <p className="text-sm font-medium text-gray-500 dark:text-gray-400">{t('globalProviders.noPresets')}</p>
        <p className="mt-1 text-xs text-gray-400 dark:text-gray-500">{t('globalProviders.noPresetsHint')}</p>
        <Button variant="ghost" onClick={onRefresh} className="mt-3">{t('common.refresh')}</Button>
      </div>
    );
  }
  const sorted = [...presets].sort((a, b) => a.tier - b.tier);
  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {sorted.map(p => {
        const added = existingNames.has(p.name);
        return (
          <Card key={p.name} className="relative overflow-hidden">
            {p.featured && (
              <div className="absolute top-0 right-0 bg-amber-400/90 text-white text-[10px] font-bold px-2 py-0.5 rounded-bl-lg">
                <Star size={10} className="inline mr-0.5 -mt-0.5" />
              </div>
            )}
            <div className="space-y-3">
              <div>
                <h3 className="font-medium text-gray-900 dark:text-white">{p.display_name || p.name}</h3>
                {(p.description || p.description_zh) && (
                  <p className="mt-1 text-xs text-gray-500 dark:text-gray-400 line-clamp-2">
                    {lang.startsWith('zh') && p.description_zh ? p.description_zh : p.description}
                  </p>
                )}
              </div>
              {p.agents && Object.keys(p.agents).length > 0 && (
                <div className="flex flex-wrap gap-1">
                  {Object.keys(p.agents).map(a => (
                    <Badge key={a} variant="info" className="text-xs">{a}</Badge>
                  ))}
                </div>
              )}
              {p.features && p.features.length > 0 && (
                <div className="flex flex-wrap gap-1">
                  {p.features.map(f => (
                    <Badge key={f} variant="outline" className="text-xs">{f}</Badge>
                  ))}
                </div>
              )}
              {(() => {
                const firstAc = p.agents?.[Object.keys(p.agents || {})[0]];
                return firstAc?.models && firstAc.models.length > 0 ? (
                  <ModelBadges models={firstAc.models} limit={5} />
                ) : null;
              })()}
              <div className="flex items-center justify-between pt-1">
                {p.invite_url ? (
                  <a
                    href={p.invite_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-xs text-accent hover:underline inline-flex items-center gap-1"
                  >
                    {t('globalProviders.register')} <ExternalLink size={11} />
                  </a>
                ) : <span />}
                <Button
                  size="sm"
                  variant={added ? 'ghost' : 'primary'}
                  disabled={added}
                  onClick={() => onAdd(p)}
                >
                  {added ? t('globalProviders.added') : t('globalProviders.addPreset')}
                </Button>
              </div>
            </div>
          </Card>
        );
      })}
    </div>
  );
}

/* ── Model Badges (collapsible) ── */

function ModelBadges({ models, limit = 3 }: { models: string[]; limit?: number }) {
  const [expanded, setExpanded] = useState(false);
  const visible = expanded ? models : models.slice(0, limit);
  const remaining = models.length - limit;

  return (
    <div className="mt-2 flex flex-wrap gap-1 items-center">
      {visible.map(m => (
        <Badge key={m} variant="outline" className="text-xs">{m}</Badge>
      ))}
      {remaining > 0 && !expanded && (
        <button
          onClick={() => setExpanded(true)}
          className="text-[11px] text-accent hover:underline"
        >
          +{remaining} more
        </button>
      )}
      {expanded && remaining > 0 && (
        <button
          onClick={() => setExpanded(false)}
          className="text-[11px] text-gray-400 hover:text-gray-600 hover:underline"
        >
          less
        </button>
      )}
    </div>
  );
}

/* ── Model List Editor ── */

function ModelListEditor({
  models, onChange, defaultModel, onSetDefault,
}: {
  models: ProviderModel[];
  onChange: (models: ProviderModel[]) => void;
  defaultModel?: string;
  onSetDefault?: (model: string) => void;
}) {
  const [input, setInput] = useState('');

  const addModel = () => {
    const name = input.trim();
    if (!name || models.some(m => m.model === name)) return;
    onChange([...models, { model: name }]);
    setInput('');
  };

  const removeModel = (model: string) => {
    onChange(models.filter(m => m.model !== model));
  };

  return (
    <div className="space-y-2">
      {models.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {models.map(m => {
            const isDefault = defaultModel === m.model;
            return (
              <span
                key={m.model}
                className={cn(
                  'inline-flex items-center gap-1 px-2 py-0.5 rounded-lg text-xs transition-colors',
                  isDefault
                    ? 'bg-accent/15 text-accent border border-accent/30'
                    : 'bg-gray-100 dark:bg-white/[0.06] text-gray-700 dark:text-gray-300 hover:bg-gray-200 dark:hover:bg-white/10',
                )}
              >
                {onSetDefault && !isDefault && (
                  <button
                    type="button"
                    onClick={() => onSetDefault(m.model)}
                    className="text-gray-400 hover:text-accent transition-colors"
                    title="Set as default"
                  >
                    <Check size={12} />
                  </button>
                )}
                {isDefault && <Check size={12} className="text-accent" />}
                {m.model}
                <button
                  type="button"
                  onClick={() => removeModel(m.model)}
                  className="text-gray-400 hover:text-red-500 transition-colors"
                >
                  <X size={12} />
                </button>
              </span>
            );
          })}
        </div>
      )}
      <div className="flex gap-2">
        <Input
          value={input}
          onChange={e => setInput(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); addModel(); } }}
          placeholder="model-name"
          className="flex-1"
        />
        <Button type="button" variant="ghost" size="sm" onClick={addModel} disabled={!input.trim()}>
          <Plus size={14} />
        </Button>
      </div>
    </div>
  );
}

/* ── Per-agent config type (internal form state) ── */

type AgentConfigEntry = { base_url: string; model: string; models: ProviderModel[]; wire_api?: string };

function buildPerAgentConfigs(form: GlobalProvider): Record<string, AgentConfigEntry> {
  const agents = form.agent_types || [];
  const result: Record<string, AgentConfigEntry> = {};
  for (const at of agents) {
    result[at] = {
      base_url: form.endpoints?.[at] || form.base_url || '',
      model: form.agent_models?.[at] || form.model || '',
      models: form.agent_model_lists?.[at] || form.models || [],
      wire_api: at === 'codex' ? form.codex?.wire_api || '' : undefined,
    };
  }
  return result;
}

function mergePerAgentToForm(form: GlobalProvider, configs: Record<string, AgentConfigEntry>): GlobalProvider {
  const agents = Object.keys(configs);
  if (agents.length === 0) return form;

  const first = agents[0];
  const base = configs[first];
  const endpoints: Record<string, string> = {};
  const agentModels: Record<string, string> = {};
  const agentModelLists: Record<string, ProviderModel[]> = {};
  let codex: GlobalProvider['codex'];

  for (const at of agents) {
    const cfg = configs[at];
    if (at !== first) {
      if (cfg.base_url && cfg.base_url !== base.base_url) endpoints[at] = cfg.base_url;
      if (cfg.model && cfg.model !== base.model) agentModels[at] = cfg.model;
      const modelsStr = JSON.stringify(cfg.models);
      const baseModelsStr = JSON.stringify(base.models);
      if (cfg.models.length > 0 && modelsStr !== baseModelsStr) agentModelLists[at] = cfg.models;
    }
    if (at === 'codex' && cfg.wire_api) {
      codex = { wire_api: cfg.wire_api };
    }
  }

  return {
    ...form,
    base_url: base.base_url,
    model: base.model,
    models: base.models.length > 0 ? base.models : undefined,
    endpoints: Object.keys(endpoints).length ? endpoints : undefined,
    agent_models: Object.keys(agentModels).length ? agentModels : undefined,
    agent_model_lists: Object.keys(agentModelLists).length ? agentModelLists : undefined,
    codex: codex || undefined,
  };
}

/* ── Per-agent config editor ── */

function AgentConfigEditor({
  agentType, config, onChange, t,
}: {
  agentType: string;
  config: AgentConfigEntry;
  onChange: (cfg: AgentConfigEntry) => void;
  t: (k: string) => string;
}) {
  return (
    <div className="space-y-3 pt-2">
      <div>
        <label className="block text-xs font-medium text-gray-500 dark:text-gray-400 mb-1">
          {t('globalProviders.form.baseUrl')}
        </label>
        <Input
          value={config.base_url}
          onChange={e => onChange({ ...config, base_url: e.target.value })}
          placeholder="https://api.example.com/v1"
        />
      </div>
      <div>
        <label className="block text-xs font-medium text-gray-500 dark:text-gray-400 mb-1">
          {t('globalProviders.form.model')}
        </label>
        <Input
          value={config.model}
          onChange={e => onChange({ ...config, model: e.target.value })}
          placeholder="model-name"
        />
      </div>
      <div>
        <label className="block text-xs font-medium text-gray-500 dark:text-gray-400 mb-1">
          {t('globalProviders.form.models')}
        </label>
        <ModelListEditor
          models={config.models}
          onChange={models => onChange({ ...config, models })}
          defaultModel={config.model}
          onSetDefault={model => onChange({ ...config, model })}
        />
      </div>
      {agentType === 'codex' && (
        <div>
          <label className="block text-xs font-medium text-gray-500 dark:text-gray-400 mb-1">
            {t('globalProviders.form.codexWireApi')}
          </label>
          <select
            value={config.wire_api || ''}
            onChange={e => onChange({ ...config, wire_api: e.target.value || undefined })}
            className={cn(
              'w-full rounded-xl border px-3 py-2 text-sm outline-none transition-colors',
              'border-gray-200 bg-white text-gray-900',
              'dark:border-white/10 dark:bg-white/[0.04] dark:text-white',
              'focus:border-accent focus:ring-1 focus:ring-accent/30',
            )}
          >
            <option value="">default</option>
            <option value="responses">responses</option>
            <option value="chat">chat</option>
          </select>
        </div>
      )}
    </div>
  );
}

/* ── Add/Edit Form Modal ── */

function ProviderFormModal({
  provider, onClose, onSave, t,
}: {
  provider: GlobalProvider | null;
  onClose: () => void;
  onSave: (p: GlobalProvider) => Promise<void>;
  t: (k: string) => string;
}) {
  const isEdit = !!provider?.name;
  const [form, setForm] = useState<GlobalProvider>(provider || { name: '' });
  const [saving, setSaving] = useState(false);
  const [showKey, setShowKey] = useState(false);
  const [perAgent, setPerAgent] = useState<Record<string, AgentConfigEntry>>(() =>
    provider ? buildPerAgentConfigs(provider) : {},
  );
  const [activeAgentTab, setActiveAgentTab] = useState<string>('');

  const agents = form.agent_types || [];
  const multiAgent = agents.length >= 2;

  const updatePerAgent = (at: string, cfg: AgentConfigEntry) => {
    setPerAgent(prev => ({ ...prev, [at]: cfg }));
  };

  const set = (key: keyof GlobalProvider, value: any) => {
    setForm(f => {
      const next = { ...f, [key]: value };
      if (key === 'agent_types') {
        const newAgents = value as string[];
        setPerAgent(prev => {
          const updated = { ...prev };
          for (const at of newAgents) {
            if (!updated[at]) {
              updated[at] = { base_url: f.base_url || '', model: f.model || '', models: [...(f.models || [])] };
              if (at === 'codex') updated[at].wire_api = f.codex?.wire_api || '';
            }
          }
          for (const at of Object.keys(updated)) {
            if (!newAgents.includes(at)) delete updated[at];
          }
          return updated;
        });
        if (newAgents.length >= 2 && !newAgents.includes(activeAgentTab)) {
          setActiveAgentTab(newAgents[0]);
        }
      }
      return next;
    });
  };

  const handleSubmit = async () => {
    if (!form.name) return;
    setSaving(true);
    try {
      const final = multiAgent ? mergePerAgentToForm(form, perAgent) : form;
      await onSave(final);
    } catch { /* empty */ }
    setSaving(false);
  };

  return (
    <Modal open onClose={onClose} title={isEdit ? t('globalProviders.edit') : t('globalProviders.add')}>
      <div className="space-y-5">
        <div className="space-y-4">
          {/* Name */}
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              {t('globalProviders.form.name')} *
            </label>
            <Input
              value={form.name}
              onChange={e => set('name', e.target.value)}
              placeholder="e.g. minimaxi"
              disabled={isEdit}
            />
          </div>

          {/* API Key */}
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              API Key
            </label>
            <div className="relative">
              <Input
                type={showKey ? 'text' : 'password'}
                value={form.api_key || ''}
                onChange={e => set('api_key', e.target.value)}
                placeholder="sk-..."
                className="pr-10"
              />
              <button
                type="button"
                onClick={() => setShowKey(!showKey)}
                className="absolute right-2 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600"
              >
                {showKey ? <EyeOff size={16} /> : <Eye size={16} />}
              </button>
            </div>
          </div>

          {/* Agent Types */}
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              {t('globalProviders.form.agentTypes')}
            </label>
            <div className="flex flex-wrap gap-2">
              {['claudecode', 'codex', 'gemini', 'opencode', 'cursor', 'kimi', 'qoder', 'acp'].map(at => {
                const selected = agents.includes(at);
                return (
                  <button
                    key={at}
                    type="button"
                    onClick={() => {
                      set('agent_types', selected ? agents.filter(x => x !== at) : [...agents, at]);
                    }}
                    className={cn(
                      'px-2.5 py-1 rounded-lg text-xs font-medium border transition-colors',
                      selected
                        ? 'bg-accent/10 text-accent border-accent/30'
                        : 'bg-transparent text-gray-400 border-gray-200 dark:border-white/10 hover:border-gray-300',
                    )}
                  >
                    {at}
                  </button>
                );
              })}
            </div>
            <p className="mt-1 text-xs text-gray-400">{t('globalProviders.form.agentTypesHint')}</p>
          </div>

          {/* Base URL / Model / Models — flat when <= 1 agent, tabbed when >= 2 */}
          {!multiAgent ? (
            <>
              <div>
                <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
                  {t('globalProviders.form.baseUrl')}
                </label>
                <Input
                  value={form.base_url || ''}
                  onChange={e => set('base_url', e.target.value)}
                  placeholder="https://api.example.com/v1"
                />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
                  {t('globalProviders.form.model')}
                </label>
                <Input
                  value={form.model || ''}
                  onChange={e => set('model', e.target.value)}
                  placeholder="claude-sonnet-4-20250514"
                />
                <p className="mt-1 text-xs text-gray-400">{t('globalProviders.form.modelHint')}</p>
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
                  {t('globalProviders.form.models')}
                </label>
                <ModelListEditor
                  models={form.models || []}
                  onChange={models => set('models', models)}
                  defaultModel={form.model}
                  onSetDefault={model => set('model', model)}
                />
                <p className="mt-1 text-xs text-gray-400">{t('globalProviders.form.modelsHint')}</p>
              </div>
            </>
          ) : (
            <div className="rounded-xl border border-gray-200 dark:border-white/10 overflow-hidden">
              <p className="px-3 pt-3 text-xs text-gray-400">{t('globalProviders.form.perAgentHint')}</p>
              <div className="flex gap-1 px-3 pt-2 pb-0">
                {agents.map(at => (
                  <button
                    key={at}
                    type="button"
                    onClick={() => setActiveAgentTab(at)}
                    className={cn(
                      'px-3 py-1.5 rounded-t-lg text-xs font-medium transition-colors',
                      (activeAgentTab || agents[0]) === at
                        ? 'bg-white dark:bg-white/10 text-gray-900 dark:text-white shadow-sm'
                        : 'text-gray-400 hover:text-gray-600 dark:hover:text-gray-300',
                    )}
                  >
                    {at}
                  </button>
                ))}
              </div>
              <div className="px-3 pb-3 bg-white dark:bg-white/[0.02]">
                {agents.map(at => {
                  if ((activeAgentTab || agents[0]) !== at) return null;
                  return (
                    <AgentConfigEditor
                      key={at}
                      agentType={at}
                      config={perAgent[at] || { base_url: '', model: '', models: [] }}
                      onChange={cfg => updatePerAgent(at, cfg)}
                      t={t}
                    />
                  );
                })}
              </div>
            </div>
          )}

          {/* Thinking */}
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              Thinking
            </label>
            <select
              value={form.thinking || ''}
              onChange={e => set('thinking', e.target.value)}
              className={cn(
                'w-full rounded-xl border px-3 py-2 text-sm outline-none transition-colors',
                'border-gray-200 bg-white text-gray-900',
                'dark:border-white/10 dark:bg-white/[0.04] dark:text-white',
                'focus:border-accent focus:ring-1 focus:ring-accent/30',
              )}
            >
              <option value="">{t('globalProviders.form.thinkingDefault')}</option>
              <option value="enabled">enabled</option>
              <option value="disabled">disabled</option>
            </select>
          </div>
        </div>

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>{t('common.cancel')}</Button>
          <Button onClick={handleSubmit} disabled={!form.name || saving}>
            {saving ? t('common.loading') : t('common.save')}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
