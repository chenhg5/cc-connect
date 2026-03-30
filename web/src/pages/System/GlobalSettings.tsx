import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Save, Loader2 } from 'lucide-react';
import { Card, Button, Input } from '@/components/ui';
import { getGlobalSettings, updateGlobalSettings, type GlobalSettings as GS } from '@/api/settings';
import { cn } from '@/lib/utils';

const LOG_LEVELS = ['debug', 'info', 'warn', 'error'];
const ATTACHMENT_OPTS = ['', 'on', 'off'];
const LANGUAGES = ['en', 'zh', 'zh-TW', 'ja', 'es'];

function Toggle({ value, onChange, label, hint }: { value: boolean; onChange: (v: boolean) => void; label: string; hint?: string }) {
  return (
    <div className="flex items-center justify-between">
      <div>
        <label className="text-sm font-medium text-gray-700 dark:text-gray-300">{label}</label>
        {hint && <p className="text-[11px] text-gray-400 mt-0.5">{hint}</p>}
      </div>
      <button
        type="button"
        onClick={() => onChange(!value)}
        className={cn(
          'w-10 h-6 rounded-full transition-colors shrink-0',
          value ? 'bg-accent' : 'bg-gray-300 dark:bg-gray-700',
        )}
      >
        <div className={cn('w-4 h-4 bg-white rounded-full transition-transform mx-1', value ? 'translate-x-4' : 'translate-x-0')} />
      </button>
    </div>
  );
}

function Select({ value, onChange, label, options, hint }: { value: string; onChange: (v: string) => void; label: string; options: { value: string; label: string }[]; hint?: string }) {
  return (
    <div>
      <label className="block text-xs font-medium text-gray-600 dark:text-gray-400 mb-1">{label}</label>
      {hint && <p className="text-[11px] text-gray-400 mb-1.5">{hint}</p>}
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="w-full px-3 py-2 text-sm rounded-lg border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-800 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/50"
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>
    </div>
  );
}

function NumberInput({ value, onChange, label, hint, min, max }: { value: number; onChange: (v: number) => void; label: string; hint?: string; min?: number; max?: number }) {
  return (
    <div>
      <label className="block text-xs font-medium text-gray-600 dark:text-gray-400 mb-1">{label}</label>
      {hint && <p className="text-[11px] text-gray-400 mb-1.5">{hint}</p>}
      <input
        type="number"
        value={value}
        min={min}
        max={max}
        onChange={(e) => onChange(parseInt(e.target.value, 10) || 0)}
        className="w-full px-3 py-2 text-sm rounded-lg border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-800 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/50"
      />
    </div>
  );
}

