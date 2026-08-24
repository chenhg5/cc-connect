import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AlertCircle, KeyRound, LogIn, Monitor, Moon, Sun } from 'lucide-react';
import { useAuthStore } from '@/store/auth';
import { useThemeStore } from '@/store/theme';
import { api } from '@/api/client';

const languages = [
  { code: 'en', label: 'EN' }, { code: 'zh', label: '中' }, { code: 'zh-TW', label: '繁' },
  { code: 'ja', label: '日' }, { code: 'ko', label: '한' }, { code: 'es', label: 'ES' }, { code: 'ru', label: 'RU' },
];

export default function Login() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const login = useAuthStore((state) => state.login);
  const setup = useAuthStore((state) => state.setup);
  const { theme, setTheme } = useThemeStore();
  const [setupRequired, setSetupRequired] = useState<boolean | null>(null);
  const [setupToken, setSetupToken] = useState('');
  const [password, setPassword] = useState('');
  const [confirmation, setConfirmation] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    api.get<{ required: boolean }>('/auth/setup')
      .then((result) => setSetupRequired(result.required))
      .catch((cause) => setError(cause instanceof Error ? cause.message : String(cause)));
  }, []);

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!password || setupRequired === null) return;
    if (setupRequired && password !== confirmation) {
      setError(t('control.passwordMismatch'));
      return;
    }
    setLoading(true);
    setError('');
    try {
      if (setupRequired) await setup(setupToken.trim(), password);
      else await login(password);
      navigate(setupRequired ? '/setup' : '/', { replace: true });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setLoading(false);
    }
  };

  const themeIcons = { light: Sun, dark: Moon, system: Monitor };
  const nextTheme: Record<string, 'light' | 'dark' | 'system'> = { light: 'dark', dark: 'system', system: 'light' };
  const ThemeIcon = themeIcons[theme];

  return (
    <div className="min-h-screen bg-gray-100 px-4 py-12 dark:bg-[#0a0a0c]">
      <div className="fixed right-4 top-4 flex items-center gap-2">
        <div className="flex overflow-hidden rounded-md border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-900">
          {languages.map((language) => (
            <button key={language.code} type="button" onClick={() => i18n.changeLanguage(language.code)}
              className={`px-2.5 py-1.5 text-xs ${i18n.language === language.code ? 'bg-accent/15 text-accent' : 'text-gray-500'}`}>
              {language.label}
            </button>
          ))}
        </div>
        <button type="button" title={t('common.theme')} onClick={() => setTheme(nextTheme[theme])}
          className="rounded-md border border-gray-200 bg-white p-2 text-gray-500 dark:border-gray-700 dark:bg-gray-900">
          <ThemeIcon size={16} />
        </button>
      </div>

      <div className="mx-auto mt-[8vh] w-full max-w-sm rounded-lg border border-gray-200 bg-white p-7 shadow-sm dark:border-gray-800 dark:bg-[#111114]">
        <div className="mb-6 flex items-center gap-3">
          <div className="grid h-10 w-10 place-items-center rounded-md bg-gray-900 text-white dark:bg-white dark:text-black">
            {setupRequired ? <KeyRound size={19} /> : <LogIn size={19} />}
          </div>
          <div>
            <h1 className="text-lg font-semibold text-gray-900 dark:text-white">
              {setupRequired ? t('control.firstSetup') : t('login.title')}
            </h1>
            <p className="text-xs text-gray-500">
              {setupRequired ? t('control.setupSubtitle') : t('control.loginSubtitle')}
            </p>
          </div>
        </div>

        {error && <div className="mb-4 flex gap-2 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300"><AlertCircle size={16} />{error}</div>}

        <form onSubmit={submit} className="space-y-4">
          {setupRequired && (
            <label className="block text-sm text-gray-700 dark:text-gray-300">
              <span className="mb-1.5 block font-medium">{t('control.setupToken')}</span>
              <input type="password" value={setupToken} onChange={(event) => setSetupToken(event.target.value)} autoFocus
                className="w-full rounded-md border border-gray-300 bg-white px-3 py-2.5 font-mono text-sm outline-none focus:border-accent dark:border-gray-700 dark:bg-gray-900" />
            </label>
          )}
          <label className="block text-sm text-gray-700 dark:text-gray-300">
            <span className="mb-1.5 block font-medium">{t('control.adminPassword')}</span>
            <input type="password" value={password} onChange={(event) => setPassword(event.target.value)} autoFocus={!setupRequired}
              autoComplete={setupRequired ? 'new-password' : 'current-password'}
              className="w-full rounded-md border border-gray-300 bg-white px-3 py-2.5 text-sm outline-none focus:border-accent dark:border-gray-700 dark:bg-gray-900" />
          </label>
          {setupRequired && (
            <label className="block text-sm text-gray-700 dark:text-gray-300">
              <span className="mb-1.5 block font-medium">{t('control.confirmPassword')}</span>
              <input type="password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} autoComplete="new-password"
                className="w-full rounded-md border border-gray-300 bg-white px-3 py-2.5 text-sm outline-none focus:border-accent dark:border-gray-700 dark:bg-gray-900" />
            </label>
          )}
          <button type="submit" disabled={loading || setupRequired === null || !password || (setupRequired && !setupToken)}
            className="flex w-full items-center justify-center gap-2 rounded-md bg-accent px-4 py-2.5 text-sm font-semibold text-black disabled:opacity-50">
            {setupRequired ? <KeyRound size={16} /> : <LogIn size={16} />}
            {loading ? t('common.loading') : setupRequired ? t('control.createAdmin') : t('login.connect')}
          </button>
        </form>
      </div>
    </div>
  );
}
