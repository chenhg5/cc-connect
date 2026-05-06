import api from './client';

// ── Types ─────────────────────────────────────────────────────

export interface AeliosSavedEntry {
  id: string;
  type: 'text' | 'link';
  content: string;
  source?: string;
  created_at: string;
}

export type AeliosTimelineType =
  | 'chat_summary'
  | 'agent_task'
  | 'favorite'
  | 'diary'
  | 'memory_update'
  | 'system_event'
  | 'file_result';

export interface AeliosTimelineEntry {
  id: string;
  type: AeliosTimelineType;
  content: string;
  date?: string;
  source?: string;
  created_at: string;
}

// ── Saved CRUD ────────────────────────────────────────────────

export const listSaved = () =>
  api.get<{ entries: AeliosSavedEntry[] }>('/aelios/saved');

export const createSaved = (body: {
  type: 'text' | 'link';
  content: string;
  source?: string;
}) => api.post<AeliosSavedEntry>('/aelios/saved', body);

export const getSaved = (id: string) =>
  api.get<AeliosSavedEntry>(`/aelios/saved/${id}`);

export const deleteSaved = (id: string) =>
  api.delete(`/aelios/saved/${id}`);

// ── Timeline CRUD ─────────────────────────────────────────────

export const listTimeline = (params?: { date?: string }) =>
  api.get<{ entries: AeliosTimelineEntry[] }>(
    '/aelios/timeline',
    params?.date ? { date: params.date } : undefined,
  );

export const createTimeline = (body: {
  type: AeliosTimelineType;
  content: string;
  date?: string;
  source?: string;
}) => api.post<AeliosTimelineEntry>('/aelios/timeline', body);

export const getTimeline = (id: string) =>
  api.get<AeliosTimelineEntry>(`/aelios/timeline/${id}`);

export const deleteTimeline = (id: string) =>
  api.delete(`/aelios/timeline/${id}`);

// ── Diary CRUD ────────────────────────────────────────────────

export type AeliosDiaryType = 'manual' | 'daily_summary' | 'work' | 'life';

export interface AeliosDiaryEntry {
  id: string;
  type: AeliosDiaryType;
  content: string;
  date: string;
  time?: string;
  created_at: string;
}

export const listDiary = (params?: { date?: string }) =>
  api.get<{ entries: AeliosDiaryEntry[] }>(
    '/aelios/diary',
    params?.date ? { date: params.date } : undefined,
  );

export const createDiary = (body: {
  type: AeliosDiaryType;
  content: string;
  date: string;
  time?: string;
}) => api.post<AeliosDiaryEntry>('/aelios/diary', body);

export const getDiary = (id: string) =>
  api.get<AeliosDiaryEntry>(`/aelios/diary/${id}`);

export const deleteDiary = (id: string) =>
  api.delete(`/aelios/diary/${id}`);
