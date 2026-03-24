import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Clock, Plus, Trash2, Terminal, MessageSquare } from 'lucide-react';
import { Card, Button, Badge, Modal, Input, Textarea, EmptyState } from '@/components/ui';
import { listCronJobs, createCronJob, deleteCronJob, type CronJob } from '@/api/cron';
import { formatTime } from '@/lib/utils';

export default function CronList() {
  const { t } = useTranslation();
  const [jobs, setJobs] = useState<CronJob[]>([]);
  const [loading, setLoading] = useState(true);
  const [showAdd, setShowAdd] = useState(false);
  const [form, setForm] = useState({ project: '', session_key: '', cron_expr: '', prompt: '', exec: '', description: '', silent: false });

  const fetchJobs = useCallback(async () => {
    setLoading(true);
    try {
      const data = await listCronJobs();
      setJobs(data.jobs || []);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchJobs();
    const handler = () => fetchJobs();
    window.addEventListener('cc:refresh', handler);
    return () => window.removeEventListener('cc:refresh', handler);
  }, [fetchJobs]);

  const handleCreate = async () => {
    const body: any = { ...form };
    if (!body.prompt) delete body.prompt;
    if (!body.exec) delete body.exec;
    await createCronJob(body);
    setShowAdd(false);
    setForm({ project: '', session_key: '', cron_expr: '', prompt: '', exec: '', description: '', silent: false });
    fetchJobs();
  };

  const handleDelete = async (id: string) => {
    if (!confirm(t('common.confirmDelete'))) return;
    await deleteCronJob(id);
    fetchJobs();
  };

  if (loading && jobs.length === 0) {
    return <div className="flex items-center justify-center h-64 text-gray-400 animate-pulse">Loading...</div>;
  }

  return (
    <div className="space-y-4 animate-fade-in">
      <div className="flex justify-between items-center">
        <h2 className="text-lg font-semibold text-gray-900 dark:text-white">{t('cron.title')}</h2>
        <Button onClick={() => setShowAdd(true)}><Plus size={16} /> {t('cron.add')}</Button>
      </div>

      {jobs.length === 0 ? (
        <EmptyState message={t('cron.noJobs')} icon={Clock} />
      ) : (
        <div className="space-y-3">
          {jobs.map((job) => (
            <Card key={job.id}>
              <div className="flex items-start justify-between">
                <div className="flex-1">
                  <div className="flex items-center gap-2 mb-1">
                    {job.prompt ? <MessageSquare size={14} className="text-blue-400" /> : <Terminal size={14} className="text-amber-400" />}
                    <span className="font-medium text-gray-900 dark:text-white text-sm">{job.description || job.id}</span>
                    <Badge variant={job.enabled ? 'success' : 'default'}>{job.enabled ? t('cron.enabled') : 'disabled'}</Badge>
                    {job.silent && <Badge variant="default">silent</Badge>}
                  </div>
                  <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-gray-400 mt-2">
                    <span><strong>{t('cron.expression')}:</strong> {job.cron_expr}</span>
                    <span><strong>{t('cron.project')}:</strong> {job.project}</span>
                    {job.prompt && <span><strong>{t('cron.prompt')}:</strong> {job.prompt.length > 60 ? job.prompt.slice(0, 60) + '...' : job.prompt}</span>}
                    {job.exec && <span><strong>{t('cron.exec')}:</strong> {job.exec}</span>}
                    {job.last_run && <span><strong>{t('cron.lastRun')}:</strong> {formatTime(job.last_run)}</span>}
                  </div>
                  {job.last_error && <p className="text-xs text-red-500 mt-1">{job.last_error}</p>}
                </div>
                <Button size="sm" variant="danger" onClick={() => handleDelete(job.id)}>
                  <Trash2 size={14} />
                </Button>
              </div>
            </Card>
          ))}
        </div>
      )}

      <Modal open={showAdd} onClose={() => setShowAdd(false)} title={t('cron.add')}>
        <div className="space-y-3">
          <Input label={t('cron.project')} value={form.project} onChange={(e) => setForm({...form, project: e.target.value})} />
          <Input label={t('cron.sessionKey')} value={form.session_key} onChange={(e) => setForm({...form, session_key: e.target.value})} placeholder="telegram:123:456" />
          <Input label={t('cron.expression')} value={form.cron_expr} onChange={(e) => setForm({...form, cron_expr: e.target.value})} placeholder="0 6 * * *" />
          <Input label={t('cron.description')} value={form.description} onChange={(e) => setForm({...form, description: e.target.value})} />
          <Textarea label={t('cron.prompt')} value={form.prompt} onChange={(e) => setForm({...form, prompt: e.target.value})} rows={3} placeholder="Prompt to send to agent..." />
          <Input label={t('cron.exec')} value={form.exec} onChange={(e) => setForm({...form, exec: e.target.value})} placeholder="npm run report" />
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="secondary" onClick={() => setShowAdd(false)}>{t('common.cancel')}</Button>
            <Button onClick={handleCreate}>{t('cron.add')}</Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