export default function GlobalSettings() {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [msg, setMsg] = useState('');

  const [language, setLanguage] = useState('en');
  const [quiet, setQuiet] = useState(false);
  const [attachmentSend, setAttachmentSend] = useState('');
  const [logLevel, setLogLevel] = useState('info');
  const [idleTimeout, setIdleTimeout] = useState(120);
  const [thinkingMaxLen, setThinkingMaxLen] = useState(300);
  const [toolMaxLen, setToolMaxLen] = useState(500);
  const [spEnabled, setSpEnabled] = useState(true);
  const [spInterval, setSpInterval] = useState(1500);
  const [rlMax, setRlMax] = useState(20);
  const [rlWindow, setRlWindow] = useState(60);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const s = await getGlobalSettings();
      setLanguage(s.language || 'en');
      setQuiet(!!s.quiet);
      setAttachmentSend(s.attachment_send || '');
      setLogLevel(s.log_level || 'info');
      setIdleTimeout(s.idle_timeout_mins ?? 120);
      setThinkingMaxLen(s.thinking_max_len ?? 300);
      setToolMaxLen(s.tool_max_len ?? 500);
      setSpEnabled(s.stream_preview_enabled ?? true);
      setSpInterval(s.stream_preview_interval_ms ?? 1500);
      setRlMax(s.rate_limit_max_messages ?? 20);
      setRlWindow(s.rate_limit_window_secs ?? 60);
    } catch {
      // ignore
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  const handleSave = async () => {
    setSaving(true);
    setMsg('');
    try {
      await updateGlobalSettings({
        language,
        quiet,
        attachment_send: attachmentSend,
        log_level: logLevel,
        idle_timeout_mins: idleTimeout,
        thinking_max_len: thinkingMaxLen,
        tool_max_len: toolMaxLen,
        stream_preview_enabled: spEnabled,
        stream_preview_interval_ms: spInterval,
        rate_limit_max_messages: rlMax,
        rate_limit_window_secs: rlWindow,
      });
      setMsg(t('common.success'));
      setTimeout(() => setMsg(''), 3000);
    } catch (e: any) {
      setMsg(e.message || 'Error');
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <Card>
        <div className="flex items-center gap-2 text-gray-400 animate-pulse py-8 justify-center">
          <Loader2 size={16} className="animate-spin" /> {t('common.loading', 'Loading...')}
        </div>
      </Card>
    );
  }

  return (
    <div className="space-y-5">
      {/* General */}
      <Card>
        <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-4">{t('settings.general', 'General')}</h3>
        <div className="space-y-4 max-w-lg">
          <Select
            label={t('settings.language', 'Language')}
            value={language}
            onChange={setLanguage}
            options={LANGUAGES.map((l) => ({ value: l, label: l }))}
          />
          <Toggle label={t('settings.quiet', 'Quiet mode')} value={quiet} onChange={setQuiet} hint={t('settings.quietHint', 'Suppress start / end notifications globally')} />
          <Select
            label={t('settings.attachmentSend', 'Attachment send')}
            value={attachmentSend}
            onChange={setAttachmentSend}
            hint={t('settings.attachmentSendHint', 'Send file/image attachments back to platform')}
            options={ATTACHMENT_OPTS.map((v) => ({ value: v, label: v || t('settings.default', 'default') }))}
          />
          <NumberInput
            label={t('settings.idleTimeout', 'Idle timeout (min)')}
            value={idleTimeout}
            onChange={setIdleTimeout}
            min={0}
            hint={t('settings.idleTimeoutHint', 'Auto-stop agent after N minutes of inactivity; 0 = disabled')}
          />
        </div>
      </Card>

      {/* Display */}
      <Card>
        <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-4">{t('settings.display', 'Display')}</h3>
        <div className="space-y-4 max-w-lg">
          <NumberInput
            label={t('settings.thinkingMaxLen', 'Thinking max length')}
            value={thinkingMaxLen}
            onChange={setThinkingMaxLen}
            min={0}
            hint={t('settings.thinkingMaxLenHint', 'Max characters for thinking messages; 0 = no truncation')}
          />
          <NumberInput
            label={t('settings.toolMaxLen', 'Tool max length')}
            value={toolMaxLen}
            onChange={setToolMaxLen}
            min={0}
            hint={t('settings.toolMaxLenHint', 'Max characters for tool use messages; 0 = no truncation')}
          />
        </div>
      </Card>

      {/* Stream preview */}
      <Card>
        <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-4">{t('settings.streamPreview', 'Stream preview')}</h3>
        <div className="space-y-4 max-w-lg">
          <Toggle label={t('settings.streamPreviewEnabled', 'Enable')} value={spEnabled} onChange={setSpEnabled} hint={t('settings.streamPreviewEnabledHint', 'Show real-time streaming updates in IM')} />
          <NumberInput
            label={t('settings.streamPreviewInterval', 'Interval (ms)')}
            value={spInterval}
            onChange={setSpInterval}
            min={100}
            hint={t('settings.streamPreviewIntervalHint', 'Minimum milliseconds between preview updates')}
          />
        </div>
      </Card>

      {/* Rate limit */}
      <Card>
        <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-4">{t('settings.rateLimit', 'Rate limit')}</h3>
        <div className="space-y-4 max-w-lg">
          <NumberInput
            label={t('settings.rlMaxMessages', 'Max messages')}
            value={rlMax}
            onChange={setRlMax}
            min={0}
            hint={t('settings.rlMaxMessagesHint', 'Max messages per window; 0 = disabled')}
          />
          <NumberInput
            label={t('settings.rlWindowSecs', 'Window (sec)')}
            value={rlWindow}
            onChange={setRlWindow}
            min={1}
            hint={t('settings.rlWindowSecsHint', 'Time window in seconds')}
          />
        </div>
      </Card>

      {/* Log */}
      <Card>
        <h3 className="text-sm font-semibold text-gray-900 dark:text-white mb-4">{t('settings.log', 'Log')}</h3>
        <div className="space-y-4 max-w-lg">
          <Select
            label={t('settings.logLevel', 'Log level')}
            value={logLevel}
            onChange={setLogLevel}
            options={LOG_LEVELS.map((l) => ({ value: l, label: l }))}
          />
        </div>
      </Card>

      {/* Save */}
      <div className="max-w-lg">
        <Button loading={saving} onClick={handleSave}>
          <Save size={16} /> {t('common.save')}
        </Button>
        {msg && (
          <p className={cn('text-sm mt-2', msg === t('common.success') ? 'text-accent' : 'text-red-500')}>{msg}</p>
        )}
      </div>
    </div>
  );
}
