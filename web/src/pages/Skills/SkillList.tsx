import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Sparkles, Star, ExternalLink, FolderOpen, Puzzle, RefreshCw,
} from 'lucide-react';
import { Card, Badge, Button } from '@/components/ui';
import {
  listSkills, fetchSkillPresets,
  type ProjectSkills, type SkillPreset,
} from '@/api/skills';
import { cn } from '@/lib/utils';

type Tab = 'local' | 'recommended';

export default function SkillList() {
  const { t, i18n } = useTranslation();
  const [tab, setTab] = useState<Tab>('local');
  const [projects, setProjects] = useState<ProjectSkills[]>([]);
  const [presets, setPresets] = useState<SkillPreset[]>([]);
  const [loading, setLoading] = useState(true);
  const [presetsLoading, setPresetsLoading] = useState(false);
  const [activeProject, setActiveProject] = useState('');

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const data = await listSkills();
      const ps = data.projects || [];
      setProjects(ps);
      if (ps.length > 0 && !activeProject) setActiveProject(ps[0].project);
    } catch { /* empty */ }
    setLoading(false);
  }, [activeProject]);

  const loadPresets = useCallback(async () => {
    setPresetsLoading(true);
    try {
      const data = await fetchSkillPresets();
      setPresets(data.skills || []);
    } catch { /* empty */ }
    setPresetsLoading(false);
  }, []);

  useEffect(() => { refresh(); }, []);
  useEffect(() => {
    if (tab === 'recommended' && presets.length === 0) loadPresets();
  }, [tab, presets.length, loadPresets]);

  const current = projects.find(p => p.project === activeProject);
  const lang = i18n.language || 'en';

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-gray-900 dark:text-white">
          {t('skills.title')}
        </h1>
        <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {t('skills.subtitle')}
        </p>
      </div>

      {/* Tabs + refresh on the same row */}
      <div className="flex items-center justify-between">
        <div className="flex gap-1 p-1 rounded-xl bg-gray-100 dark:bg-white/[0.06] w-fit">
          {(['local', 'recommended'] as const).map(key => (
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
              {t(`skills.tab.${key}`)}
            </button>
          ))}
        </div>
        {tab === 'recommended' && (
          <Button variant="ghost" size="sm" onClick={loadPresets}>
            <RefreshCw size={14} className="mr-1.5" />
            {t('common.refresh')}
          </Button>
        )}
      </div>

      {tab === 'local' && (
        <LocalSkills
          projects={projects}
          current={current}
          activeProject={activeProject}
          onSelectProject={setActiveProject}
          loading={loading}
          t={t}
        />
      )}
      {tab === 'recommended' && (
        <RecommendedSkills
          presets={presets}
          loading={presetsLoading}
          onRefresh={loadPresets}
          t={t}
          lang={lang}
        />
      )}
    </div>
  );
}

/* ── Local Skills ── */

