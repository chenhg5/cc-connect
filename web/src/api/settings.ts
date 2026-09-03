import api from './client';

export interface GlobalSettings {
  language: string;
  attachment_send: string;
  log_level: string;
  idle_timeout_mins: number;
  thinking_messages: boolean;
  thinking_max_len: number;
  tool_messages: boolean;
  tool_max_len: number;
  stream_preview_enabled: boolean;
  stream_preview_interval_ms: number;
  rate_limit_max_messages: number;
  rate_limit_window_secs: number;
  retriable_error_initial_delay_secs: number;
  retriable_error_retry_delay_secs: number;
  retriable_error_max_attempts: number;
}

export const getGlobalSettings = () => api.get<GlobalSettings>('/settings');
export const updateGlobalSettings = (body: Partial<GlobalSettings>) => api.patch<GlobalSettings>('/settings', body);
