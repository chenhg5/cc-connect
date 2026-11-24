import api from './client';

export interface FeishuBeginResponse {
  device_code: string;
  qr_url: string;
  interval: number;
  expires_in: number;
}

export interface FeishuPollResponse {
  status: 'pending' | 'completed' | 'denied' | 'expired' | 'error';
  base_url?: string;
  app_id?: string;
  app_secret?: string;
  platform?: string;
  owner_open_id?: string;
  slow_down?: boolean;
  error?: string;
}

export const setupFeishuBegin = () =>
  api.post<FeishuBeginResponse>('/setup/feishu/begin', {});

export const setupFeishuPoll = (deviceCode: string, baseUrl?: string) =>
  api.post<FeishuPollResponse>('/setup/feishu/poll', { device_code: deviceCode, base_url: baseUrl });

export const setupFeishuSave = (body: {
  project: string; app_id: string; app_secret: string; platform_type: string;
  owner_open_id?: string; work_dir?: string; agent_type?: string;
}) => api.post<{ message: string; restart_required: boolean }>('/setup/feishu/save', body);
