import { useState } from 'react';
import type { ApiError } from '@/lib/api';
import styles from './ErrorBanner.module.css';

export interface BannerData {
  /** Stable key per source ("table", "refresh", …) so re-fetches replace, not stack. */
  key: string;
  /** Short label of what failed, shown before the code ("Cost table"). */
  context?: string;
  code: string;
  message: string;
  hint: string | null;
  requestId?: string | null;
  action?: { label: string; onClick: () => void };
}

export function bannerFromError(key: string, context: string, err: ApiError, action?: BannerData['action']): BannerData {
  return { key, context, code: err.code, message: err.message, hint: err.hint, requestId: err.requestId, action };
}

const COLLAPSE_AFTER = 160;

interface Props {
  banner: BannerData;
  onDismiss?: (key: string) => void;
}

/** "`code` — message" + hint in bold; long messages collapse to two lines. */
export function ErrorBanner({ banner, onDismiss }: Props) {
  const [expanded, setExpanded] = useState(false);
  const long = banner.message.length > COLLAPSE_AFTER;
  return (
    <div className={styles.banner} role="alert" data-testid="error-banner" data-code={banner.code} data-key={banner.key}>
      <div className={styles.main}>
        <div className={expanded || !long ? styles.message : `${styles.message} ${styles.clamped}`}>
          {banner.context ? <span className={styles.context}>{banner.context}: </span> : null}
          <code className={styles.code}>{banner.code}</code> — {banner.message}
        </div>
        {long ? (
          <button type="button" className={styles.link} onClick={() => setExpanded((v) => !v)}>
            {expanded ? 'less' : 'more'}
          </button>
        ) : null}
        {banner.hint ? (
          <div className={styles.hint} data-testid="error-banner-hint">
            {banner.hint}
          </div>
        ) : null}
        {banner.code === 'internal_error' && banner.requestId ? (
          <div className={styles.meta}>
            request id <code>{banner.requestId}</code> — search the server log for it.
          </div>
        ) : null}
        {banner.action ? (
          <button type="button" className={styles.link} onClick={banner.action.onClick}>
            {banner.action.label}
          </button>
        ) : null}
      </div>
      {onDismiss ? (
        <button type="button" className={styles.dismiss} aria-label="Dismiss" onClick={() => onDismiss(banner.key)}>
          ×
        </button>
      ) : null}
    </div>
  );
}
