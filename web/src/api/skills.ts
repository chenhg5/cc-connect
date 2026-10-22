import api from './client';

export interface SkillInfo {
  name: string;
  display_name?: string;
  description?: string;
  source: string;
}

export interface ProjectSkills {
  project: string;
  agent_type: string;
  dirs: string[];
  skills: SkillInfo[];
}

export interface SkillSource {
  provider: string;
  name?: string;
  url?: string;
}

export interface SkillPricing {
  type: 'free' | 'paid' | 'freemium';
  price?: number;
  currency?: string;
}

export interface SkillPreset {
  name: string;
  display_name: string;
  description?: string;
  description_zh?: string;
  version?: string;
  author?: string;
  url?: string;
  agent_types?: string[];
  tags?: string[];
  featured?: boolean;
  source?: SkillSource;
  pricing?: SkillPricing;
}

export interface SkillPresetsResponse {
  version: number;
  updated_at?: string;
  skills: SkillPreset[];
}

export const listSkills = () =>
  api.get<{ projects: ProjectSkills[] }>('/skills');

export const fetchSkillPresets = () =>
  api.get<SkillPresetsResponse>('/skills/presets');
