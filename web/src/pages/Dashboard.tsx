import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Activity, Server, Layers } from 'lucide-react';
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

      {/* Projects */}
      <div>
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white">{t('nav.projects')}</h3>
          <Link to="/projects" className="text-xs text-accent hover:underline">{t('common.viewAll')}</Link>
        </div>
        {projects.length === 0 ? (
          <Card><EmptyState message={t('projects.noProjects')} icon={Layers} /></Card>
        ) : (
          <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-4">
            {projects.map((p) => (
              <Link
                key={p.name}
                to={`/projects/${p.name}`}
                className="block p-4 rounded-xl border border-gray-200/80 dark:border-white/[0.06] bg-white dark:bg-white/[0.02] hover:border-accent/40 hover:shadow-md hover:shadow-accent/5 transition-all group"
              >
                <div className="flex items-center gap-3 mb-3">
                  <div className="w-9 h-9 rounded-lg bg-accent/10 flex items-center justify-center shrink-0">
                    <Server size={16} className="text-accent" />
                  </div>
                  <div className="min-w-0">
                    <p className="text-sm font-semibold text-gray-900 dark:text-white truncate">{p.name}</p>
                    <p className="text-[11px] text-gray-400 font-mono">{p.agent_type}</p>
                  </div>
                </div>
                <div className="flex flex-wrap gap-1.5 mb-2">
                  {p.platforms?.map((pl) => (
                    <Badge key={pl} variant="info" className="text-[10px] px-1.5 py-0">{pl}</Badge>
                  ))}
                </div>
                <div className="flex items-center justify-between text-[11px] text-gray-400">
                  <span>{p.sessions_count} sessions</span>
                  {p.heartbeat_enabled && <Badge variant="success" className="text-[10px] px-1.5 py-0">heartbeat</Badge>}
                </div>
              </Link>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
