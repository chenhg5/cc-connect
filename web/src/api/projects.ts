import api from './client';

export interface ProjectSummary {
  name: string;
  agent_type: string;
  platforms: string[];
  sessions_count: number;
  heartbeat_enabled: boolean;
}

export interface ProjectDetail {
  name: string;
  agent_type: string;
  platforms: { type: string; connected: boolean }[];
  sessions_count: number;
  active_session_keys: string[];
  heartbeat: {
    enabled: boolean;
    paused: boolean;
    interval_mins: number;
    session_key: string;
  };
  settings: {
    quiet: boolean;
    admin_from: string;
    language: string;
    disabled_commands: string[];
  };
}

export const listProjects = () => api.get<{ projects: ProjectSummary[] }>('/projects');
export const getProject = (name: string) => api.get<ProjectDetail>(`/projects/${name}`);
export const updateProject = (name: string, body: Partial<ProjectDetail['settings']>) => api.patch(`/projects/${name}`, body);

export const addPlatformToProject = (projectName: string, body: { type: string; options: Record<string, any> }) =>
  api.post<{ message: string; restart_required: boolean }>(`/projects/${projectName}/add-platform`, body);

export const deleteProject = (name: string) =>
  api.delete<{ message: string; restart_required: boolean }>(`/projects/${name}`);
