import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Check, CheckCircle2, Clipboard, Laptop, Pencil, RefreshCw, RotateCcw, ScrollText, Server, Unplug, X, XCircle } from 'lucide-react';
import {
  createPairingCode, getControlDashboard, getServiceLogs, renameDevice, restartService, revokeDevice, startDeployRun,
  cancelDeployRun, streamDeployRun, streamDeviceLogs, type Dashboard, type DeployLogLine, type DeviceLogLine, type PairingCode,
} from '@/api/control';

export default function Operations() {
  const { t } = useTranslation();
  const [dashboard, setDashboard] = useState<Dashboard | null>(null);
  const [pairing, setPairing] = useState<PairingCode | null>(null);
  const [logs, setLogs] = useState<Array<{ cursor: number; occurred_at: string; stream: string; line: string }>>([]);
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
	const [selectedRun, setSelectedRun] = useState('');
	const [runLogs, setRunLogs] = useState<DeployLogLine[]>([]);
  const [editingDevice, setEditingDevice] = useState('');
  const [deviceName, setDeviceName] = useState('');
  const [selectedDevice, setSelectedDevice] = useState('');
  const [deviceLogs, setDeviceLogs] = useState<DeviceLogLine[]>([]);

  const refresh = useCallback(async () => {
    try {
      const [next, nextLogs] = await Promise.all([getControlDashboard(), getServiceLogs()]);
      setDashboard(next);
      setLogs(nextLogs.slice(-200));
      setError('');
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  }, []);

  useEffect(() => {
    refresh();
    const timer = window.setInterval(refresh, 5000);
    return () => window.clearInterval(timer);
  }, [refresh]);

	useEffect(() => {
		if (!selectedRun) return;
		const controller = new AbortController();
		setRunLogs([]);
		streamDeployRun(selectedRun, 0, (line) => setRunLogs((current) => [...current.slice(-499), line]), controller.signal)
			.then(refresh)
			.catch((cause) => {
				if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : String(cause));
			});
		return () => controller.abort();
	}, [selectedRun, refresh]);

  useEffect(() => {
    if (!selectedDevice) return;
    const controller = new AbortController();
    setDeviceLogs([]);
    streamDeviceLogs(selectedDevice, 0, (line) => setDeviceLogs((current) => [...current.slice(-499), line]), controller.signal)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : String(cause));
      });
    return () => controller.abort();
  }, [selectedDevice]);

  const act = async (name: string, operation: () => Promise<unknown>) => {
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

  const pairCommand = pairing && dashboard?.public_url ? [
    `curl -fsSL '${dashboard.public_url}/runtime/v1/install.sh' -o cc-connect-runtime-install.sh`,
    `sh cc-connect-runtime-install.sh --server '${dashboard.public_url}' --tag '${dashboard.current_release_tag || 'v0.1.0'}' --code '${pairing.code}'`,
  ].join('\n') : '';

  return (
    <div className="mx-auto w-full max-w-6xl space-y-8">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-gray-900 dark:text-white">{t('control.operations')}</h1>
          <p className="mt-1 text-sm text-gray-500">{t('control.operationsSubtitle')}</p>
        </div>
        <button type="button" title={t('common.refresh')} onClick={refresh} className="rounded-md border border-gray-300 p-2 text-gray-600 dark:border-gray-700 dark:text-gray-300"><RefreshCw size={16} /></button>
      </div>

      {error && <div className="rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">{error}</div>}

      <section className="border-t border-gray-200 pt-5 dark:border-gray-800">
        <div className="mb-4 flex items-center justify-between">
          <h2 className="flex items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white"><Laptop size={16} />{t('control.devices')}</h2>
          <button type="button" disabled={busy !== ''} onClick={() => act('pair', async () => setPairing(await createPairingCode()))}
            className="rounded-md bg-accent px-3 py-2 text-xs font-semibold text-black disabled:opacity-50">{t('control.pairDevice')}</button>
        </div>
        {pairing && (
          <div className="mb-4 space-y-3 rounded-md border border-accent/30 bg-accent/5 px-4 py-3">
            <div className="flex flex-wrap items-center gap-3">
              <code className="text-sm font-semibold text-gray-900 dark:text-white">{pairing.code}</code>
              <span className="text-xs text-gray-500">{t('control.pairExpires', { time: new Date(pairing.expires_at).toLocaleTimeString() })}</span>
            </div>
            <div className="flex items-start gap-2">
              <pre className="min-w-0 flex-1 overflow-auto rounded-md bg-[#111114] p-3 text-xs leading-5 text-gray-300">{pairCommand}</pre>
              <button type="button" title={t('common.copy')} onClick={() => navigator.clipboard.writeText(pairCommand)} className="p-2 text-gray-500"><Clipboard size={15} /></button>
            </div>
          </div>
        )}
        <div className="grid gap-3 md:grid-cols-2">
          {(dashboard?.devices || []).map((device) => (
            <div key={device.id} className="rounded-md border border-gray-200 bg-white p-4 dark:border-gray-800 dark:bg-white/[0.02]">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  {editingDevice === device.id ? (
                    <div className="flex items-center gap-1">
                      <input value={deviceName} onChange={(event) => setDeviceName(event.target.value)} className="min-w-0 rounded-md border border-gray-300 bg-white px-2 py-1 text-sm dark:border-gray-700 dark:bg-gray-900" />
                      <button type="button" title={t('common.save')} onClick={() => act(`rename-${device.id}`, async () => { await renameDevice(device.id, deviceName); setEditingDevice(''); })} className="p-1 text-green-600"><Check size={14} /></button>
                      <button type="button" title={t('common.cancel')} onClick={() => setEditingDevice('')} className="p-1 text-gray-500"><X size={14} /></button>
                    </div>
                  ) : (
                    <div className="flex items-center gap-1">
                      <p className="truncate text-sm font-medium text-gray-900 dark:text-white">{device.name}</p>
                      {!device.revoked_at && <button type="button" title={t('control.renameDevice')} onClick={() => { setEditingDevice(device.id); setDeviceName(device.name); }} className="p-1 text-gray-400"><Pencil size={13} /></button>}
                    </div>
                  )}
                  <p className="mt-1 truncate font-mono text-[11px] text-gray-400">{device.id}</p>
                </div>
                <span className={`flex items-center gap-1 text-xs ${device.online ? 'text-green-600' : 'text-gray-400'}`}>
                  {device.online ? <CheckCircle2 size={13} /> : <Unplug size={13} />}{device.online ? t('control.online') : t('control.offline')}
                </span>
              </div>
              <div className="mt-3 flex items-center gap-3">
                <button type="button" title={t('control.deviceLogs')} onClick={() => setSelectedDevice(device.id)} className="flex items-center gap-1 text-xs text-gray-500"><ScrollText size={13} />{t('control.deviceLogs')}</button>
                {!device.revoked_at && <button type="button" title={t('control.revoke')} onClick={() => act(`revoke-${device.id}`, () => revokeDevice(device.id))}
                  className="flex items-center gap-1 text-xs text-red-600"><XCircle size={13} />{t('control.revoke')}</button>}
              </div>
            </div>
          ))}
          {!dashboard?.devices?.length && <p className="text-sm text-gray-500">{t('control.noDevices')}</p>}
        </div>
        {selectedDevice && <pre className="mt-4 h-40 overflow-auto rounded-md bg-[#111114] p-3 text-xs leading-5 text-gray-300">{deviceLogs.map((line) => `${new Date(line.occurred_at).toLocaleString()} ${line.action} ${line.outcome}`).join('\n') || t('control.noLogs')}</pre>}
      </section>

      <section className="border-t border-gray-200 pt-5 dark:border-gray-800">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h2 className="flex items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white"><Server size={16} />{t('control.service')}</h2>
          <div className="flex gap-2">
            <button type="button" disabled={busy !== '' || !dashboard?.deployment.update} onClick={() => act('update', async () => { const run = await startDeployRun('update'); setSelectedRun(run.id); })} className="rounded-md border border-gray-300 px-3 py-2 text-xs disabled:cursor-not-allowed disabled:opacity-50 dark:border-gray-700">{t('control.update')}</button>
            <button type="button" disabled={busy !== '' || !dashboard?.deployment.rollback} onClick={() => act('rollback', async () => { const run = await startDeployRun('rollback'); setSelectedRun(run.id); })} className="rounded-md border border-gray-300 px-3 py-2 text-xs disabled:cursor-not-allowed disabled:opacity-50 dark:border-gray-700">{t('control.rollback')}</button>
            <button type="button" disabled={busy !== ''} onClick={() => act('restart', async () => { const run = await restartService(); setSelectedRun(run.id); })} className="flex items-center gap-1 rounded-md bg-gray-900 px-3 py-2 text-xs text-white dark:bg-white dark:text-black"><RotateCcw size={13} />{t('control.restart')}</button>
          </div>
        </div>
        {dashboard?.deployment.reason === 'container_host_unavailable' && (
          <p className="mb-4 text-xs text-red-700 dark:text-red-300">{t('control.containerHostUnavailable')}{dashboard.deployment.detail ? `: ${dashboard.deployment.detail}` : ''}</p>
        )}
        <div className="mb-4 flex items-center gap-2 text-sm">
          <span className={`h-2 w-2 rounded-full ${dashboard?.service.running ? 'bg-green-500' : 'bg-red-500'}`} />
          <span className="text-gray-700 dark:text-gray-300">{dashboard?.service.running ? t('control.running') : t('control.stopped')}</span>
          {dashboard?.service.pid && <code className="text-xs text-gray-400">PID {dashboard.service.pid}</code>}
          {dashboard?.current_release_tag && <code className="ml-auto text-xs text-gray-500">{t('control.currentRelease')}: {dashboard.current_release_tag}</code>}
        </div>
        <pre className="h-56 overflow-auto rounded-md bg-[#111114] p-3 text-xs leading-5 text-gray-300">{logs.map((line) => `[${line.stream}] ${line.line}`).join('\n') || t('control.noLogs')}</pre>
      </section>

      <section className="border-t border-gray-200 pt-5 dark:border-gray-800">
        <h2 className="mb-3 text-sm font-semibold text-gray-900 dark:text-white">{t('control.deployRuns')}</h2>
        <div className="divide-y divide-gray-200 border-y border-gray-200 text-sm dark:divide-gray-800 dark:border-gray-800">
          {(dashboard?.runs || []).map((run) => (
            <button type="button" key={run.id} onClick={() => setSelectedRun(run.id)} className="grid w-full grid-cols-[100px_1fr_auto] gap-3 py-3 text-left">
              <span className="font-medium text-gray-800 dark:text-gray-200">{run.kind}</span>
              <span className="truncate font-mono text-xs text-gray-400">{run.target_tag || run.id}</span>
              <span className={run.status === 'succeeded' ? 'text-green-600' : run.status === 'failed' ? 'text-red-600' : 'text-gray-500'}>{run.status}</span>
            </button>
          ))}
        </div>
		{selectedRun && (
			<div className="mt-4">
				<div className="mb-2 flex items-center justify-between gap-3">
					<code className="truncate text-xs text-gray-500">{selectedRun}</code>
					{dashboard?.runs.find((run) => run.id === selectedRun)?.status === 'running' && (
						<button type="button" onClick={() => act('cancel', () => cancelDeployRun(selectedRun))} className="text-xs text-red-600">{t('common.cancel')}</button>
					)}
				</div>
				<pre className="h-48 overflow-auto rounded-md bg-[#111114] p-3 text-xs leading-5 text-gray-300">{runLogs.map((line) => `[${line.stream}] ${line.line}`).join('\n') || t('control.noLogs')}</pre>
			</div>
		)}
      </section>
    </div>
  );
}
