import api from './client';

export interface ProviderModel {
  model: string;
  alias?: string;
}

export interface Provider {
  name: string;
  active: boolean;
  model: string;
  base_url: string;
}

export interface CodexConfig {
  wire_api?: string;
  http_headers?: Record<string, string>;
}

export interface GlobalProvider {
  name: string;
  api_key?: string;
  base_url?: string;
  model?: string;
  thinking?: string;
  env?: Record<string, string>;
  agent_types?: string[];
  models?: ProviderModel[];
  endpoints?: Record<string, string>;
  agent_models?: Record<string, string>;
  agent_model_lists?: Record<string, ProviderModel[]>;
  codex?: CodexConfig;
}

export interface PresetAgentConfig {
  base_url: string;
  model: string;
  models?: string[];
  codex_config?: { wire_api?: string; http_headers?: Record<string, string> };
}

export interface ProviderPreset {
  name: string;
  display_name: string;
  agents: Record<string, PresetAgentConfig>;
  invite_url?: string;
  description?: string;
  description_zh?: string;
  features?: string[];
  thinking?: string;
  tier: number;
  featured?: boolean;
  website?: string;
}

export interface PresetsResponse {
  version: number;
  updated_at?: string;
  providers: ProviderPreset[];
}

// Project-level provider APIs (existing)
export const listProviders = (project: string) =>
  api.get<{ providers: Provider[]; active_provider: string }>(`/projects/${project}/providers`);
export const addProvider = (project: string, body: any) => api.post(`/projects/${project}/providers`, body);
export const removeProvider = (project: string, provider: string) => api.delete(`/projects/${project}/providers/${provider}`);
export const activateProvider = (project: string, provider: string) => api.post(`/projects/${project}/providers/${provider}/activate`);
export const listModels = (project: string) => api.get<{ models: string[]; current: string }>(`/projects/${project}/models`);
export const setModel = (project: string, model: string) => api.post(`/projects/${project}/model`, { model });

// Project provider_refs APIs
export const getProviderRefs = (project: string) =>
  api.get<{ provider_refs: string[] }>(`/projects/${project}/provider-refs`);
export const saveProviderRefs = (project: string, refs: string[]) =>
  api.put<{ message: string }>(`/projects/${project}/provider-refs`, { provider_refs: refs });

// Global provider APIs
export const listGlobalProviders = () =>
  api.get<{ providers: GlobalProvider[] }>('/providers');
export const addGlobalProvider = (body: GlobalProvider) =>
  api.post<{ name: string; message: string }>('/providers', body);
export const updateGlobalProvider = (name: string, body: Partial<GlobalProvider>) =>
  api.put<{ message: string }>(`/providers/${name}`, body);
export const removeGlobalProvider = (name: string) =>
  api.delete<{ message: string }>(`/providers/${name}`);
export const fetchProviderPresets = () =>
  api.get<PresetsResponse>('/providers/presets');
