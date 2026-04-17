import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Plug, Plus, Trash2, Pencil, ExternalLink, Sparkles, X, Eye, EyeOff,
} from 'lucide-react';
import { Card, Button, Badge, Modal, Input } from '@/components/ui';
import {
  listGlobalProviders, addGlobalProvider, updateGlobalProvider, removeGlobalProvider,
  fetchProviderPresets,
  type GlobalProvider, type ProviderPreset,
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
    const baseUrl = preset.endpoints?.['claudecode'] || preset.base_url;
    setEditProvider({
      name: preset.name,
      base_url: baseUrl,
      model: preset.agent_models?.['claudecode'] || preset.models?.[0] || '',
      thinking: preset.thinking || '',
      models: preset.models?.map(m => ({ model: m })),
      agent_types: preset.agents || [],
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
                <div className="mt-2 flex flex-wrap gap-1">
                  {p.models.map(m => (
                    <Badge key={m.model} variant="outline" className="text-xs">
                      {m.alias || m.model}
                    </Badge>
                  ))}
                </div>
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
            <div className="space-y-3">
              <div>
                <h3 className="font-medium text-gray-900 dark:text-white">{p.display_name || p.name}</h3>
                {(p.description || p.description_zh) && (
                  <p className="mt-1 text-xs text-gray-500 dark:text-gray-400 line-clamp-2">
                    {lang.startsWith('zh') && p.description_zh ? p.description_zh : p.description}
                  </p>
                )}
              </div>
              {p.agents && p.agents.length > 0 && (
                <div className="flex flex-wrap gap-1">
                  {p.agents.map(a => (
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
              {p.models && p.models.length > 0 && (
                <div className="flex flex-wrap gap-1">
                  {p.models.slice(0, 4).map(m => (
                    <Badge key={m} className="text-xs">{m}</Badge>
                  ))}
                  {p.models.length > 4 && (
                    <Badge className="text-xs">+{p.models.length - 4}</Badge>
                  )}
                </div>
              )}
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

  const set = (key: keyof GlobalProvider, value: any) =>
    setForm(f => ({ ...f, [key]: value }));

  const handleSubmit = async () => {
    if (!form.name) return;
    setSaving(true);
    try {
      await onSave(form);
    } catch { /* empty */ }
    setSaving(false);
  };

  return (
    <Modal open onClose={onClose} title={isEdit ? t('globalProviders.edit') : t('globalProviders.add')}>
      <div className="space-y-5">
        <div className="space-y-4">
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

          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              Base URL
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
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              {t('globalProviders.form.agentTypes')}
            </label>
            <div className="flex flex-wrap gap-2">
              {['claudecode', 'codex', 'gemini', 'opencode', 'cursor', 'kimi', 'qoder', 'acp'].map(at => {
                const selected = form.agent_types?.includes(at) ?? false;
                return (
                  <button
                    key={at}
                    type="button"
                    onClick={() => {
                      const current = form.agent_types || [];
                      set('agent_types', selected ? current.filter(x => x !== at) : [...current, at]);
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
