import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Activity, Server, Layers, Cable, ArrowRight } from 'lucide-react';
import { Card, StatCard, Badge, EmptyState } from '@/components/ui';
import { getStatus, type SystemStatus } from '@/api/status';
import { listProjects, type ProjectSummary } from '@/api/projects';
import { formatUptime } from '@/lib/utils';

export default function Dashboard() {
  const { t } = useTranslation();
  const [status, setStatus] = useState<SystemStatus | null>(null);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const fetchData = useCallback(async () => {
    try {
      setLoading(true);
      setError('');
      const [s, p] = await Promise.all([getStatus(), listProjects()]);
      setStatus(s);
      setProjects(p.projects || []);
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
    <div className="space-y-6 animate-fade-in">
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <StatCard label={t('dashboard.version')} value={status?.version || '-'} accent />
        <StatCard label={t('dashboard.uptime')} value={status ? formatUptime(status.uptime_seconds) : '-'} />
        <StatCard label={t('dashboard.platforms')} value={status?.connected_platforms?.length ?? 0} />
        <StatCard label={t('dashboard.projects')} value={status?.projects_count ?? 0} />
      </div>

      {/* Bridge adapters */}
      {status?.bridge_adapters && status.bridge_adapters.length > 0 && (
        <Card>
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-3">{t('dashboard.bridgeAdapters')}</h3>
          <div className="flex flex-wrap gap-2">
            {status.bridge_adapters.map((a, i) => (
              <Badge key={i} variant="info">{a.platform} → {a.project}</Badge>
            ))}
          </div>
        </Card>
      )}

      {/* Project list */}
      <Card>
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white">{t('nav.projects')}</h3>
          <Link to="/projects" className="text-xs text-accent hover:underline">{t('common.viewAll')}</Link>
        </div>
        {projects.length === 0 ? (
          <EmptyState message={t('projects.noProjects')} icon={Layers} />
        ) : (
          <div className="space-y-2">
            {projects.map((p) => (
              <Link
                key={p.name}
                to={`/projects/${p.name}`}
                className="flex items-center justify-between p-3 rounded-lg hover:bg-gray-50 dark:hover:bg-gray-800/50 transition-colors group"
              >
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 rounded-lg bg-gray-100 dark:bg-gray-800 flex items-center justify-center">
                    <Server size={16} className="text-gray-500 dark:text-gray-400" />
                  </div>
                  <div>
                    <p className="text-sm font-medium text-gray-900 dark:text-white">{p.name}</p>
                    <p className="text-xs text-gray-500 dark:text-gray-400">
                      {p.agent_type} · {p.platforms?.join(', ')} · {p.sessions_count} {t('nav.sessions').toLowerCase()}
                    </p>
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  {p.heartbeat_enabled && <Badge variant="success">heartbeat</Badge>}
                  <ArrowRight size={16} className="text-gray-300 dark:text-gray-600 group-hover:text-accent transition-colors" />
                </div>
              </Link>
            ))}
          </div>
        )}
      </Card>
    </div>
  );
}
