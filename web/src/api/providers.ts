import api from './client';

export interface Provider {
  name: string;
  active: boolean;
  model: string;
  base_url: string;
}

export const listProviders = (project: string) =>
  api.get<{ providers: Provider[]; active_provider: string }>(`/projects/${project}/providers`);
export const addProvider = (project: string, body: any) => api.post(`/projects/${project}/providers`, body);
export const removeProvider = (project: string, provider: string) => api.delete(`/projects/${project}/providers/${provider}`);
export const activateProvider = (project: string, provider: string) => api.post(`/projects/${project}/providers/${provider}/activate`);
export const listModels = (project: string) => api.get<{ models: string[]; current: string }>(`/projects/${project}/models`);
export const setModel = (project: string, model: string) => api.post(`/projects/${project}/model`, { model });
