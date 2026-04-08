import api from './client';

export interface SystemStatus {
  version: string;
  uptime_seconds: number;
  connected_platforms: string[];
  projects_count: number;
  bridge_adapters: { platform: string; project: string; capabilities: string[] }[];
}

export const getStatus = () => api.get<SystemStatus>('/status');
export const restartSystem = (body?: { session_key?: string; platform?: string }) => api.post('/restart', body);
export const reloadConfig = () => api.post<{ message: string; projects_added: string[]; projects_removed: string[]; projects_updated: string[] }>('/reload');
