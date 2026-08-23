import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Settings2 } from 'lucide-react';
import { cn } from '@/lib/utils';
import type {
  NativeRuntimeCatalog,
  NativeThreadSettings,
  NativeThreadSettingsPatch,
} from '@/api/workspaceChat';

interface Props {
  catalog: NativeRuntimeCatalog | null;
  settings: NativeThreadSettingsPatch | NativeThreadSettings | null;
  disabled: boolean;
  updating: boolean;
  onPatch: (patch: NativeThreadSettingsPatch) => void;
}

const selectClass = 'h-8 min-w-0 rounded-md border border-gray-200 bg-white px-2 text-xs text-gray-700 outline-none focus:border-accent focus:ring-2 focus:ring-accent/25 dark:border-white/[0.12] dark:bg-black/30 dark:text-gray-200';

function settingMode(settings: Props['settings']): string {
  if (!settings) return 'default';
  if ('mode' in settings && settings.mode) return settings.mode;
  if ('collaboration_mode' in settings && settings.collaboration_mode?.mode) return settings.collaboration_mode.mode;
  return 'default';
}

function settingEffort(settings: Props['settings'], mode: string): string {
  if (!settings) return '';
  if (mode === 'plan' && 'collaboration_mode' in settings) {
    return settings.collaboration_mode?.settings.reasoning_effort || '';
  }
	if (mode === 'plan' && 'plan_effort' in settings) return settings.plan_effort || '';
  return settings.effort || '';
}

