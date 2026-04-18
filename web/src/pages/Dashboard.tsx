import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import {
  Activity, Server, Layers, MessageSquare, Clock, ChevronRight,
} from 'lucide-react';
import { StatCard, Badge, EmptyState } from '@/components/ui';
import { getStatus, type SystemStatus } from '@/api/status';
import { listProjects, type ProjectSummary } from '@/api/projects';
import { listSessions, type Session } from '@/api/sessions';
import { formatUptime, formatTime } from '@/lib/utils';

const MAX_ITEMS = 4;

export default function Dashboard() {
  const { t } = useTranslation();
  const [status, setStatus] = useState<SystemStatus | null>(null);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [recentSessions, setRecentSessions] = useState<(Session & { project: string })[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const fetchData = useCallback(async () => {
    try {
      setLoading(true);
      setError('');
      const [s, p] = await Promise.all([getStatus(), listProjects()]);
      setStatus(s);
      const projs = p.projects || [];
      setProjects(projs);

      const sessResults = await Promise.allSettled(
        projs.map(proj => listSessions(proj.name).then(r => ({ project: proj.name, sessions: r.sessions || [] })))
      );
      const allSessions: (Session & { project: string })[] = [];
      for (const r of sessResults) {
        if (r.status === 'fulfilled') {
          for (const sess of r.value.sessions) {
            allSessions.push({ ...sess, project: r.value.project });
          }
        }
      }
      allSessions.sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime());
      setRecentSessions(allSessions.slice(0, MAX_ITEMS));
    } catch (e: any) {
      setError(e.message);
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

  if (loading && !status) {
    return <div className="flex items-center justify-center h-64 text-gray-400"><Activity className="animate-pulse" size={24} /></div>;
  }

  if (error) {
    return <div className="text-center py-16 text-red-500">{error}</div>;
  }

  return (
    <div className="space-y-8 animate-fade-in">
      {/* Stats */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <StatCard label={t('dashboard.version')} value={status?.version || '-'} accent />
        <StatCard label={t('dashboard.uptime')} value={status ? formatUptime(status.uptime_seconds) : '-'} />
        <StatCard label={t('dashboard.platforms')} value={status?.connected_platforms?.length ?? 0} />
        <StatCard label={t('dashboard.projects')} value={status?.projects_count ?? 0} />
      </div>

      {/* Projects */}
      <section>
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white flex items-center gap-1.5">
            <Server size={14} className="text-gray-400" />
            {t('nav.projects')}
          </h3>
          <Link to="/projects" className="text-xs text-accent hover:underline flex items-center gap-0.5">
            {t('common.viewAll')} <ChevronRight size={12} />
          </Link>
        </div>
        {projects.length === 0 ? (
          <div className="rounded-xl border border-gray-200/80 dark:border-white/[0.06] bg-white dark:bg-white/[0.02] p-8">
            <EmptyState message={t('projects.noProjects')} icon={Layers} />
          </div>
        ) : (
          <div className="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-4 gap-3">
            {projects.slice(0, MAX_ITEMS).map((p) => (
              <Link
                key={p.name}
                to={`/chat/${p.name}`}
                className="block p-4 rounded-xl border border-gray-200/80 dark:border-white/[0.06] bg-white dark:bg-white/[0.02] hover:border-accent/40 hover:shadow-md hover:shadow-accent/5 transition-all"
              >
                <div className="flex items-center gap-2.5 mb-3">
                  <div className="w-8 h-8 rounded-lg bg-accent/10 flex items-center justify-center shrink-0">
                    <Server size={14} className="text-accent" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-semibold text-gray-900 dark:text-white truncate">{p.name}</p>
                    <p className="text-xs text-gray-400 font-mono">{p.agent_type}</p>
                  </div>
                </div>
                <div className="flex flex-wrap gap-1 mb-2">
                  {p.platforms?.slice(0, 3).map((pl) => (
                    <Badge key={pl} className="text-xs">{pl}</Badge>
                  ))}
                  {(p.platforms?.length ?? 0) > 3 && (
                    <Badge className="text-xs">+{p.platforms!.length - 3}</Badge>
                  )}
                </div>
                <p className="text-xs text-gray-400">{p.sessions_count} sessions</p>
              </Link>
            ))}
          </div>
        )}
      </section>

      {/* Recent Sessions */}
      <section>
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white flex items-center gap-1.5">
            <MessageSquare size={14} className="text-gray-400" />
            {t('dashboard.recentSessions')}
          </h3>
          <Link to="/chat" className="text-xs text-accent hover:underline flex items-center gap-0.5">
            {t('common.viewAll')} <ChevronRight size={12} />
          </Link>
        </div>
        {recentSessions.length === 0 ? (
          <div className="rounded-xl border border-gray-200/80 dark:border-white/[0.06] bg-white dark:bg-white/[0.02] p-8">
            <p className="text-xs text-gray-400 text-center">{t('sessions.noSessions')}</p>
          </div>
        ) : (
          <div className="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-4 gap-3">
            {recentSessions.map((sess) => (
              <Link
                key={`${sess.project}-${sess.id}`}
                to={`/chat/${sess.project}`}
                className="block p-4 rounded-xl border border-gray-200/80 dark:border-white/[0.06] bg-white dark:bg-white/[0.02] hover:border-accent/40 hover:shadow-md hover:shadow-accent/5 transition-all"
              >
                <div className="flex items-center gap-2.5 mb-3">
                  <div className={`w-2 h-2 rounded-full shrink-0 ${sess.active ? 'bg-green-400' : 'bg-gray-300 dark:bg-gray-600'}`} />
                  <p className="text-sm font-medium text-gray-900 dark:text-white truncate">
                    {sess.name || sess.id}
                  </p>
                </div>
                <Badge className="text-xs mb-2">{sess.project}</Badge>
                {sess.last_message && (
                  <p className="text-xs text-gray-400 truncate mb-2">
                    {sess.last_message.content.slice(0, 60)}
                  </p>
                )}
                <div className="flex items-center gap-1 text-xs text-gray-400">
                  <Clock size={10} />
                  {formatTime(sess.updated_at)}
                </div>
              </Link>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
