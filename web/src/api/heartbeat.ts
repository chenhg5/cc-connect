import api from './client';

export interface HeartbeatStatus {
  enabled: boolean;
  paused: boolean;
  interval_mins: number;
  only_when_idle: boolean;
  session_key: string;
  silent: boolean;
  run_count: number;
  error_count: number;
  skipped_busy: number;
  last_run: string;
  last_error: string;
}

export const getHeartbeat = (project: string) => api.get<HeartbeatStatus>(`/projects/${project}/heartbeat`);
export const pauseHeartbeat = (project: string) => api.post(`/projects/${project}/heartbeat/pause`);
export const resumeHeartbeat = (project: string) => api.post(`/projects/${project}/heartbeat/resume`);
export const triggerHeartbeat = (project: string) => api.post(`/projects/${project}/heartbeat/run`);
export const setHeartbeatInterval = (project: string, minutes: number) =>
  api.post(`/projects/${project}/heartbeat/interval`, { minutes });
