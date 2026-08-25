import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { KeyRound, UserRound } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Card, Button } from '@/components/ui';
import { changeAdministratorPassword, getAdministratorProfile, type AdministratorProfile } from '@/api/control';
import { useAuthStore } from '@/store/auth';

export default function Profile() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const logout = useAuthStore((s) => s.logout);
  const [profile, setProfile] = useState<AdministratorProfile | null>(null);
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  useEffect(() => { getAdministratorProfile().then(setProfile).catch((e) => setError(e.message)); }, []);

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setError('');
    setMessage('');
    if (newPassword !== confirmPassword) { setError(t('control.passwordMismatch')); return; }
    setLoading(true);
    try {
      await changeAdministratorPassword(currentPassword, newPassword);
      setMessage(t('control.passwordChanged'));
      await logout();
      navigate('/login', { replace: true });
    } catch (e: any) {
      setError(e?.message || String(e));
    } finally { setLoading(false); }
  };

  return (
    <div className="mx-auto w-full max-w-2xl space-y-6 animate-fade-in">
      <div><h1 className="text-xl font-semibold text-gray-900 dark:text-white">{t('control.profileTitle')}</h1></div>
      {error && <div role="alert" className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">{error}</div>}
      {message && <div role="status" className="rounded-lg border border-accent/30 bg-accent/10 px-4 py-3 text-sm text-accent">{message}</div>}
      <Card>
        <div className="flex items-center gap-3"><UserRound size={18} className="text-accent" /><h2 className="text-sm font-semibold">{t('control.account')}</h2></div>
        <div className="mt-4 grid gap-3 text-sm sm:grid-cols-2">
          <div><div className="text-xs text-gray-500">{t('control.adminUsername')}</div><div className="mt-1 font-medium">{profile?.username || '...'}</div></div>
          <div><div className="text-xs text-gray-500">{t('dashboard.version')}</div><div className="mt-1 font-medium">{profile ? new Date(profile.created_at).toLocaleString() : '...'}</div></div>
        </div>
      </Card>
      <Card>
        <div className="flex items-center gap-3"><KeyRound size={18} className="text-accent" /><h2 className="text-sm font-semibold">{t('control.changePassword')}</h2></div>
        <form onSubmit={submit} className="mt-4 space-y-4">
          <label className="block text-sm"><span className="mb-1.5 block text-gray-600 dark:text-gray-300">{t('control.currentPassword')}</span><input required minLength={12} type="password" value={currentPassword} onChange={(e) => setCurrentPassword(e.target.value)} autoComplete="current-password" className="w-full rounded-md border border-gray-300 bg-white px-3 py-2.5 dark:border-gray-700 dark:bg-gray-900" /></label>
          <label className="block text-sm"><span className="mb-1.5 block text-gray-600 dark:text-gray-300">{t('control.newPassword')}</span><input required minLength={12} type="password" value={newPassword} onChange={(e) => setNewPassword(e.target.value)} autoComplete="new-password" className="w-full rounded-md border border-gray-300 bg-white px-3 py-2.5 dark:border-gray-700 dark:bg-gray-900" /></label>
          <label className="block text-sm"><span className="mb-1.5 block text-gray-600 dark:text-gray-300">{t('control.confirmPassword')}</span><input required minLength={12} type="password" value={confirmPassword} onChange={(e) => setConfirmPassword(e.target.value)} autoComplete="new-password" className="w-full rounded-md border border-gray-300 bg-white px-3 py-2.5 dark:border-gray-700 dark:bg-gray-900" /></label>
          <Button type="submit" loading={loading}>{t('control.savePassword')}</Button>
        </form>
      </Card>
    </div>
  );
}
