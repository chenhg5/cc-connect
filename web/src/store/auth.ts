import { create } from 'zustand';
import { api } from '@/api/client';

interface AuthState {
  token: string;
  serverUrl: string;
  isAuthenticated: boolean;
  login: (token: string, serverUrl?: string) => void;
  logout: () => void;
  init: () => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  token: '',
  serverUrl: '',
  isAuthenticated: false,
  login: (token: string, serverUrl?: string) => {
    api.setToken(token);
    localStorage.setItem('cc_token', token);
    if (serverUrl) localStorage.setItem('cc_server_url', serverUrl);
    set({ token, serverUrl: serverUrl || '', isAuthenticated: true });
  },
  logout: () => {
    api.setToken('');
    localStorage.removeItem('cc_token');
    localStorage.removeItem('cc_server_url');
    set({ token: '', serverUrl: '', isAuthenticated: false });
  },
  init: () => {
    const token = localStorage.getItem('cc_token') || '';
    const serverUrl = localStorage.getItem('cc_server_url') || '';
    if (token) {
      api.setToken(token);
      set({ token, serverUrl, isAuthenticated: true });
    }
  },
}));
