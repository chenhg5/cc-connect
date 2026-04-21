import api from './client';

export interface ProjectSummary {
  name: string;
  agent_type: string;
  platforms: string[];
  sessions_count: number;
  heartbeat_enabled: boolean;
}

export interface PlatformConfigInfo {
  type: string;
  allow_from?: string;
}

export interface ProjectDetail {
  name: string;
  agent_type: string;
  work_dir?: string;
  agent_mode?: string;
  show_context_indicator?: boolean;
  reply_footer?: boolean;
  inject_sender?: boolean;
  provider_refs?: string[];
  platform_configs?: PlatformConfigInfo[];
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
    admin_from: string;
    language: string;
    disabled_commands: string[];
  };
}

export interface ProjectSettingsUpdate {
  language?: string;
  admin_from?: string;
  disabled_commands?: string[];
  work_dir?: string;
  mode?: string;
  agent_type?: string;
  show_context_indicator?: boolean;
  reply_footer?: boolean;
  inject_sender?: boolean;
  platform_allow_from?: Record<string, string>;
}

export const listAgentTypes = () => api.get<{ agents: string[]; platforms: string[] }>('/agents');

export const listProjects = () => api.get<{ projects: ProjectSummary[] }>('/projects');
export const getProject = (name: string) => api.get<ProjectDetail>(`/projects/${name}`);
export const updateProject = (name: string, body: ProjectSettingsUpdate) => api.patch(`/projects/${name}`, body);

export const addPlatformToProject = (projectName: string, body: {
  type: string; options: Record<string, any>; work_dir?: string; agent_type?: string;
}) => api.post<{ message: string; restart_required: boolean }>(`/projects/${projectName}/add-platform`, body);

export const deleteProject = (name: string) =>
  api.delete<{ message: string; restart_required: boolean }>(`/projects/${name}`);
