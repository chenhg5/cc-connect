import { useEffect, useState, useCallback, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Clock, Play, Plus, Trash2, Terminal, MessageSquare, Power,
  ChevronDown,
} from 'lucide-react';
import { Button, Badge, Modal, Input, Textarea, EmptyState } from '@/components/ui';
import { listCronJobs, createCronJob, updateCronJob, deleteCronJob, triggerCronJob, type CronJob } from '@/api/cron';
import { listProjects, type ProjectSummary } from '@/api/projects';
import { listSessions, type Session } from '@/api/sessions';
import { formatTime, cn } from '@/lib/utils';

const MODE_OPTIONS = ['bypassPermissions', 'acceptEdits', 'auto', 'plan', 'dontAsk'] as const;

/* ── Cron presets ── */
interface CronPreset {
  label: string;
  labelZh: string;
  expr: string;
}

const PRESETS: CronPreset[] = [
  { label: 'Every minute',  labelZh: '每分钟',     expr: '* * * * *'    },
  { label: 'Every 5 min',   labelZh: '每 5 分钟',  expr: '*/5 * * * *'  },
  { label: 'Every 15 min',  labelZh: '每 15 分钟', expr: '*/15 * * * *' },
  { label: 'Every 30 min',  labelZh: '每 30 分钟', expr: '*/30 * * * *' },
  { label: 'Every hour',    labelZh: '每小时',     expr: '0 * * * *'    },
  { label: 'Every 2 hours', labelZh: '每 2 小时',  expr: '0 */2 * * *'  },
  { label: 'Every 6 hours', labelZh: '每 6 小时',  expr: '0 */6 * * *'  },
  { label: 'Daily 6:00',    labelZh: '每天 6:00',  expr: '0 6 * * *'    },
  { label: 'Daily 9:00',    labelZh: '每天 9:00',  expr: '0 9 * * *'    },
  { label: 'Daily 12:00',   labelZh: '每天 12:00', expr: '0 12 * * *'   },
  { label: 'Daily 18:00',   labelZh: '每天 18:00', expr: '0 18 * * *'   },
  { label: 'Daily 22:00',   labelZh: '每天 22:00', expr: '0 22 * * *'   },
  { label: 'Weekdays 9:00', labelZh: '工作日 9:00',expr: '0 9 * * 1-5'  },
  { label: 'Weekly Mon 9AM',labelZh: '每周一 9:00',expr: '0 9 * * 1'    },
  { label: 'Monthly 1st',   labelZh: '每月 1 号',  expr: '0 0 1 * *'    },
];

function describeCron(expr: string): string {
  const preset = PRESETS.find(p => p.expr === expr);
  if (preset) return preset.labelZh;
  return expr;
}

/* ── Cron Schedule Picker (dropdown + custom input) ── */
const CUSTOM_VALUE = '__custom__';

function CronPicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { i18n } = useTranslation();
  const isZh = i18n.language?.startsWith('zh');
  const isPreset = PRESETS.some(p => p.expr === value);
  const [custom, setCustom] = useState(!isPreset && !!value);

  const selectValue = custom ? CUSTOM_VALUE : value;

  const handleSelect = (v: string) => {
    if (v === CUSTOM_VALUE) {
      setCustom(true);
    } else {
      setCustom(false);
      onChange(v);
    }
  };

  return (
    <div className="space-y-2">
      <div className="relative">
        <select
          value={selectValue}
          onChange={e => handleSelect(e.target.value)}
          className={cn(
            'w-full px-3 py-2 text-sm rounded-lg transition-all duration-200 appearance-none pr-8',
            'border border-gray-300/90 dark:border-white/[0.1]',
            'bg-white/90 backdrop-blur-sm dark:bg-[rgba(0,0,0,0.45)]',
            'text-gray-900 dark:text-white',
            'focus:outline-none focus:ring-2 focus:ring-accent/45 focus:border-accent',
          )}
        >
          <option value="" disabled>{isZh ? '选择执行频率' : 'Select schedule'}</option>
          {PRESETS.map(p => (
            <option key={p.expr} value={p.expr}>
              {isZh ? p.labelZh : p.label} ({p.expr})
            </option>
          ))}
          <option value={CUSTOM_VALUE}>{isZh ? '✏ 自定义表达式' : '✏ Custom expression'}</option>
        </select>
        <ChevronDown size={14} className="absolute right-2.5 top-1/2 -translate-y-1/2 text-gray-400 pointer-events-none" />
      </div>
      {custom && (
        <Input
          placeholder="0 9 * * 1-5"
          value={value}
          onChange={e => onChange(e.target.value)}
          className="font-mono text-xs"
        />
      )}
    </div>
  );
}

