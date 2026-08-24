import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Check, Clipboard, ExternalLink, Laptop, Network, Play, RefreshCw, ServerCog } from 'lucide-react';
import {
  configureServer,
  createPairingCode,
  getControlDashboard,
  savePublicURL,
  type Dashboard,
  type PairingCode,
} from '@/api/control';

const initialReleaseTag = 'v0.1.0';

export default function SetupWizard() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const [dashboard, setDashboard] = useState<Dashboard | null>(null);
  const [publicURL, setPublicURL] = useState('');
  const [pairing, setPairing] = useState<PairingCode | null>(null);
  const [enableWeCom, setEnableWeCom] = useState(false);
  const [botID, setBotID] = useState('');
  const [botSecret, setBotSecret] = useState('');
  const [allowFrom, setAllowFrom] = useState('');
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');

  const refresh = useCallback(async () => {
    try {
      const next = await getControlDashboard();
      setDashboard(next);
      setPublicURL((current) => current || next.public_url || '');
      setError('');
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  }, []);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 3000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  useEffect(() => {
    if (dashboard?.configured && dashboard.service.running) navigate('/chat', { replace: true });
  }, [dashboard, navigate]);

  const pairCommand = useMemo(() => {
		if (!pairing || !publicURL) return '';
		const releaseTag = dashboard?.current_release_tag || initialReleaseTag;
		return [
			`curl -fsSL '${publicURL}/runtime/v1/install.sh' -o cc-connect-runtime-install.sh`,
			`sh cc-connect-runtime-install.sh --server '${publicURL}' --tag '${releaseTag}' --code '${pairing.code}'`,
		].join('\n');
  }, [dashboard?.current_release_tag, pairing, publicURL]);

  const proxyConfig = useMemo(() => [
    'location / {',
    '    proxy_pass http://127.0.0.1:9820;',
    '    proxy_http_version 1.1;',
    '    proxy_set_header Host $host;',
    '    proxy_set_header X-Forwarded-Proto https;',
    '    proxy_set_header Upgrade $http_upgrade;',
    '    proxy_set_header Connection "upgrade";',
    '    client_max_body_size 50m;',
    '    proxy_read_timeout 3600s;',
    '    proxy_send_timeout 3600s;',
    '}',
  ].join('\n'), []);

  const perform = async (name: string, operation: () => Promise<void>) => {
    setBusy(name);
    setError('');
    try {
      await operation();
      await refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy('');
    }
  };

  const onlineDevice = dashboard?.devices.some((device) => device.online) || false;
  const runtimeReady = onlineDevice && (dashboard?.workspace_count || 0) > 0;

  return (
    <div className="mx-auto w-full max-w-5xl space-y-7 pb-12">
      <div>
        <h1 className="text-xl font-semibold text-gray-900 dark:text-white">{t('control.setupWizard')}</h1>
        <p className="mt-1 text-sm text-gray-500">{t('control.setupWizardSubtitle')}</p>
      </div>
      {error && <div className="rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">{error}</div>}

      <SetupSection number={2} title={t('control.publicURL')} icon={<Network size={17} />} complete={!!dashboard?.public_url}>
        <div className="grid gap-3 md:grid-cols-[1fr_auto]">
          <input value={publicURL} onChange={(event) => setPublicURL(event.target.value)} placeholder="https://cc.example.com"
            className="rounded-md border border-gray-300 bg-white px-3 py-2 text-sm outline-none focus:border-accent dark:border-gray-700 dark:bg-gray-900" />
          <button type="button" disabled={busy !== '' || !publicURL} onClick={() => perform('url', async () => { await savePublicURL(publicURL); })}
            className="rounded-md bg-gray-900 px-4 py-2 text-sm text-white disabled:opacity-50 dark:bg-white dark:text-black">{t('common.save')}</button>
        </div>
        <div className="mt-4 flex items-center justify-between">
          <span className="text-xs font-medium text-gray-500">{t('control.openrestyConfig')}</span>
          <button type="button" title={t('common.copy')} onClick={() => navigator.clipboard.writeText(proxyConfig)} className="text-gray-500"><Clipboard size={15} /></button>
        </div>
        <pre className="mt-2 overflow-auto rounded-md bg-[#111114] p-3 text-xs leading-5 text-gray-300">{proxyConfig}</pre>
      </SetupSection>

      <SetupSection number={3} title={t('control.runtimePairing')} icon={<Laptop size={17} />} complete={onlineDevice}>
        <div className="flex flex-wrap items-center gap-3">
          <button type="button" disabled={busy !== '' || !dashboard?.public_url} onClick={() => perform('pair', async () => setPairing(await createPairingCode()))}
            className="rounded-md border border-gray-300 px-3 py-2 text-sm disabled:opacity-50 dark:border-gray-700">{t('control.createPairingCode')}</button>
          <button type="button" title={t('common.refresh')} onClick={() => void refresh()} className="rounded-md p-2 text-gray-500 hover:bg-gray-100 dark:hover:bg-white/[0.06]"><RefreshCw size={15} /></button>
          <span className={`text-sm ${onlineDevice ? 'text-green-600' : 'text-gray-500'}`}>{onlineDevice ? t('control.online') : t('control.offline')}</span>
        </div>
        {pairing && <div className="mt-4 space-y-2">
          <div className="flex items-center gap-2"><code className="font-semibold">{pairing.code}</code><span className="text-xs text-gray-500">{t('control.pairExpires', { time: new Date(pairing.expires_at).toLocaleTimeString() })}</span></div>
          <pre className="overflow-auto rounded-md bg-[#111114] p-3 text-xs leading-5 text-gray-300">{pairCommand}</pre>
          <button type="button" onClick={() => navigator.clipboard.writeText(pairCommand)} className="flex items-center gap-1 text-xs text-gray-500"><Clipboard size={13} />{t('common.copy')}</button>
        </div>}
      </SetupSection>

      <SetupSection number={4} title={t('control.codexValidation')} icon={<Check size={17} />} complete={runtimeReady}>
        <span className={`text-sm ${runtimeReady ? 'text-green-600' : 'text-gray-500'}`}>
          {runtimeReady ? t('control.runtimeReady', { count: dashboard?.workspace_count || 0 }) : t('control.runtimeWaiting')}
        </span>
      </SetupSection>

      <SetupSection number={5} title={t('control.wecomOptional')} icon={<ServerCog size={17} />} complete={!enableWeCom || (!!botID && !!botSecret)}>
        <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={enableWeCom} onChange={(event) => setEnableWeCom(event.target.checked)} />{t('control.enableWeCom')}</label>
        {enableWeCom && <div className="mt-3 grid gap-3 md:grid-cols-2">
          <input value={botID} onChange={(event) => setBotID(event.target.value)} placeholder={t('control.wecomBotID')} className="rounded-md border border-gray-300 bg-white px-3 py-2 text-sm dark:border-gray-700 dark:bg-gray-900" />
          <input type="password" value={botSecret} onChange={(event) => setBotSecret(event.target.value)} placeholder={t('control.wecomBotSecret')} className="rounded-md border border-gray-300 bg-white px-3 py-2 text-sm dark:border-gray-700 dark:bg-gray-900" />
          <input value={allowFrom} onChange={(event) => setAllowFrom(event.target.value)} placeholder={t('control.wecomAllowFrom')} className="rounded-md border border-gray-300 bg-white px-3 py-2 text-sm md:col-span-2 dark:border-gray-700 dark:bg-gray-900" />
        </div>}
      </SetupSection>

      <SetupSection number={6} title={t('control.startService')} icon={<Play size={17} />} complete={!!dashboard?.service.running}>
        <div className="flex justify-end">
          <button type="button" disabled={busy !== '' || !dashboard?.public_url || !runtimeReady || (enableWeCom && (!botID || !botSecret))}
            onClick={() => perform('start', async () => {
              await configureServer({ language: i18n.language, enable_wecom: enableWeCom, wecom_bot_id: botID, wecom_bot_secret: botSecret, wecom_allow_from: allowFrom });
              navigate('/chat', { replace: true });
            })}
            className="flex items-center gap-2 rounded-md bg-accent px-5 py-2.5 text-sm font-semibold text-black disabled:opacity-50">
            <Play size={15} />{t('control.startService')}
          </button>
        </div>
      </SetupSection>
      {dashboard?.public_url && <a href={dashboard.public_url} className="inline-flex items-center gap-1 text-xs text-gray-500"><ExternalLink size={12} />{dashboard.public_url}</a>}
    </div>
  );
}

function SetupSection({ number, title, icon, complete, children }: { number: number; title: string; icon: React.ReactNode; complete: boolean; children: React.ReactNode }) {
  return <section className="border-t border-gray-200 pt-5 dark:border-gray-800">
    <div className="mb-4 flex items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white">
      <span className={`grid h-6 w-6 place-items-center rounded-full text-xs ${complete ? 'bg-green-600 text-white' : 'bg-gray-200 text-gray-700 dark:bg-gray-800 dark:text-gray-300'}`}>{complete ? <Check size={14} /> : number}</span>
      {icon}{title}
    </div>
    {children}
  </section>;
}