function LocalSkills({
  projects, current, activeProject, onSelectProject, loading, t,
}: {
  projects: ProjectSkills[];
  current?: ProjectSkills;
  activeProject: string;
  onSelectProject: (name: string) => void;
  loading: boolean;
  t: (k: string, opts?: any) => string;
}) {
  if (loading) {
    return (
      <div className="flex justify-center py-16">
        <div className="animate-spin rounded-full h-6 w-6 border-2 border-accent border-t-transparent" />
      </div>
    );
  }

  if (projects.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-20 text-center">
        <Puzzle size={40} className="text-gray-300 dark:text-gray-600 mb-3" />
        <p className="text-sm font-medium text-gray-500 dark:text-gray-400">{t('skills.noSkills')}</p>
        <p className="mt-1 text-xs text-gray-400 dark:text-gray-500">{t('skills.noSkillsHint')}</p>
      </div>
    );
  }

  return (
    <div className="flex gap-6 items-start">
      <div className="w-48 shrink-0 sticky top-4 max-h-[calc(100vh-8rem)] overflow-y-auto">
        <p className="text-xs font-medium text-gray-400 dark:text-gray-500 uppercase tracking-wider mb-2 px-2">
          {t('skills.projects')}
        </p>
        <div className="space-y-0.5">
          {projects.map(p => (
            <button
              key={p.project}
              onClick={() => onSelectProject(p.project)}
              className={cn(
                'w-full flex items-center gap-2.5 px-3 py-2 rounded-xl text-sm transition-all text-left',
                activeProject === p.project
                  ? 'bg-accent/10 text-accent font-medium'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-white/[0.06]',
              )}
            >
              <FolderOpen size={15} className="shrink-0" />
              <div className="min-w-0 flex-1">
                <div className="truncate">{p.project}</div>
                <div className="text-[11px] text-gray-400 dark:text-gray-500">{p.agent_type}</div>
              </div>
              <Badge variant="outline" className="text-[10px] shrink-0">{p.skills?.length || 0}</Badge>
            </button>
          ))}
        </div>
      </div>

      <div className="flex-1 min-w-0">
        {current && (
          <>
            <div className="flex items-center gap-2 mb-4">
              <h2 className="text-sm font-medium text-gray-900 dark:text-white">
                {current.project}
              </h2>
              <Badge variant="info">{current.agent_type}</Badge>
              <span className="text-xs text-gray-400">
                {t('skills.skillCount', { count: current.skills?.length || 0 })}
              </span>
            </div>

            {current.dirs && current.dirs.length > 0 && (
              <div className="mb-4 rounded-xl bg-gray-50 dark:bg-white/[0.03] border border-gray-100 dark:border-white/[0.06] px-3 py-2">
                <p className="text-[11px] font-medium text-gray-400 dark:text-gray-500 uppercase tracking-wider mb-1">
                  {t('skills.scanDirs')}
                </p>
                {current.dirs.map(d => (
                  <p key={d} className="text-xs text-gray-500 dark:text-gray-400 font-mono truncate">{d}</p>
                ))}
              </div>
            )}

            {(!current.skills || current.skills.length === 0) ? (
              <div className="flex flex-col items-center justify-center py-16 text-center">
                <Puzzle size={36} className="text-gray-300 dark:text-gray-600 mb-3" />
                <p className="text-sm text-gray-500 dark:text-gray-400">{t('skills.emptyProject')}</p>
              </div>
            ) : (
              <div className="space-y-2">
                {current.skills.map(s => (
                  <Card key={s.name + s.source} className="!py-3">
                    <div className="flex items-start gap-3">
                      <div
                        className={cn(
                          'mt-0.5 w-8 h-8 rounded-lg flex items-center justify-center shrink-0',
                          'bg-purple-50 dark:bg-purple-500/10 text-purple-500 dark:text-purple-400',
                        )}
                      >
                        <Puzzle size={16} />
                      </div>
                      <div className="min-w-0 flex-1">
                        <h3 className="text-sm font-medium text-gray-900 dark:text-white">
                          {s.display_name || s.name}
                        </h3>
                        {s.description && (
                          <p className="mt-0.5 text-xs text-gray-500 dark:text-gray-400 line-clamp-2">
                            {s.description}
                          </p>
                        )}
                        <p className="mt-1 text-[11px] text-gray-400 dark:text-gray-500 font-mono truncate">
                          {s.source}
                        </p>
                      </div>
                    </div>
                  </Card>
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

/* ── Recommended Skills ── */

function RecommendedSkills({
  presets, loading, onRefresh, t, lang,
}: {
  presets: SkillPreset[];
  loading: boolean;
  onRefresh: () => void;
  t: (k: string, opts?: any) => string;
  lang: string;
}) {
  if (loading) {
    return (
      <div className="flex justify-center py-16">
        <div className="animate-spin rounded-full h-6 w-6 border-2 border-accent border-t-transparent" />
      </div>
    );
  }

  if (presets.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-20 text-center">
        <Sparkles size={40} className="text-gray-300 dark:text-gray-600 mb-3" />
        <p className="text-sm font-medium text-gray-500 dark:text-gray-400">{t('skills.noPresets')}</p>
        <p className="mt-1 text-xs text-gray-400 dark:text-gray-500">{t('skills.noPresetsHint')}</p>
        <Button variant="ghost" onClick={onRefresh} className="mt-3">
          <RefreshCw size={14} className="mr-1.5" />
          {t('common.refresh')}
        </Button>
      </div>
    );
  }

  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {presets.map(s => (
        <SkillPresetCard key={s.name} skill={s} t={t} lang={lang} />
      ))}
    </div>
  );
}

/* ── Pricing Badge ── */

function PricingBadge({ pricing, t }: { pricing: SkillPreset['pricing']; t: (k: string) => string }) {
  if (!pricing) return null;

  if (pricing.type === 'free') {
    return (
      <span className={cn(
        'inline-flex items-center px-2 py-0.5 rounded-md text-xs font-semibold',
        'bg-green-50 text-green-600 dark:bg-green-500/10 dark:text-green-400',
      )}>
        {t('skills.free')}
      </span>
    );
  }

  if (pricing.type === 'freemium') {
    return (
      <span className={cn(
        'inline-flex items-center px-2 py-0.5 rounded-md text-xs font-semibold',
        'bg-blue-50 text-blue-600 dark:bg-blue-500/10 dark:text-blue-400',
      )}>
        {t('skills.freemium')}
      </span>
    );
  }

  const price = pricing.price ?? 0;
  const currency = pricing.currency || 'USD';
  const symbol = currency === 'CNY' ? '¥' : currency === 'EUR' ? '€' : '$';

  return (
    <span className={cn(
      'inline-flex items-center px-2 py-0.5 rounded-md text-xs font-semibold',
      'bg-amber-50 text-amber-600 dark:bg-amber-500/10 dark:text-amber-400',
    )}>
      {symbol}{price}
    </span>
  );
}

/* ── Skill Card ── */

function SkillPresetCard({
  skill: s, t, lang,
}: {
  skill: SkillPreset;
  t: (k: string) => string;
  lang: string;
}) {
  return (
    <Card className="relative overflow-hidden flex flex-col">
      {s.featured && (
        <div className="absolute top-0 right-0 bg-amber-400/90 text-white text-[10px] font-bold px-2 py-0.5 rounded-bl-lg">
          <Star size={10} className="inline mr-0.5 -mt-0.5" />
        </div>
      )}

      {/* Body */}
      <div className="space-y-3 flex-1">
        <div>
          <div className="flex items-center gap-2 flex-wrap">
            <h3 className="font-medium text-gray-900 dark:text-white">
              {s.display_name || s.name}
            </h3>
            {s.version && (
              <Badge variant="outline" className="text-[10px]">{s.version}</Badge>
            )}
            <PricingBadge pricing={s.pricing} t={t} />
          </div>
          {(s.description || s.description_zh) && (
            <p className="mt-1.5 text-sm text-gray-500 dark:text-gray-400 line-clamp-2">
              {lang.startsWith('zh') && s.description_zh ? s.description_zh : s.description}
            </p>
          )}
        </div>

        {s.tags && s.tags.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {s.tags.map(tag => (
              <Badge key={tag} variant="success" className="text-xs">{tag}</Badge>
            ))}
          </div>
        )}
      </div>

      {/* Footer: source + author left, download right */}
      <div className="flex items-center justify-between mt-4 pt-3 border-t border-gray-100 dark:border-white/[0.06]">
        <div className="flex items-center gap-3 text-xs text-gray-400 dark:text-gray-500 min-w-0">
          {s.source && (
            <span className="inline-flex items-center gap-1 shrink-0">
              {t('skills.source')}:
              {s.source.url ? (
                <a
                  href={s.source.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-accent hover:underline"
                >
                  {s.source.name || s.source.provider}
                </a>
              ) : (
                <span>{s.source.name || s.source.provider}</span>
              )}
            </span>
          )}
          {s.author && (
            <span className="truncate">{t('skills.author')}: {s.author}</span>
          )}
        </div>
        {s.url && (
          <a
            href={s.url}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium text-accent hover:bg-accent/10 transition-colors shrink-0"
          >
            {t('skills.download')} <ExternalLink size={12} />
          </a>
        )}
      </div>
    </Card>
  );
}
