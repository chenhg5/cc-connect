import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { MessageSquare, Bot, User, Circle, ArrowRight } from 'lucide-react';
import { Card, EmptyState, Badge } from '@/components/ui';
import { listProjects, type ProjectSummary } from '@/api/projects';
import { listSessions, type Session } from '@/api/sessions';

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
    <div className="animate-fade-in space-y-4">
      <h2 className="text-lg font-bold text-gray-900 dark:text-white">{t('nav.chat')}</h2>

      {entries.length === 0 ? (
        <EmptyState message={t('chat.noChats')} icon={MessageSquare} />
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
          {entries.map(({ project, latestSession }) => {
            const hasLive = latestSession?.live;
            const lastMsg = latestSession?.last_message;
            const ts = latestSession?.updated_at || latestSession?.created_at || '';

            return (
              <Link key={project.name} to={`/chat/${project.name}`}>
                <Card hover className="h-full flex flex-col">
                  <div className="flex items-start justify-between mb-3">
                    <div className="flex items-center gap-2">
                      <MessageSquare size={18} className="text-accent" />
                      <h3 className="font-semibold text-gray-900 dark:text-white">{project.name}</h3>
                      {hasLive && <Circle size={6} className="fill-emerald-500 text-emerald-500" />}
                    </div>
                    <ArrowRight size={16} className="text-gray-300 dark:text-gray-600" />
                  </div>

                  <div className="flex-1 min-h-[2rem] mb-3">
                    {lastMsg ? (
                      <p className="text-xs text-gray-500 dark:text-gray-400 line-clamp-2 leading-relaxed">
                        {lastMsg.role === 'user' ? (
                          <User size={10} className="inline mr-1 -mt-0.5 opacity-60" />
                        ) : (
                          <Bot size={10} className="inline mr-1 -mt-0.5 opacity-60" />
                        )}
                        {lastMsg.content.replace(/\n/g, ' ').slice(0, 120)}
                      </p>
                    ) : (
                      <p className="text-xs text-gray-400 dark:text-gray-500 italic">
                        {t('chat.noMessages')}
                      </p>
                    )}
                  </div>

                  <div className="flex items-center justify-between text-xs text-gray-500 dark:text-gray-400 mt-auto pt-3 border-t border-gray-100 dark:border-gray-800">
                    <div className="flex items-center gap-1.5">
                      <Badge className="text-[9px]">{project.agent_type}</Badge>
                      {project.platforms?.slice(0, 2).map((pl) => <Badge key={pl}>{pl}</Badge>)}
                      {(project.platforms?.length ?? 0) > 2 && (
                        <Badge>+{project.platforms!.length - 2}</Badge>
                      )}
                    </div>
                    <div className="flex items-center gap-2">
                      <span>{project.sessions_count} {t('chat.sessions', 'sessions')}</span>
                      {ts && <span className="text-gray-400">{timeAgo(ts, t)}</span>}
                    </div>
                  </div>
                </Card>
              </Link>
            );
          })}
        </div>
      )}
    </div>
  );
}
