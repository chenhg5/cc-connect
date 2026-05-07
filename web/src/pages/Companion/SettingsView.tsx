import { useState, useEffect, useCallback } from 'react';
import { Loader2, WifiOff, RefreshCw, Circle } from 'lucide-react';
import { getStatus, type SystemStatus } from '@/api/status';
import { listProjects, getProject, type ProjectSummary, type ProjectDetail } from '@/api/projects';
import { listProviders, listModels, type Provider } from '@/api/providers';
import { listSessions, type Session } from '@/api/sessions';
import { getAeliosStatus, type AeliosStatus } from '@/api/aelios';
import { cn, formatUptime } from '@/lib/utils';

// ── Data shape ────────────────────────────────────────────────

interface SettingsData {
  system: SystemStatus | null;
  project: ProjectSummary | null;
  projectDetail: ProjectDetail | null;
  providers: Provider[];
  activeProvider: string;
  currentModel: string;
  companionSession: Session | null;
  aelios: AeliosStatus | null;
}

// ── Component ─────────────────────────────────────────────────

export default function SettingsView() {
  const [data, setData] = useState<SettingsData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      // 1. System status + project list (parallel)
      const [sysRes, projRes] = await Promise.all([
        getStatus().catch(() => null),
        listProjects().catch(() => null),
      ]);

      const projects = projRes?.projects || [];
      const project = projects[0] || null;

      // 2. If we have a project, fetch detail + providers + models + sessions in parallel
      let projectDetail: ProjectDetail | null = null;
      let providers: Provider[] = [];
      let activeProvider = '';
      let currentModel = '';
      let companionSession: Session | null = null;
      let aelios: AeliosStatus | null = null;

      if (project) {
        const companionKey = `bridge:aelios-companion:${project.name}`;
        const results = await Promise.allSettled([
          getProject(project.name),
          listProviders(project.name),
          listModels(project.name),
          listSessions(project.name),
          getAeliosStatus(),
        ]);

        if (results[0].status === 'fulfilled') projectDetail = results[0].value;
        if (results[1].status === 'fulfilled') {
          providers = results[1].value.providers || [];
          activeProvider = results[1].value.active_provider || '';
        }
        if (results[2].status === 'fulfilled') {
          currentModel = results[2].value.current || '';
        }
        if (results[3].status === 'fulfilled') {
          const sessions = results[3].value.sessions || [];
          companionSession = sessions.find(s => s.session_key === companionKey) || null;
        }
        if (results[4].status === 'fulfilled') {
          aelios = results[4].value;
        }
      } else {
        // No project — still try aelios status
        aelios = await getAeliosStatus().catch(() => null);
      }

      // If all three top-level sources failed, surface a visible error
      if (!sysRes && !project && !aelios) {
        setError('Failed to load status — server may be unreachable');
        setData(null);
      } else {
        setData({
          system: sysRes,
          project,
          projectDetail,
          providers,
          activeProvider,
          currentModel,
          companionSession,
          aelios,
        });
      }
    } catch (e: any) {
      setError(e?.message || 'Failed to load settings');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { fetchData(); }, [fetchData]);

  // ── Loading ─────────────────────────────────────────────────

  if (loading) {
    return (
      <div className="flex flex-col h-full bg-[#f6f1e7] items-center justify-center">
        <Loader2 size={24} className="text-[#9e9590] animate-spin" />
        <p className="mt-3 text-xs font-mono text-[#9e9590]">Loading status...</p>
      </div>
    );
  }

  // ── Fatal error ─────────────────────────────────────────────

  if (error && !data) {
    return (
      <div className="flex flex-col h-full bg-[#f6f1e7] items-center justify-center px-8 text-center">
        <WifiOff size={28} className="text-[#b5b0a8] mb-3" />
        <p className="text-sm text-[#6b6560]">{error}</p>
        <button onClick={fetchData} className="mt-3 text-xs font-mono text-[#e85d3a] hover:underline">
          Retry
        </button>
      </div>
    );
  }

  const sys = data?.system;
  const proj = data?.project;
  const detail = data?.projectDetail;
  const session = data?.companionSession;
  const aelios = data?.aelios;

  return (
    <div className="flex flex-col h-full bg-[#f6f1e7]">
      {/* Header */}
      <div className="flex-shrink-0 px-5 pt-4 pb-3 flex items-end justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl font-medium text-[#1a1915] tracking-tight leading-none">
            Settings
          </h1>
          <p className="text-[11px] font-mono text-[#9e9590] mt-1 tracking-wider uppercase">
            Status &amp; info
          </p>
        </div>
        <button
          onClick={fetchData}
          className="w-9 h-9 rounded-full border border-[#e5dfd5] bg-[#fffcf5] flex items-center justify-center text-[#6b6560] hover:bg-[#ede8db] transition-colors"
        >
          <RefreshCw size={16} />
        </button>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto px-4 pb-6">
        {/* cc-connect section */}
        {sys && (
          <SettingsGroup label="cc-connect">
            <SettingsRow label="Version">
              <span className="text-xs font-mono text-[#6b6560]">{sys.version}</span>
            </SettingsRow>
            <SettingsRow label="Uptime">
              <span className="text-xs font-mono text-[#6b6560]">{formatUptime(sys.uptime_seconds)}</span>
            </SettingsRow>
            <SettingsRow label="Platforms">
              <div className="flex flex-wrap gap-1 justify-end">
                {sys.connected_platforms.length > 0 ? sys.connected_platforms.map(p => (
                  <span key={p} className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-[#ede8db] text-[#6b6560]">{p}</span>
                )) : <span className="text-xs font-mono text-[#9e9590]">none</span>}
              </div>
            </SettingsRow>
            <SettingsRow label="Bridge adapters">
              <span className="text-xs font-mono text-[#6b6560]">
                {sys.bridge_adapters?.length || 0}
                {sys.bridge_adapters?.length > 0 && (
                  <span className="text-[#9e9590] ml-1">
                    ({sys.bridge_adapters.map(a => a.platform).join(', ')})
                  </span>
                )}
              </span>
            </SettingsRow>
          </SettingsGroup>
        )}

        {/* Project section */}
        {proj && (
          <SettingsGroup label="Project">
            <SettingsRow label="Name">
              <span className="text-xs font-mono text-[#6b6560]">{proj.name}</span>
            </SettingsRow>
            <SettingsRow label="Agent">
              <span className="text-xs font-mono text-[#6b6560]">{proj.agent_type}</span>
            </SettingsRow>
            {detail?.work_dir && (
              <SettingsRow label="Work dir">
                <span className="text-xs font-mono text-[#6b6560] truncate max-w-[200px]">{detail.work_dir}</span>
              </SettingsRow>
            )}
            {detail?.agent_mode && (
              <SettingsRow label="Mode">
                <span className="text-xs font-mono text-[#6b6560]">{detail.agent_mode}</span>
              </SettingsRow>
            )}
            <SettingsRow label="Platforms">
              <div className="flex flex-col items-end gap-0.5">
                {(detail?.platforms || proj.platforms.map(p => ({ type: p, connected: true }))).map(p => (
                  <div key={typeof p === 'string' ? p : p.type} className="flex items-center gap-1.5">
                    <Circle size={5} className={cn('fill-current', (typeof p === 'string' || p.connected) ? 'text-[#2d6a4f]' : 'text-[#9e9590]')} />
                    <span className="text-xs font-mono text-[#6b6560]">{typeof p === 'string' ? p : p.type}</span>
                  </div>
                ))}
              </div>
            </SettingsRow>
            {detail?.heartbeat && (
              <SettingsRow label="Heartbeat">
                <span className="text-xs font-mono text-[#6b6560]">
                  {detail.heartbeat.enabled
                    ? detail.heartbeat.paused ? 'paused' : `every ${detail.heartbeat.interval_mins}m`
                    : 'off'}
                </span>
              </SettingsRow>
            )}
          </SettingsGroup>
        )}

        {/* Provider / Model section */}
        {proj && (
          <SettingsGroup label="Provider & Model">
            <SettingsRow label="Active provider">
              <span className="text-xs font-mono text-[#6b6560]">{data?.activeProvider || 'none'}</span>
            </SettingsRow>
            <SettingsRow label="Current model">
              <span className="text-xs font-mono text-[#6b6560]">{data?.currentModel || 'default'}</span>
            </SettingsRow>
            {data?.providers && data.providers.length > 0 && (
              <SettingsRow label="Providers">
                <div className="flex flex-col items-end gap-0.5">
                  {data.providers.map(p => (
                    <div key={p.name} className="flex items-center gap-1.5">
                      {p.active && <Circle size={5} className="fill-current text-[#2d6a4f]" />}
                      <span className={cn('text-xs font-mono', p.active ? 'text-[#6b6560] font-semibold' : 'text-[#9e9590]')}>
                        {p.name}
                      </span>
                    </div>
                  ))}
                </div>
              </SettingsRow>
            )}
          </SettingsGroup>
        )}

        {/* Companion Session section */}
        {proj && (
          <SettingsGroup label="Companion Session">
            {session ? (
              <>
                <SettingsRow label="Session">
                  <span className="text-xs font-mono text-[#6b6560]">{session.name || session.id.slice(0, 8)}</span>
                </SettingsRow>
                <SettingsRow label="Live">
                  <span className={cn('text-xs font-mono', session.live ? 'text-[#2d6a4f]' : 'text-[#9e9590]')}>
                    {session.live ? 'yes' : 'no'}
                  </span>
                </SettingsRow>
                <SettingsRow label="History">
                  <span className="text-xs font-mono text-[#6b6560]">{session.history_count} messages</span>
                </SettingsRow>
                <SettingsRow label="Updated">
                  <span className="text-xs font-mono text-[#6b6560]">
                    {session.updated_at ? new Date(session.updated_at).toLocaleString() : '-'}
                  </span>
                </SettingsRow>
              </>
            ) : (
              <div className="px-3.5 py-3">
                <p className="text-xs text-[#9e9590] italic">Not started yet</p>
              </div>
            )}
          </SettingsGroup>
        )}

        {/* Aelios section */}
        {aelios && (
          <SettingsGroup label="Aelios Storage">
            <SettingsRow label="Status">
              <div className="flex items-center gap-1.5">
                <Circle size={5} className="fill-current text-[#2d6a4f]" />
                <span className="text-xs font-mono text-[#6b6560]">{aelios.cc_connect}</span>
              </div>
            </SettingsRow>
            <SettingsRow label="Storage">
              <span className="text-xs font-mono text-[#6b6560]">{aelios.storage}</span>
            </SettingsRow>
            <SettingsRow label="Data dir">
              <span className="text-xs font-mono text-[#6b6560] truncate max-w-[200px]">{aelios.data_dir}</span>
            </SettingsRow>
            <SettingsRow label="Memory">
              <span className="text-xs font-mono text-[#9e9590]">{aelios.memory_adapter}</span>
            </SettingsRow>
          </SettingsGroup>
        )}

        {/* Footer */}
        <p className="text-center pt-5 font-serif text-xs text-[#b5b0a8] italic">
          Aelios Companion · v0.1 · {new Date().getFullYear()}-{String(new Date().getMonth() + 1).padStart(2, '0')}
        </p>
      </div>
    </div>
  );
}

// ── Shared components ─────────────────────────────────────────

function SettingsGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="mt-4 first:mt-0">
      <p className="text-[10px] font-mono text-[#9e9590] tracking-widest uppercase px-1 pb-2">
        {label}
      </p>
      <div className="rounded-xl overflow-hidden bg-[#fffcf5] border border-[#e5dfd5]">
        {children}
      </div>
    </div>
  );
}

function SettingsRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between px-3.5 py-2.5 border-b border-[#e5dfd5] last:border-b-0 gap-3">
      <span className="text-sm text-[#6b6560] flex-shrink-0">{label}</span>
      {children}
    </div>
  );
}
