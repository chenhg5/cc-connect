import { create } from 'zustand';
import { api } from '@/api/client';

interface AuthState {
  phase: 'loading' | 'authenticated' | 'unauthenticated';
  isAuthenticated: boolean;
  login: (password: string) => Promise<void>;
  setup: (setupToken: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  init: () => Promise<void>;
}

export const useAuthStore = create<AuthState>((set) => ({
  phase: 'loading',
  isAuthenticated: false,
  login: async (password: string) => {
    const session = await api.post<{ csrf_token: string }>('/auth/login', { password });
    api.setCSRFToken(session.csrf_token);
    set({ phase: 'authenticated', isAuthenticated: true });
  },
  setup: async (setupToken: string, password: string) => {
    const session = await api.post<{ csrf_token: string }>('/auth/setup', { setup_token: setupToken, password });
    api.setCSRFToken(session.csrf_token);
    set({ phase: 'authenticated', isAuthenticated: true });
  },
  logout: async () => {
    try {
      await api.post('/auth/logout');
    } finally {
      api.clearSession();
      set({ phase: 'unauthenticated', isAuthenticated: false });
    }
  },
  init: async () => {
    try {
      const session = await api.get<{ csrf_token: string }>('/auth/session');
      api.setCSRFToken(session.csrf_token);
      set({ phase: 'authenticated', isAuthenticated: true });
    } catch {
      api.clearSession();
      set({ phase: 'unauthenticated', isAuthenticated: false });
    }
  },
}));
