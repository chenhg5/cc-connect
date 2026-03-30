import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Eye, EyeOff, ChevronDown, AlertCircle } from 'lucide-react';
import { Button } from '@/components/ui';
import { addPlatformToProject } from '@/api/projects';
import { platformMeta, type FieldDef } from '@/lib/platformMeta';
import { cn } from '@/lib/utils';

interface Props {
  platformType: string;
  projectName: string;
  workDir?: string;
  agentType?: string;
  onComplete: () => void;
  onCancel: () => void;
}

export default function PlatformManualForm({ platformType, projectName, workDir, agentType, onComplete, onCancel }: Props) {
  const { t } = useTranslation();
  const meta = platformMeta[platformType];
  const [values, setValues] = useState<Record<string, any>>({});
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  if (!meta) {
    return (
      <div className="py-4 text-center text-sm text-gray-500">
        {t('setup.unsupportedPlatform', 'Unsupported platform type: {{type}}', { type: platformType })}
      </div>
    );
  }

  const basicFields = meta.fields.filter(f => f.group !== 'advanced');
  const advancedFields = meta.fields.filter(f => f.group === 'advanced');

  const handleSave = async () => {
    const missing = meta.fields.filter(f => f.required && !values[f.key]);
    if (missing.length > 0) {
      setError(missing.map(f => t(f.labelKey)).join(', ') + ' required');
      return;
    }

    setSaving(true);
    setError('');
    try {
      const opts: Record<string, any> = {};
      for (const f of meta.fields) {
        const v = values[f.key];
        if (v !== undefined && v !== '' && v !== false) {
          opts[f.key] = v;
        }
      }
      await addPlatformToProject(projectName, { type: platformType, options: opts, work_dir: workDir, agent_type: agentType });
      onComplete();
    } catch (e: any) {
      setError(e?.message || String(e));
    } finally {
      setSaving(false);
    }
  };

  const set = (key: string, val: any) => setValues(prev => ({ ...prev, [key]: val }));

  return (
    <div className="space-y-4 py-2">
      <p className="text-sm font-medium text-gray-900 dark:text-white">{meta.label}</p>

      {basicFields.map(f => (
        <FieldInput key={f.key} field={f} value={values[f.key]} onChange={v => set(f.key, v)} t={t} />
      ))}

      {advancedFields.length > 0 && (
        <>
          <button
            type="button"
            onClick={() => setShowAdvanced(!showAdvanced)}
            className="flex items-center gap-1 text-xs text-gray-500 hover:text-gray-700 dark:hover:text-gray-300"
          >
            <ChevronDown size={12} className={cn('transition-transform', showAdvanced && 'rotate-180')} />
            {t('setup.advancedOptions', 'Advanced options')} ({advancedFields.length})
          </button>
          {showAdvanced && advancedFields.map(f => (
            <FieldInput key={f.key} field={f} value={values[f.key]} onChange={v => set(f.key, v)} t={t} />
          ))}
        </>
      )}

      {error && (
        <div className="flex items-center gap-2 text-sm text-red-500 bg-red-50 dark:bg-red-900/20 rounded-lg p-3">
          <AlertCircle size={14} className="shrink-0" /> {error}
        </div>
      )}

      <div className="flex justify-between pt-2">
        <Button variant="secondary" size="sm" onClick={onCancel}>{t('common.back')}</Button>
        <Button onClick={handleSave} loading={saving}>{t('setup.addPlatform', 'Add platform')}</Button>
      </div>
    </div>
  );
}

function FieldInput({ field, value, onChange, t }: { field: FieldDef; value: any; onChange: (v: any) => void; t: (key: string) => string }) {
  const [showPwd, setShowPwd] = useState(false);
  const label = t(field.labelKey);
  const hint = field.hintKey ? t(field.hintKey) : undefined;

  if (field.type === 'boolean') {
    return (
      <label className="flex items-center gap-2 cursor-pointer">
        <input
          type="checkbox"
          checked={!!value}
          onChange={e => onChange(e.target.checked)}
          className="w-4 h-4 rounded border-gray-300 text-accent focus:ring-accent"
        />
        <span className="text-sm text-gray-700 dark:text-gray-300">{label}</span>
        {hint && <span className="text-[11px] text-gray-400">({hint})</span>}
      </label>
    );
  }

  const isPassword = field.type === 'password';

  return (
    <div>
      <label className="block text-xs font-medium text-gray-600 dark:text-gray-400 mb-1">
        {label} {field.required && <span className="text-red-400">*</span>}
      </label>
      <div className="relative">
        <input
          type={isPassword && !showPwd ? 'password' : field.type === 'number' ? 'number' : 'text'}
          value={value || ''}
          onChange={e => onChange(field.type === 'number' ? (e.target.value ? Number(e.target.value) : '') : e.target.value)}
          placeholder={field.placeholder}
          className="w-full px-3 py-2 text-sm rounded-lg border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-800 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/50 placeholder:text-gray-400"
        />
        {isPassword && (
          <button
            type="button"
            onClick={() => setShowPwd(!showPwd)}
            className="absolute right-2 top-1/2 -translate-y-1/2 p-1 text-gray-400 hover:text-gray-600"
          >
            {showPwd ? <EyeOff size={14} /> : <Eye size={14} />}
          </button>
        )}
      </div>
      {hint && <p className="text-[11px] text-gray-400 mt-1">{hint}</p>}
    </div>
  );
}
