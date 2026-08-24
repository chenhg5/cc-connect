import { api } from './client';

export interface DeviceStatus {
  id: string;
  name: string;
  paired_at: string;
  last_seen_at?: string;
  revoked_at?: string;
  online: boolean;
}

export interface ServiceStatus {
  running: boolean;
  pid?: number;
  started_at?: string;
  exit_error?: string;
}

export interface DeployRun {
  id: string;
  kind: 'update' | 'rollback' | 'restart';
  status: string;
  target_tag?: string;
  commit_sha?: string;
  started_at: string;
  ended_at?: string;
  error?: string;
}

export interface Dashboard {
  service: ServiceStatus;
  deployment: { owner: 'systemd' | 'container'; available: boolean; reason?: 'container_host_unavailable'; detail?: string; update: boolean; rollback: boolean; restart: boolean };
  devices: DeviceStatus[];
  runs: DeployRun[];
  runtime_contract_hash: string;
  control_schema: number;
  current_release_tag?: string;
  runtime_updates: Array<{ device_id: string; target_tag: string; status: string; updated_at: string }>;
  configured: boolean;
  public_url?: string;
  workspace_count: number;
}

export interface PairingCode { code: string; expires_at: string }
export interface DeployLogLine { sequence: number; occurred_at: string; stream: string; line: string }
export interface DeviceLogLine { id: number; occurred_at: string; actor: string; action: string; resource: string; outcome: string; details: unknown }

export const getControlDashboard = () => api.get<Dashboard>('/deploy/dashboard');
export const savePublicURL = (publicURL: string) => api.post<{ public_url: string }>('/deploy/preflight-operations', { public_url: publicURL });
export const getPreflightOperations = () => api.get<Array<{ id: string; ok: boolean; message: string }>>('/deploy/preflight-operations');
export const configureServer = (input: {
	language: string;
	enable_wecom: boolean;
	wecom_bot_id?: string;
	wecom_bot_secret?: string;
	wecom_allow_from?: string;
}) => api.put<ServiceStatus>('/deploy/dashboard', input);
export const createPairingCode = () => api.post<PairingCode>('/devices/pairing-code');
export const renameDevice = (id: string, name: string) => api.patch(`/devices/${encodeURIComponent(id)}`, { name });
export const revokeDevice = (id: string) => api.delete(`/devices/${encodeURIComponent(id)}`);
export const streamDeviceLogs = (id: string, after: number, onLine: (line: DeviceLogLine) => void, signal: AbortSignal) =>
	api.streamJSONLines<DeviceLogLine>(`/devices/${encodeURIComponent(id)}/logs/stream?after=${after}`, onLine, signal);
export const restartService = () => api.post<DeployRun>('/service/restart');
export const getServiceLogs = (after = 0) => api.get<Array<{ cursor: number; occurred_at: string; stream: string; line: string }>>('/service/logs', { after: String(after) });
export const listDeployRuns = () => api.get<DeployRun[]>('/deploy/runs');
export const startDeployRun = (kind: 'update' | 'rollback', targetTag?: string) => api.post<DeployRun>('/deploy/runs', { kind, target_tag: targetTag || '' });
export const cancelDeployRun = (id: string) => api.post(`/deploy/runs/${encodeURIComponent(id)}/cancel`);
export const streamDeployRun = (id: string, after: number, onLine: (line: DeployLogLine) => void, signal: AbortSignal) =>
	api.streamJSONLines<DeployLogLine>(`/deploy/runs/${encodeURIComponent(id)}/stream?after=${after}`, onLine, signal);
