'use client';

import { type FormEvent, useState } from 'react';
import { postLogin, toApiError } from '@/lib/api';
import { afterLogin } from '@/lib/login';
import styles from './LoginForm.module.css';

/** The /login card: one password field (there is no username). */
export function LoginForm() {
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await postLogin(password);
      window.location.replace(afterLogin(new URLSearchParams(window.location.search).get('next'), window.location.hash));
    } catch (err) {
      const apiErr = toApiError(err);
      if (apiErr.code === 'not_found') {
        // No CLOUD0_PASSWORD on this server: nothing to sign in to.
        window.location.replace('/');
        return;
      }
      setError(apiErr.message);
      setBusy(false);
    }
  }

  return (
    <main className={styles.page}>
      <form className={styles.card} onSubmit={onSubmit} data-testid="login-form">
        <div>
          <h1 className={styles.brand}>cloud0</h1>
          <p className={styles.lead}>Weekly GCP cost review</p>
        </div>
        <label className={styles.field}>
          <span>Password</span>
          <input
            type="password"
            name="password"
            autoComplete="current-password"
            autoFocus
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            aria-invalid={error !== null}
          />
        </label>
        {error !== null && (
          <p className={styles.error} role="alert">
            {error}
          </p>
        )}
        <button type="submit" className={styles.primary} disabled={busy || password === ''}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </main>
  );
}
