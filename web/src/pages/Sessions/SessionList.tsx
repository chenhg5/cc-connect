import { useEffect, useState, useCallback, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { MessageSquare, Circle, Filter, User, Bot } from 'lucide-react';
import { Badge, EmptyState } from '@/components/ui';
import { listProjects, type ProjectSummary } from '@/api/projects';
import { listSessions, type Session } from '@/api/sessions';
import { cn } from '@/lib/utils';

interface FlatSession extends Session {
  _project: string;
}

function timeAgo(iso: string, t: (k: string) => string): string {
  if (!iso) return '';
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return t('sessions.justNow');
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  return `${days}d`;
}

export default function SessionList() {
  const { t } = useTranslation();
  const [allData, setAllData] = useState<{ project: string; sessions: Session[] }[]>([]);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [selectedProject, setSelectedProject] = useState<string>('');
  const [loading, setLoading] = useState(true);

  const fetchData = useCallback(async () => {
    setLoading(true);
    try {
      const { projects: projs } = await listProjects();
      setProjects(projs || []);
      const results = await Promise.all(
        (projs || []).map(async (p) => {
          try {
            const { sessions } = await listSessions(p.name);
            return { project: p.name, sessions: sessions || [] };
          } catch {
            return { project: p.name, sessions: [] };
          }
        })
      );
      setAllData(results);
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

  const filtered = useMemo<FlatSession[]>(() => {
    const src = selectedProject
      ? allData.filter((d) => d.project === selectedProject)
      : allData;
    return src
      .flatMap((d) => d.sessions.map((s) => ({ ...s, _project: d.project })))
      .sort((a, b) => (b.updated_at || b.created_at || '').localeCompare(a.updated_at || a.created_at || ''));
  }, [allData, selectedProject]);

  if (loading && allData.length === 0) {
    return <div className="flex items-center justify-center h-64 text-gray-400 animate-pulse">Loading...</div>;
  }

  return (
    <div className="space-y-4 animate-fade-in">
      {/* Filter bar */}
      <div className="flex items-center gap-3">
        <Filter size={16} className="text-gray-400" />
        <select
          value={selectedProject}
          onChange={(e) => setSelectedProject(e.target.value)}
          className="px-3 py-1.5 text-sm rounded-lg border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-800 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/50"
        >
          <option value="">{t('sessions.allProjects')}</option>
          {projects.map((p) => (
            <option key={p.name} value={p.name}>{p.name}</option>
          ))}
        </select>
        <span className="text-xs text-gray-400">
          {filtered.length} {t('nav.sessions').toLowerCase()}
        </span>
      </div>

      {filtered.length === 0 ? (
        <EmptyState message={t('sessions.noSessions')} icon={MessageSquare} />
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4 gap-3">
          {filtered.map((s) => (
            <Link key={`${s._project}-${s.id}`} to={`/sessions/${s._project}/${s.id}`}>
              <div className={cn(
                'group relative rounded-xl border p-4 transition-all duration-200 cursor-pointer h-full',
                'bg-white/60 dark:bg-white/[0.03] backdrop-blur-sm',
                'border-gray-200/80 dark:border-white/[0.06]',
                'hover:border-accent/40 hover:shadow-md hover:shadow-accent/5 hover:-translate-y-0.5',
              )}>
                {/* Top: name + time */}
                <div className="flex items-start justify-between gap-2 mb-2">
                  <div className="flex items-center gap-1.5 min-w-0">
                    <MessageSquare size={14} className={s.live ? 'text-accent shrink-0' : 'text-gray-400 shrink-0'} />
                    <span className="text-sm font-medium text-gray-900 dark:text-white truncate">
                      {s.name || s.user_name || s.id.slice(0, 8)}
                    </span>
                    {s.live && <Circle size={5} className="fill-emerald-500 text-emerald-500 shrink-0" />}
                  </div>
                  <span className="text-[10px] text-gray-400 shrink-0 mt-0.5">
                    {timeAgo(s.updated_at || s.created_at, t)}
                  </span>
                </div>

                {/* Last message preview */}
                <div className="mb-2.5 min-h-[2.5rem]">
                  {s.last_message ? (
                    <p className="text-xs text-gray-500 dark:text-gray-400 line-clamp-2 leading-relaxed">
                      {s.last_message.role === 'user' ? (
                        <User size={10} className="inline mr-1 -mt-0.5 opacity-60" />
                      ) : (
                        <Bot size={10} className="inline mr-1 -mt-0.5 opacity-60" />
                      )}
                      {s.last_message.content.replace(/\n/g, ' ').slice(0, 100)}
                    </p>
                  ) : (
                    <p className="text-xs text-gray-400 dark:text-gray-500 italic">{t('sessions.noMessages')}</p>
                  )}
                </div>

                {/* Bottom: badges + count */}
                <div className="flex items-center gap-1.5 flex-wrap">
                  <Badge>{s._project}</Badge>
                  {s.platform && <Badge variant="info">{s.platform}</Badge>}
                  <span className="text-[10px] text-gray-400 ml-auto">{s.history_count} msgs</span>
                </div>
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