/* ── Select dropdown ── */
function Select({ label, value, onChange, options, placeholder }: {
  label?: string;
  value: string;
  onChange: (v: string) => void;
  options: { value: string; label: string }[];
  placeholder?: string;
}) {
  return (
    <div className="space-y-1.5">
      {label && <label className="block text-sm font-medium text-gray-700 dark:text-gray-300">{label}</label>}
      <div className="relative">
        <select
          value={value}
          onChange={e => onChange(e.target.value)}
          className={cn(
            'w-full px-3 py-2 text-sm rounded-lg transition-all duration-200 appearance-none pr-8',
            'border border-gray-300/90 dark:border-white/[0.1]',
            'bg-white/90 backdrop-blur-sm dark:bg-[rgba(0,0,0,0.45)]',
            'text-gray-900 dark:text-white',
            'focus:outline-none focus:ring-2 focus:ring-accent/45 focus:border-accent',
          )}
        >
          {placeholder && <option value="">{placeholder}</option>}
          {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
        <ChevronDown size={14} className="absolute right-2.5 top-1/2 -translate-y-1/2 text-gray-400 pointer-events-none" />
      </div>
    </div>
  );
}

/* ── Toggle ── */
function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label?: string }) {
  return (
    <label className="inline-flex items-center gap-2 cursor-pointer">
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={cn(
          'relative w-9 h-5 rounded-full transition-colors duration-200 shrink-0',
          checked ? 'bg-accent' : 'bg-gray-300 dark:bg-gray-600',
        )}
      >
        <span className={cn(
          'block w-3.5 h-3.5 rounded-full bg-white shadow-sm transition-transform duration-200',
          checked ? 'translate-x-[18px]' : 'translate-x-[3px]',
          'mt-[3px]',
        )} />
      </button>
      {label && <span className="text-sm text-gray-700 dark:text-gray-300">{label}</span>}
    </label>
  );
}

/* ── Job form type ── */
interface JobForm {
  project: string;
  session_key: string;
  cron_expr: string;
  prompt: string;
  exec: string;
  description: string;
  silent: boolean;
  enabled: boolean;
  mode: string;
  _type: 'prompt' | 'exec';
}

const emptyForm: JobForm = {
  project: '', session_key: '', cron_expr: '', prompt: '', exec: '',
  description: '', silent: false, enabled: true, mode: '', _type: 'prompt',
};

