import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { MessageSquare, ArrowRight, Circle } from 'lucide-react';
import { Card, Badge, EmptyState } from '@/components/ui';
import { listProjects } from '@/api/projects';
import { listSessions, type Session } from '@/api/sessions';
import { formatTime } from '@/lib/utils';

interface ProjectSessions {
  project: string;
  sessions: Session[];
}

export default function SessionList() {
  const { t } = useTranslation();
  const [data, setData] = useState<ProjectSessions[]>([]);
  const [loading, setLoading] = useState(true);

  const fetchData = useCallback(async () => {
    setLoading(true);
    try {
      const { projects } = await listProjects();
      const results = await Promise.all(
        (projects || []).map(async (p) => {
          try {
            const { sessions } = await listSessions(p.name);
            return { project: p.name, sessions: sessions || [] };
          } catch {
            return { project: p.name, sessions: [] };
          }
        })
      );
      setData(results.filter((r) => r.sessions.length > 0));
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

  if (loading && data.length === 0) {
    return <div className="flex items-center justify-center h-64 text-gray-400 animate-pulse">Loading...</div>;
  }

  if (data.length === 0) {
    return <EmptyState message={t('sessions.noSessions')} icon={MessageSquare} />;
  }

  return (
    <div className="space-y-6 animate-fade-in">
      {data.map(({ project, sessions }) => (
        <div key={project}>
          <h3 className="text-sm font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wide mb-3">{project}</h3>
          <div className="space-y-2">
            {sessions.map((s) => (
              <Link key={s.id} to={`/sessions/${project}/${s.id}`}>
                <Card hover>
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-3">
                      <MessageSquare size={18} className="text-gray-400" />
                      <div>
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-medium text-gray-900 dark:text-white">{s.name || s.id}</span>
                          {s.active && <Circle size={8} className="fill-emerald-500 text-emerald-500" />}
                        </div>
                        <p className="text-xs text-gray-500 dark:text-gray-400">{s.session_key}</p>
                      </div>
                    </div>
                    <div className="flex items-center gap-3">
                      <Badge>{s.platform}</Badge>
                      <span className="text-xs text-gray-400">{s.history_count} msgs</span>
                      <span className="text-xs text-gray-400">{formatTime(s.created_at)}</span>
                      <ArrowRight size={16} className="text-gray-300 dark:text-gray-600" />
                    </div>
                  </div>
                </Card>
              </Link>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
