import api from './client';

export interface BridgeAdapter {
  platform: string;
  project: string;
  capabilities: string[];
  connected_at: string;
}

export const listBridgeAdapters = () => api.get<{ adapters: BridgeAdapter[] }>('/bridge/adapters');
