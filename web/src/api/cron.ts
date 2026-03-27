import api from './client';

export interface CronJob {
  id: string;
  project: string;
  session_key: string;
  cron_expr: string;
  prompt: string;
  exec: string;
  work_dir: string;
  description: string;
  enabled: boolean;
  silent: boolean;
  mute: boolean;
  session_mode: string;
  timeout_mins: number | null;
  created_at: string;
  last_run: string;
  last_error: string;
}

export const listCronJobs = (project?: string) =>
  api.get<{ jobs: CronJob[] }>('/cron', project ? { project } : undefined);
export const createCronJob = (body: Partial<CronJob>) => api.post<CronJob>('/cron', body);
export const updateCronJob = (id: string, fields: Record<string, any>) => api.patch<CronJob>(`/cron/${id}`, fields);
export const deleteCronJob = (id: string) => api.delete(`/cron/${id}`);
