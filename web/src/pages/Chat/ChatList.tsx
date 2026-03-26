import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { MessageSquare, Bot, User, Circle, FolderKanban } from 'lucide-react';
import { EmptyState, Badge } from '@/components/ui';
import { listProjects, type ProjectSummary } from '@/api/projects';
import { listSessions, type Session } from '@/api/sessions';
import { cn } from '@/lib/utils';

interface ChatEntry {
  project: ProjectSummary;
  latestSession: Session | null;
}

function timeAgo(iso: string, t: (k: string) => string): string {
  if (!iso) return '';
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return t('sessions.justNow');
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

export default function ChatList() {
  const { t } = useTranslation();
  const [entries, setEntries] = useState<ChatEntry[]>([]);
  const [loading, setLoading] = useState(true);

  const fetchData = useCallback(async () => {
    setLoading(true);
    try {
      const { projects: projs } = await listProjects();
      if (!projs?.length) {
        setEntries([]);
        return;
      }
      const results = await Promise.all(
        projs.map(async (p) => {
          try {
            const { sessions } = await listSessions(p.name);
            const sorted = (sessions || []).sort(
              (a, b) => (b.updated_at || b.created_at || '').localeCompare(a.updated_at || a.created_at || ''),
            );
            return { project: p, latestSession: sorted[0] || null };
          } catch {
            return { project: p, latestSession: null };
          }
        }),
      );
      results.sort((a, b) => {
        const ta = a.latestSession?.updated_at || a.latestSession?.created_at || '';
        const tb = b.latestSession?.updated_at || b.latestSession?.created_at || '';
        return tb.localeCompare(ta);
      });
      setEntries(results);
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

  if (loading && entries.length === 0) {
    return <div className="flex items-center justify-center h-64 text-gray-400 animate-pulse">Loading...</div>;
  }

  return (
    <div className="max-w-2xl mx-auto space-y-1 animate-fade-in">
      {entries.length === 0 ? (
        <EmptyState message={t('chat.noChats')} icon={MessageSquare} />
      ) : (
        entries.map(({ project, latestSession }) => {
          const hasLive = latestSession?.live;
          const lastMsg = latestSession?.last_message;
          const ts = latestSession?.updated_at || latestSession?.created_at || '';

          return (
            <Link key={project.name} to={`/chat/${project.name}`}>
              <div
                className={cn(
                  'group flex items-center gap-4 px-4 py-3.5 rounded-xl transition-all duration-200 cursor-pointer',
                  'hover:bg-gray-100/70 dark:hover:bg-white/[0.04]',
                )}
              >
                {/* Avatar */}
                <div
                  className={cn(
                    'w-12 h-12 rounded-2xl flex items-center justify-center shrink-0',
                    'bg-accent/10 ring-1 ring-accent/20',
                  )}
                >
                  <FolderKanban size={22} className="text-accent" />
                </div>

                {/* Content */}
                <div className="flex-1 min-w-0">
                  <div className="flex items-center justify-between gap-2 mb-0.5">
                    <div className="flex items-center gap-2 min-w-0">
                      <span className="text-sm font-semibold text-gray-900 dark:text-white truncate">
                        {project.name}
                      </span>
                      {hasLive && <Circle size={5} className="fill-emerald-500 text-emerald-500 shrink-0" />}
                    </div>
                    {ts && (
                      <span className="text-[11px] text-gray-400 shrink-0">{timeAgo(ts, t)}</span>
                    )}
                  </div>

                  <div className="flex items-center gap-2">
                    <div className="flex-1 min-w-0">
                      {lastMsg ? (
                        <p className="text-xs text-gray-500 dark:text-gray-400 truncate leading-relaxed">
                          {lastMsg.role === 'user' ? (
                            <User size={10} className="inline mr-1 -mt-0.5 opacity-60" />
                          ) : (
                            <Bot size={10} className="inline mr-1 -mt-0.5 opacity-60" />
                          )}
                          {lastMsg.content.replace(/\n/g, ' ').slice(0, 80)}
                        </p>
                      ) : (
                        <p className="text-xs text-gray-400 dark:text-gray-500 italic">
                          {t('chat.noMessages')}
                        </p>
                      )}
                    </div>
                    <div className="flex items-center gap-1 shrink-0">
                      <Badge className="text-[9px]">{project.agent_type}</Badge>
                      <span className="text-[10px] text-gray-400">
                        {project.sessions_count}
                      </span>
                    </div>
                  </div>
                </div>
              </div>
            </Link>
          );
        })
      )}
    </div>
  );
}