/* ── Main page ── */
export default function CronList() {
  const { t } = useTranslation();
  const [jobs, setJobs] = useState<CronJob[]>([]);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [loading, setLoading] = useState(true);

  const [editJob, setEditJob] = useState<CronJob | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState<JobForm>({ ...emptyForm });
  const [saving, setSaving] = useState(false);
  const [triggeringId, setTriggeringId] = useState<string | null>(null);
  const [sessionKeys, setSessionKeys] = useState<string[]>([]);

  const isEdit = !!editJob;

  const projectOptions = useMemo(
    () => projects.map(p => ({ value: p.name, label: p.name })),
    [projects],
  );

  useEffect(() => {
    if (!form.project) { setSessionKeys([]); return; }
    let cancelled = false;
    listSessions(form.project).then(data => {
      if (cancelled) return;
      const keys = new Set<string>();
      for (const s of data.sessions || []) {
        if (s.session_key) keys.add(s.session_key);
      }
      setSessionKeys([...keys]);
    }).catch(() => { if (!cancelled) setSessionKeys([]); });
    return () => { cancelled = true; };
  }, [form.project]);

  const fetchData = useCallback(async () => {
    setLoading(true);
    try {
      const [cronData, projData] = await Promise.all([listCronJobs(), listProjects()]);
      setJobs(cronData.jobs || []);
      setProjects(projData.projects || []);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
    const handler = () => fetchData();
    window.addEventListener('cc:refresh', handler);
    return () => window.removeEventListener('cc:refresh', handler);
  }, [fetchData]);

  const openAdd = () => {
    setEditJob(null);
    setForm({ ...emptyForm });
    setShowForm(true);
  };

  const openEdit = (job: CronJob) => {
    setEditJob(job);
    setForm({
      project: job.project,
      session_key: job.session_key,
      cron_expr: job.cron_expr,
      prompt: job.prompt,
      exec: job.exec,
      description: job.description,
      silent: !!job.silent,
      enabled: job.enabled,
      mode: job.mode || '',
      _type: job.exec ? 'exec' : 'prompt',
    });
    setShowForm(true);
  };

  const handleSave = async () => {
    setSaving(true);
    const activePrompt = form._type === 'prompt' ? form.prompt : '';
    const activeExec   = form._type === 'exec'   ? form.exec   : '';
    try {
      if (isEdit && editJob) {
        const updates: Record<string, any> = {};
        if (form.cron_expr !== editJob.cron_expr) updates.cron_expr = form.cron_expr;
        if (form.description !== editJob.description) updates.description = form.description;
        if (activePrompt !== editJob.prompt) updates.prompt = activePrompt;
        if (activeExec !== editJob.exec) updates.exec = activeExec;
        if (form.project !== editJob.project) updates.project = form.project;
        if (form.session_key !== editJob.session_key) updates.session_key = form.session_key;
        if (form.silent !== !!editJob.silent) updates.silent = form.silent;
        if (form.enabled !== editJob.enabled) updates.enabled = form.enabled;
        if ((form.mode || '') !== (editJob.mode || '')) updates.mode = form.mode;
        if (Object.keys(updates).length > 0) {
          await updateCronJob(editJob.id, updates);
        }
      } else {
        const body: any = { ...form, prompt: activePrompt, exec: activeExec };
        delete body._type;
        if (!body.prompt) delete body.prompt;
        if (!body.exec) delete body.exec;
        await createCronJob(body);
      }
      setShowForm(false);
      fetchData();
    } catch (e: any) {
      alert(e.message);
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (id: string) => {
    if (!confirm(t('common.confirmDelete'))) return;
    await deleteCronJob(id);
    fetchData();
  };

  const handleToggleEnabled = async (job: CronJob) => {
    try {
      await updateCronJob(job.id, { enabled: !job.enabled });
      fetchData();
    } catch (e: any) {
      alert(e.message);
    }
  };

  const handleTrigger = async (job: CronJob) => {
    setTriggeringId(job.id);
    try {
      await triggerCronJob(job.id);
      alert(t('cron.triggerPending'));
      fetchData();
    } catch (e: any) {
      alert(e.message);
    } finally {
      setTriggeringId(null);
    }
  };

  if (loading && jobs.length === 0) {
    return <div className="flex items-center justify-center h-64 text-gray-400 animate-pulse">Loading...</div>;
  }

  return (
    <div className="space-y-4 animate-fade-in">
      <div className="flex justify-between items-center">
        <h2 className="text-lg font-semibold text-gray-900 dark:text-white">{t('cron.title')}</h2>
        <Button onClick={openAdd}><Plus size={16} /> {t('cron.add')}</Button>
      </div>

      {jobs.length === 0 ? (
        <EmptyState message={t('cron.noJobs')} icon={Clock} />
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
          {jobs.map(job => (
            <div
              key={job.id}
              onClick={() => openEdit(job)}
              className={cn(
                'relative p-4 rounded-xl border transition-all cursor-pointer group',
                'bg-white dark:bg-white/[0.02]',
                job.enabled
                  ? 'border-gray-200/80 dark:border-white/[0.06] hover:border-accent/40 hover:shadow-md hover:shadow-accent/5'
                  : 'border-dashed border-gray-300/60 dark:border-white/[0.04] opacity-60 hover:opacity-80',
              )}
            >
              {/* Header */}
              <div className="flex items-center gap-2 mb-2">
                {job.prompt ? (
                  <MessageSquare size={14} className="text-blue-400 shrink-0" />
                ) : (
                  <Terminal size={14} className="text-amber-400 shrink-0" />
                )}
                <span className="font-medium text-sm text-gray-900 dark:text-white truncate">
                  {job.description || job.id}
                </span>
              </div>

              {/* Schedule badge + mode badge */}
              <div className="flex items-center gap-2 mb-3">
                <span className="inline-flex items-center gap-1 text-[11px] font-mono bg-accent/10 text-accent px-2 py-0.5 rounded-md">
                  <Clock size={10} />
                  {describeCron(job.cron_expr)}
                </span>
                {job.silent && <Badge variant="default" className="text-[10px] px-1.5 py-0">silent</Badge>}
                <Badge variant="default" className="text-[10px] px-1.5 py-0">{job.mode || t('cron.modeDefault')}</Badge>
              </div>

              {/* Info */}
              <div className="space-y-1 text-xs text-gray-500 dark:text-gray-400">
                <div className="flex items-center gap-1.5">
                  <span className="font-medium w-12 shrink-0 text-gray-400">{t('cron.project')}</span>
                  <span className="truncate">{job.project}</span>
                </div>
                {job.prompt && (
                  <div className="flex items-start gap-1.5">
                    <span className="font-medium w-12 shrink-0 text-gray-400">{t('cron.prompt')}</span>
                    <span className="line-clamp-2">{job.prompt}</span>
                  </div>
                )}
                {job.exec && (
                  <div className="flex items-center gap-1.5">
                    <span className="font-medium w-12 shrink-0 text-gray-400">{t('cron.exec')}</span>
                    <code className="truncate text-[11px]">{job.exec}</code>
                  </div>
                )}
                {job.last_run && (
                  <div className="flex items-center gap-1.5 pt-1 border-t border-gray-100 dark:border-white/[0.04] mt-1">
                    <span className="font-medium w-12 shrink-0 text-gray-400">{t('cron.lastRun')}</span>
                    <span>{formatTime(job.last_run)}</span>
                  </div>
                )}
              </div>

              {job.last_error && (
                <p className="text-[11px] text-red-500 mt-2 line-clamp-1">{job.last_error}</p>
              )}

              {/* Action buttons (top right) */}
              <div
                className="absolute top-3 right-3 flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity"
                onClick={e => e.stopPropagation()}
              >
                <button
                  onClick={() => handleTrigger(job)}
                  disabled={triggeringId === job.id}
                  className={cn(
                    'p-1.5 rounded-lg transition-colors',
                    'text-sky-500 hover:bg-sky-50 dark:hover:bg-sky-900/20 disabled:opacity-50 disabled:cursor-wait',
                  )}
                  title={t('cron.trigger')}
                >
                  <Play size={14} />
                </button>
                <button
                  onClick={() => handleToggleEnabled(job)}
                  className={cn(
                    'p-1.5 rounded-lg transition-colors',
                    job.enabled
                      ? 'text-emerald-500 hover:bg-emerald-50 dark:hover:bg-emerald-900/20'
                      : 'text-gray-400 hover:bg-gray-100 dark:hover:bg-white/[0.06]',
                  )}
                  title={job.enabled ? 'Disable' : 'Enable'}
                >
                  <Power size={14} />
                </button>
                <button
                  onClick={() => handleDelete(job.id)}
                  className="p-1.5 rounded-lg text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20 transition-colors"
                  title={t('cron.delete')}
                >
                  <Trash2 size={14} />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Add / Edit modal */}
      <Modal
        open={showForm}
        onClose={() => setShowForm(false)}
        title={isEdit ? t('cron.editJob') : t('cron.add')}
        className="max-w-xl"
      >
        <div className="space-y-4">
          <Select
            label={t('cron.project')}
            value={form.project}
            onChange={v => setForm({ ...form, project: v })}
            options={projectOptions}
            placeholder={t('cron.selectProject')}
          />

          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1.5">
              {t('cron.schedule')}
            </label>
            <CronPicker value={form.cron_expr} onChange={v => setForm({ ...form, cron_expr: v })} />
          </div>

          <Input
            label={t('cron.description')}
            value={form.description}
            onChange={e => setForm({ ...form, description: e.target.value })}
            placeholder={t('cron.descPlaceholder')}
          />

          {/* Task type: prompt or exec, mutually exclusive */}
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1.5">
              {t('cron.taskType')}
            </label>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => setForm({ ...form, _type: 'prompt' as const })}
                className={cn(
                  'flex-1 flex items-center justify-center gap-1.5 py-2 rounded-lg text-sm font-medium transition-all',
                  form._type === 'prompt'
                    ? 'bg-accent/15 text-accent ring-1 ring-accent/30'
                    : 'bg-gray-50 dark:bg-white/[0.04] text-gray-500 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-white/[0.08]',
                )}
              >
                <MessageSquare size={14} /> {t('cron.prompt')}
              </button>
              <button
                type="button"
                onClick={() => setForm({ ...form, _type: 'exec' as const })}
                className={cn(
                  'flex-1 flex items-center justify-center gap-1.5 py-2 rounded-lg text-sm font-medium transition-all',
                  form._type === 'exec'
                    ? 'bg-amber-500/15 text-amber-600 dark:text-amber-400 ring-1 ring-amber-500/30'
                    : 'bg-gray-50 dark:bg-white/[0.04] text-gray-500 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-white/[0.08]',
                )}
              >
                <Terminal size={14} /> {t('cron.exec')}
              </button>
            </div>
          </div>

          {form._type === 'prompt' ? (
            <Textarea
              label={t('cron.prompt')}
              value={form.prompt}
              onChange={e => setForm({ ...form, prompt: e.target.value })}
              rows={3}
              placeholder={t('cron.promptPlaceholder')}
            />
          ) : (
            <Input
              label={t('cron.exec')}
              value={form.exec}
              onChange={e => setForm({ ...form, exec: e.target.value })}
              placeholder="npm run report"
            />
          )}

          <Select
            label={t('cron.sessionKey')}
            value={form.session_key}
            onChange={v => setForm({ ...form, session_key: v })}
            options={sessionKeys.map(k => ({ value: k, label: k }))}
            placeholder={t('cron.selectSessionKey')}
          />

          <Select
            label={t('cron.mode')}
            value={form.mode}
            onChange={v => setForm({ ...form, mode: v })}
            options={MODE_OPTIONS.map(m => ({ value: m, label: m }))}
            placeholder={t('cron.modeDefault')}
          />

          <div className="flex items-center gap-6 pt-1">
            <Toggle checked={form.enabled} onChange={v => setForm({ ...form, enabled: v })} label={t('cron.enabled')} />
            <Toggle checked={form.silent} onChange={v => setForm({ ...form, silent: v })} label={t('cron.silent')} />
          </div>

          <div className="flex justify-end gap-2 pt-3 border-t border-gray-100 dark:border-white/[0.06]">
            <Button variant="secondary" onClick={() => setShowForm(false)}>{t('common.cancel')}</Button>
            <Button onClick={handleSave} loading={saving}>
              {isEdit ? t('common.save') : t('cron.add')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