export function WorkspaceChatSettings({ catalog, settings, disabled, updating, onPatch }: Props) {
  const { t } = useTranslation();
  const models = useMemo(() => (catalog?.models || []).filter((model) => !model.hidden), [catalog]);
  const modelValue = settings?.model || models.find((model) => model.default)?.model || models[0]?.model || '';
  const selectedModel = models.find((model) => model.model === modelValue || model.id === modelValue) || models[0];
  const mode = settingMode(settings);
	const selectedMode = (catalog?.modes || []).find((option) => (option.mode || option.name).toLowerCase() === mode);
	const materializedModeModel = settings && 'collaboration_mode' in settings ? settings.collaboration_mode?.settings.model : undefined;
	const modeModel = materializedModeModel || selectedMode?.model;
	const effectiveModelValue = mode === 'plan' && modeModel ? modeModel : modelValue;
	const effectiveModel = models.find((model) => model.model === effectiveModelValue || model.id === effectiveModelValue) || selectedModel;
	const effort = settingEffort(settings, mode) || selectedMode?.reasoning_effort || effectiveModel?.default_reasoning_effort || '';
  const modes = new Set((catalog?.modes || []).map((option) => (option.mode || option.name).toLowerCase()));
	const reasoningEfforts = effectiveModel?.reasoning_efforts || [];
	const serviceTiers = effectiveModel?.service_tiers || [];
  const permissions = catalog?.permissions || [];
  const controlDisabled = disabled || updating || !catalog;

  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2 border-b border-gray-200 px-3 py-2 dark:border-white/[0.08]">
      <div className="flex h-8 shrink-0 rounded-md bg-gray-100 p-0.5 dark:bg-white/[0.07]" aria-label={t('workspaceChat.mode')}>
        {(['default', 'plan'] as const).map((value) => {
			const supported = modes.has(value);
          return (
            <button
              key={value}
              type="button"
              disabled={controlDisabled || !supported}
              aria-pressed={mode === value}
              onClick={() => onPatch({ mode: value })}
              className={cn(
                'min-w-[4.5rem] rounded px-2 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-40',
                mode === value ? 'bg-white text-gray-900 shadow-sm dark:bg-gray-700 dark:text-white' : 'text-gray-500 hover:text-gray-900 dark:text-gray-400 dark:hover:text-white',
              )}
            >
              {t(`workspaceChat.mode${value === 'default' ? 'Default' : 'Plan'}`)}
            </button>
          );
        })}
      </div>

      <label className="min-w-[9rem] flex-1 sm:max-w-[14rem]">
        <span className="sr-only">{t('workspaceChat.model')}</span>
        <select disabled={controlDisabled || models.length === 0} value={effectiveModelValue} onChange={(event) => onPatch({ model: event.target.value })} className={cn(selectClass, 'w-full')} title={t('workspaceChat.model')}>
          {models.map((model) => <option key={model.id || model.model} value={model.model}>{model.display_name || model.model}</option>)}
        </select>
      </label>

      <label className="min-w-[7rem] flex-1 sm:max-w-[10rem]">
        <span className="sr-only">{t('workspaceChat.effort')}</span>
		<select disabled={controlDisabled || reasoningEfforts.length === 0} value={effort} onChange={(event) => onPatch(mode === 'plan' ? { plan_effort: event.target.value } : { effort: event.target.value })} className={cn(selectClass, 'w-full')} title={t('workspaceChat.effort')}>
          {reasoningEfforts.map((option) => <option key={option.effort} value={option.effort}>{option.effort}</option>)}
        </select>
      </label>

      <label className="min-w-[8rem] flex-1 sm:max-w-[12rem]">
        <span className="sr-only">{t('workspaceChat.permission')}</span>
        <select disabled={controlDisabled || !permissions.some((permission) => permission.allowed)} value={settings?.permission_profile || ''} onChange={(event) => onPatch({ permission_profile: event.target.value })} className={cn(selectClass, 'w-full')} title={t('workspaceChat.permission')}>
          <option value="">{t('workspaceChat.permissionDefault')}</option>
          {permissions.filter((permission) => permission.allowed).map((permission) => <option key={permission.id} value={permission.id}>{permission.id}</option>)}
        </select>
      </label>

      <details className="group relative shrink-0">
        <summary className="flex h-8 w-8 cursor-pointer list-none items-center justify-center rounded-md text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:hover:bg-white/[0.08] dark:hover:text-white" title={t('workspaceChat.moreSettings')} aria-label={t('workspaceChat.moreSettings')}>
          <Settings2 size={16} />
        </summary>
        <div className="absolute right-0 top-10 z-30 w-[min(19rem,calc(100vw-5rem))] space-y-3 rounded-md border border-gray-200 bg-white p-3 shadow-xl dark:border-white/[0.12] dark:bg-gray-900">
          <label className="block text-xs font-medium text-gray-600 dark:text-gray-300">
            {t('workspaceChat.serviceTier')}
            <select disabled={controlDisabled || serviceTiers.length === 0} value={settings?.service_tier || effectiveModel?.default_service_tier || ''} onChange={(event) => onPatch({ service_tier: event.target.value })} className={cn(selectClass, 'mt-1 w-full')}>
              <option value="">{t('workspaceChat.defaultValue')}</option>
              {serviceTiers.map((tier) => <option key={tier.id} value={tier.id}>{tier.name || tier.id}</option>)}
            </select>
          </label>
          <label className="block text-xs font-medium text-gray-600 dark:text-gray-300">
            {t('workspaceChat.personality')}
            <select disabled={controlDisabled || !effectiveModel?.supports_personality} value={settings?.personality || ''} onChange={(event) => onPatch({ personality: event.target.value })} className={cn(selectClass, 'mt-1 w-full')}>
              <option value="">{t('workspaceChat.defaultValue')}</option>
              {(catalog?.personalities || []).map((personality) => <option key={personality} value={personality}>{personality}</option>)}
            </select>
          </label>
          <label className="block text-xs font-medium text-gray-600 dark:text-gray-300">
            {t('workspaceChat.summary')}
            <select disabled={controlDisabled} value={settings?.summary || ''} onChange={(event) => onPatch({ summary: event.target.value })} className={cn(selectClass, 'mt-1 w-full')}>
              <option value="">{t('workspaceChat.defaultValue')}</option>
              {(catalog?.summaries || []).map((summary) => <option key={summary} value={summary}>{summary}</option>)}
            </select>
          </label>
        </div>
      </details>
      {updating && <span className="text-[10px] text-gray-400">{t('workspaceChat.updatingSettings')}</span>}
    </div>
  );
}
