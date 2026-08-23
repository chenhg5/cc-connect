import { api } from './client';

export interface Workspace {
  ref: string;
  project_id: string;
  project_name: string;
  root_index: number;
  root_name: string;
  root_path: string;
  available: boolean;
  error?: string;
  order: number;
}

export interface NativeThread {
  id: string;
  cwd: string;
  name?: string;
  preview?: string;
  status?: unknown;
  created_at: string;
  updated_at: string;
}

export interface NativeTurn {
  id: string;
  status: string;
  started_at?: string;
  completed_at?: string;
  duration_ms?: number;
  error?: unknown;
  items: Record<string, unknown>[];
}

export interface NativeThreadDetail extends NativeThread {
  turns: NativeTurn[];
}

export interface WorkspaceChatSelection {
  client_id: string;
  workspace_ref: string;
  thread_id: string;
  updated_at: string;
}

export interface WorkspaceChatEvent {
  type: string;
  request_id?: string;
  client_id?: string;
  workspace_ref?: string;
  thread_id?: string;
  content?: string;
  payload?: Record<string, unknown>;
  error?: string;
  occurred_at?: string;
}

export const listWorkspaces = () => api.get<{ workspaces: Workspace[] }>('/chat/workspaces');

export const listWorkspaceThreads = (workspaceRef: string) =>
  api.get<{ threads: NativeThread[] }>(`/chat/workspaces/${encodeURIComponent(workspaceRef)}/threads`);

export const createWorkspaceThread = (workspaceRef: string, name: string) =>
  api.post<NativeThread>(`/chat/workspaces/${encodeURIComponent(workspaceRef)}/threads`, { name });

export const readWorkspaceThread = (workspaceRef: string, threadId: string) =>
  api.get<NativeThreadDetail>(`/chat/workspaces/${encodeURIComponent(workspaceRef)}/threads/${encodeURIComponent(threadId)}`);

export const getWorkspaceChatSelection = () => api.get<WorkspaceChatSelection | null>('/chat/selection');

export const putWorkspaceChatSelection = (workspaceRef: string, threadId?: string) =>
  api.put<WorkspaceChatSelection>('/chat/selection', {
    workspace_ref: workspaceRef,
    thread_id: threadId || '',
  });

export function workspaceChatWebSocketURL(): string {
  const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = new URL(`${scheme}//${window.location.host}/api/v1/chat/ws`);
  const token = api.getToken();
  if (token) url.searchParams.set('token', token);
  return url.toString();
}
