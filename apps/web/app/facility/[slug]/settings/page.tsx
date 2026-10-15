'use client';

import { FormEvent, useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import FacilityShell from '../FacilityShell';
import { fetchSession, getAccessToken, logout, SessionUser } from '../../../../lib/auth';
import styles from '../portal.module.css';

export default function SettingsPage() {
  return (
    <FacilityShell
      title="Settings"
      description="Account options for your Facinect partner profile."
    >
      <SettingsBody />
    </FacilityShell>
  );
}

function SettingsBody() {
  const router = useRouter();
  const params = useParams<{ slug: string }>();
  const [user, setUser] = useState<SessionUser | null>(null);
  const [newPassword, setNewPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [msg, setMsg] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    (async () => {
      const data = await fetchSession();
      setUser(data?.user || null);
      setLoaded(true);
    })();
  }, []);

  async function onLogout() {
    await logout();
    router.replace('/login');
  }

  async function onSetPassword(e: FormEvent) {
    e.preventDefault();
    setMsg('');
    setErr('');
    if (newPassword.length < 8) {
      setErr('Password must be at least 8 characters.');
      return;
    }
    if (newPassword !== confirm) {
      setErr('New password and confirm do not match.');
      return;
    }
    const token = getAccessToken();
    if (!token) {
      router.replace('/login');
      return;
    }
    setBusy(true);
    try {
      const res = await fetch('/v1/auth/password', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Accept: 'application/json',
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({
          new_password: newPassword,
        }),
      });
      const data = await res.json();
      if (!res.ok) {
        const code = data.error || 'failed';
        if (code === 'password_too_short') {
          setErr('Password must be at least 8 characters.');
        } else if (code === 'invalid_current_password') {
          setErr('Current password is incorrect.');
        } else {
          setErr(code);
        }
        return;
      }
      setMsg('Password updated.');
      setNewPassword('');
      setConfirm('');
      const refreshed = await fetchSession();
      setUser(refreshed?.user || null);
    } catch {
      setErr('Network error. Try again.');
    } finally {
      setBusy(false);
    }
  }

  if (!loaded) {
    return <p>Loading profile…</p>;
  }

  return (
    <>
      <div className={styles.detailGrid}>
        <div className={styles.detailCard} style={{ display: 'flex', alignItems: 'center', gap: 14 }}>
          {user?.avatar_url ? (
            // eslint-disable-next-line @next/next/no-img-element
            <img
              src={user.avatar_url}
              alt=""
              width={56}
              height={56}
              style={{ borderRadius: '50%', objectFit: 'cover' }}
              referrerPolicy="no-referrer"
            />
          ) : (
            <div className={styles.avatar} style={{ width: 56, height: 56, fontSize: 16 }}>
              {(user?.full_name || user?.email || 'U').slice(0, 2).toUpperCase()}
            </div>
          )}
          <div>
            <span>Profile</span>
            <strong>{user?.full_name || '—'}</strong>
            <div style={{ fontSize: 12, color: '#5a6f68', marginTop: 4 }}>{user?.email}</div>
          </div>
        </div>
        <div className={styles.detailCard}>
          <span>Facility</span>
          <strong>{params.slug}</strong>
        </div>
        <div className={styles.detailCard}>
          <span>Password</span>
          <strong>{user?.has_password ? 'Set' : 'Not set'}</strong>
        </div>
      </div>

      <section className={styles.panel} style={{ marginBottom: 16 }}>
        <h2>Set password</h2>
        <p style={{ marginBottom: 14 }}>
          Set or update the login password for this partner account. No current
          password required.
        </p>
        <form onSubmit={onSetPassword} className={styles.settingsForm}>
          <label>
            New password
            <input
              type="password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              autoComplete="new-password"
              required
              minLength={8}
            />
          </label>
          <label>
            Confirm password
            <input
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              autoComplete="new-password"
              required
              minLength={8}
            />
          </label>
          {err && <p className={styles.settingsErr}>{err}</p>}
          {msg && <p className={styles.settingsOk}>{msg}</p>}
          <button type="submit" className={styles.settingsPrimary} disabled={busy}>
            {busy ? 'Saving…' : 'Update password'}
          </button>
        </form>
      </section>

      <section className={styles.panel}>
        <h2>Sign out</h2>
        <p style={{ marginBottom: 14 }}>End this session on this device.</p>
        <button type="button" className={styles.settingsDanger} onClick={onLogout}>
          <i className="fa-solid fa-right-from-bracket" /> Logout
        </button>
      </section>
    </>
  );
}
