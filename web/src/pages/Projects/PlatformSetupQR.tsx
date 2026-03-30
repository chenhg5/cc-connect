import { useState, useEffect, useRef, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { QRCodeSVG } from 'qrcode.react';
import { Loader2, CheckCircle2, XCircle, RefreshCw, Smartphone, RotateCcw } from 'lucide-react';
import { Button } from '@/components/ui';
import {
  setupFeishuBegin, setupFeishuPoll, setupFeishuSave,
  setupWeixinBegin, setupWeixinPoll, setupWeixinSave,
} from '@/api/setup';
import { restartSystem } from '@/api/status';

type PlatformKind = 'feishu' | 'lark' | 'weixin';
type Phase = 'idle' | 'loading' | 'scanning' | 'scanned' | 'completed' | 'expired' | 'denied' | 'error' | 'saving';

interface Props {
  platformType: PlatformKind;
  projectName: string;
  workDir?: string;
  agentType?: string;
  onComplete: () => void;
  onCancel: () => void;
}

export default function PlatformSetupQR({ platformType, projectName, workDir, agentType, onComplete, onCancel }: Props) {
  const { t } = useTranslation();
  const [phase, setPhase] = useState<Phase>('idle');
  const [qrUrl, setQrUrl] = useState('');
  const [error, setError] = useState('');
  const cancelledRef = useRef(false);
  const pollingRef = useRef(false);

  // Feishu state
  const feishuRef = useRef({ deviceCode: '', baseUrl: '', interval: 5 });
  // Weixin state
  const weixinRef = useRef({ qrKey: '' });

  useEffect(() => {
    return () => { cancelledRef.current = true; };
  }, []);

  const isFeishu = platformType === 'feishu' || platformType === 'lark';

  const startFeishuFlow = useCallback(async () => {
    setPhase('loading');
    setError('');
    cancelledRef.current = false;
    pollingRef.current = false;
    try {
      const res = await setupFeishuBegin();
      feishuRef.current = {
        deviceCode: res.device_code,
        baseUrl: '',
        interval: res.interval || 5,
      };
      setQrUrl(res.qr_url);
      setPhase('scanning');
      pollFeishu();
    } catch (e: any) {
      setError(e?.message || String(e));
      setPhase('error');
    }
  }, []);

  const pollFeishu = useCallback(async () => {
    if (pollingRef.current) return;
    pollingRef.current = true;

    const poll = async () => {
      while (!cancelledRef.current) {
        try {
          const res = await setupFeishuPoll(feishuRef.current.deviceCode, feishuRef.current.baseUrl || undefined);
          if (cancelledRef.current) break;
          if (res.base_url) feishuRef.current.baseUrl = res.base_url;
          if (res.slow_down) feishuRef.current.interval += 5;

          switch (res.status) {
            case 'completed':
              setPhase('saving');
              await setupFeishuSave({
                project: projectName,
                app_id: res.app_id!,
                app_secret: res.app_secret!,
                platform_type: res.platform || 'feishu',
                owner_open_id: res.owner_open_id,
                work_dir: workDir,
                agent_type: agentType,
              });
              setPhase('completed');
              pollingRef.current = false;
              return;
            case 'denied':
              setPhase('denied');
              pollingRef.current = false;
              return;
            case 'expired':
              setPhase('expired');
              pollingRef.current = false;
              return;
            case 'error':
              setError(res.error || 'Unknown error');
              setPhase('error');
              pollingRef.current = false;
              return;
          }
        } catch (e: any) {
          if (cancelledRef.current) break;
          setError(e?.message || String(e));
          setPhase('error');
          pollingRef.current = false;
          return;
        }
        await sleep(feishuRef.current.interval * 1000);
      }
      pollingRef.current = false;
    };
    poll();
  }, [projectName]);

  const startWeixinFlow = useCallback(async () => {
    setPhase('loading');
    setError('');
    cancelledRef.current = false;
    pollingRef.current = false;
    try {
      const res = await setupWeixinBegin();
      console.log('[weixin-setup] begin response:', { qr_key: res.qr_key, qr_url_len: res.qr_url?.length, qr_url_prefix: res.qr_url?.slice(0, 80) });
      const qrKey = res.qr_key;
      weixinRef.current.qrKey = qrKey;
      setQrUrl(res.qr_url);
      setPhase('scanning');

      console.log('[weixin-setup] starting poll loop, qrKey=', qrKey, 'cancelledRef=', cancelledRef.current);
      let consecutiveErrors = 0;
      while (!cancelledRef.current) {
        try {
          console.log('[weixin-setup] sending poll request, qrKey=', qrKey);
          const pollRes = await setupWeixinPoll(qrKey);
          console.log('[weixin-setup] poll response:', pollRes);
          consecutiveErrors = 0;
          if (cancelledRef.current) break;

          switch (pollRes.status) {
            case 'scaned':
              setPhase('scanned');
              break;
            case 'confirmed':
              setPhase('saving');
              await setupWeixinSave({
                project: projectName,
                token: pollRes.bot_token!,
                base_url: pollRes.base_url,
                ilink_bot_id: pollRes.ilink_bot_id,
                ilink_user_id: pollRes.ilink_user_id,
                work_dir: workDir,
                agent_type: agentType,
              });
              setPhase('completed');
              return;
            case 'expired':
              setPhase('expired');
              return;
          }
        } catch (e: any) {
          console.error('[weixin-setup] poll error:', e);
          if (cancelledRef.current) break;
          consecutiveErrors++;
          if (consecutiveErrors >= 5) {
            setError(e?.message || String(e));
            setPhase('error');
            return;
          }
        }
        await sleep(500);
      }
    } catch (e: any) {
      console.error('[weixin-setup] begin error:', e);
      setError(e?.message || String(e));
      setPhase('error');
    }
  }, [projectName]);

  const startFlow = isFeishu ? startFeishuFlow : startWeixinFlow;

  const handleRetry = () => {
    cancelledRef.current = false;
    pollingRef.current = false;
    startFlow();
  };

  const platformLabel = isFeishu
    ? t('setup.feishuLabel', 'Feishu / Lark')
    : t('setup.weixinLabel', 'WeChat (ilink)');

  const scanHint = isFeishu
    ? t('setup.scanFeishu', 'Open the Feishu / Lark app and scan the QR code')
    : t('setup.scanWeixin', 'Open WeChat and scan the QR code');

  return (
    <div className="flex flex-col items-center gap-4 py-4">
      {phase === 'idle' && (
        <>
          <Smartphone size={48} className="text-gray-400" />
          <p className="text-sm text-gray-600 dark:text-gray-400 text-center">
            {t('setup.qrDescription', 'Scan a QR code with your phone to quickly connect {{platform}}.', { platform: platformLabel })}
          </p>
          <Button onClick={startFlow}>
            {t('setup.startQR', 'Start QR Setup')}
          </Button>
        </>
      )}

      {phase === 'loading' && (
        <div className="flex flex-col items-center gap-3 py-8">
          <Loader2 size={32} className="animate-spin text-accent" />
          <p className="text-sm text-gray-500">{t('setup.generating', 'Generating QR code...')}</p>
        </div>
      )}

      {(phase === 'scanning' || phase === 'scanned' || phase === 'saving') && (
        <>
          <div className="p-4 bg-white rounded-xl shadow-sm border border-gray-200">
            <QRCodeSVG value={qrUrl} size={200} level="M" />
          </div>
          <p className="text-sm text-gray-600 dark:text-gray-400 text-center max-w-xs">
            {phase === 'scanned'
              ? t('setup.scannedConfirm', 'Scanned! Please confirm on your phone...')
              : phase === 'saving'
                ? t('setup.savingConfig', 'Saving configuration...')
                : scanHint}
          </p>
          {phase === 'scanning' && (
            <div className="flex items-center gap-2 text-xs text-gray-400">
              <Loader2 size={12} className="animate-spin" />
              {t('setup.waitingScan', 'Waiting for scan...')}
            </div>
          )}
          {phase === 'scanned' && (
            <div className="flex items-center gap-2 text-xs text-accent">
              <Loader2 size={12} className="animate-spin" />
              {t('setup.waitingConfirm', 'Waiting for confirmation...')}
            </div>
          )}
          {phase === 'saving' && (
            <div className="flex items-center gap-2 text-xs text-accent">
              <Loader2 size={12} className="animate-spin" />
              {t('setup.savingConfig', 'Saving configuration...')}
            </div>
          )}
        </>
      )}

      {phase === 'completed' && (
        <div className="flex flex-col items-center gap-3 py-4">
          <CheckCircle2 size={48} className="text-green-500" />
          <p className="text-sm font-medium text-green-700 dark:text-green-400">
            {t('setup.completed', 'Platform connected successfully!')}
          </p>
          <p className="text-xs text-gray-500 text-center">
            {t('setup.restartHint', 'Restart the service for the new platform to take effect.')}
          </p>
          <div className="flex gap-2">
            <Button
              variant="secondary"
              onClick={async () => {
                try {
                  await restartSystem();
                  setPhase('restarting' as Phase);
                  setTimeout(() => onComplete(), 3000);
                } catch (e: any) {
                  setError(e?.message || String(e));
                }
              }}
            >
              <RotateCcw size={14} /> {t('setup.restartNow', 'Restart Now')}
            </Button>
            <Button onClick={onComplete}>{t('setup.later', 'Later')}</Button>
          </div>
        </div>
      )}

      {phase === ('restarting' as Phase) && (
        <div className="flex flex-col items-center gap-3 py-4">
          <Loader2 size={32} className="animate-spin text-accent" />
          <p className="text-sm text-gray-600 dark:text-gray-400">
            {t('setup.restarting', 'Restarting service...')}
          </p>
        </div>
      )}

      {phase === 'expired' && (
        <div className="flex flex-col items-center gap-3 py-4">
          <XCircle size={48} className="text-amber-500" />
          <p className="text-sm text-amber-700 dark:text-amber-400">
            {t('setup.expired', 'QR code expired.')}
          </p>
          <Button onClick={handleRetry}>
            <RefreshCw size={14} /> {t('setup.retry', 'Retry')}
          </Button>
        </div>
      )}

      {phase === 'denied' && (
        <div className="flex flex-col items-center gap-3 py-4">
          <XCircle size={48} className="text-red-500" />
          <p className="text-sm text-red-700 dark:text-red-400">
            {t('setup.denied', 'Authorization was denied.')}
          </p>
          <Button onClick={handleRetry}>
            <RefreshCw size={14} /> {t('setup.retry', 'Retry')}
          </Button>
        </div>
      )}

      {phase === 'error' && (
        <div className="flex flex-col items-center gap-3 py-4">
          <XCircle size={48} className="text-red-500" />
          <p className="text-sm text-red-700 dark:text-red-400">{error}</p>
          <Button onClick={handleRetry}>
            <RefreshCw size={14} /> {t('setup.retry', 'Retry')}
          </Button>
        </div>
      )}

      {phase !== 'completed' && (
        <button
          onClick={onCancel}
          className="text-xs text-gray-400 hover:text-gray-600 dark:hover:text-gray-300 mt-2"
        >
          {t('common.cancel')}
        </button>
      )}
    </div>
  );
}

function sleep(ms: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, ms));
}
