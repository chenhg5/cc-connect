import api from './client';

export interface Session {
  id: string;
  session_key: string;
  name: string;
  platform: string;
  active: boolean;
  created_at: string;
  updated_at: string;
  history_count: number;
}

export interface SessionDetail extends Session {
  agent_session_id: string;
  history: { role: string; content: string; timestamp: string }[];
}

export const listSessions = (project: string) => api.get<{ sessions: Session[] }>(`/projects/${project}/sessions`);
export const getSession = (project: string, id: string, historyLimit?: number) =>
  api.get<SessionDetail>(`/projects/${project}/sessions/${id}`, historyLimit ? { history_limit: String(historyLimit) } : undefined);
export const createSession = (project: string, body: { session_key: string; name?: string }) =>
  api.post(`/projects/${project}/sessions`, body);
export const deleteSession = (project: string, id: string) => api.delete(`/projects/${project}/sessions/${id}`);
export const switchSession = (project: string, body: { session_key: string; session_id: string }) =>
  api.post(`/projects/${project}/sessions/switch`, body);
export const sendMessage = (project: string, body: { session_key: string; message: string }) =>
  api.post(`/projects/${project}/send`, body);
