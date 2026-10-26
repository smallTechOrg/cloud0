import { useEffect, useState } from 'react';
import { ApiError, toApiError } from '@/lib/api';
import { formatThreshold } from '@/lib/format';
import type { Settings } from '@/lib/types';
import styles from './ThresholdsEditor.module.css';

interface Props {
  settings: Settings | null;
  /** Error from GET /settings, shown inline instead of the thresholds. */
  loadError: ApiError | null;
  onSave: (pct: number, absUsd: number) => Promise<void>;
}

/**
 * "Anomaly thresholds: ≥ 20 % and ≥ $50 [Edit]" with a popover holding two
 * numeric inputs; Save → PUT /settings (the parent re-fetches the table).
 */
export function ThresholdsEditor({ settings, loadError, onSave }: Props) {
  const [open, setOpen] = useState(false);
  const [pct, setPct] = useState('');
  const [abs, setAbs] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open && settings) {
      setPct(String(settings.anomaly_pct_threshold));
      setAbs(String(settings.anomaly_abs_threshold_usd));
    }
  }, [open, settings]);

  async function save() {
    const p = Number(pct);
    const a = Number(abs);
    if (pct.trim() === '' || abs.trim() === '' || !Number.isFinite(p) || !Number.isFinite(a) || p < 0 || a < 0) {
      setError('Both thresholds must be finite numbers ≥ 0.');
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await onSave(p, a);
      setOpen(false);
    } catch (err) {
      const e = toApiError(err);
      setError(`${e.code} — ${e.message}${e.hint ? ` ${e.hint}` : ''}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className={styles.wrap}>
      <span className={styles.label} data-testid="thresholds-label">
        Anomaly thresholds:{' '}
        {settings ? (
          <strong>
            ≥ {formatThreshold(settings.anomaly_pct_threshold)} % and ≥ ${formatThreshold(settings.anomaly_abs_threshold_usd)}
          </strong>
        ) : loadError ? (
          <span className={styles.unavailable} title={`${loadError.code} — ${loadError.message}${loadError.hint ? ` ${loadError.hint}` : ''}`}>
            unavailable ({loadError.code})
          </span>
        ) : (
          <span className={styles.unavailable}>loading…</span>
        )}
      </span>
      <button
        type="button"
        className={styles.edit}
        data-testid="thresholds-edit"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        disabled={settings === null}
      >
        Edit
      </button>
      {open ? (
        <div className={styles.popover} role="dialog" aria-label="Edit anomaly thresholds" data-testid="thresholds-popover">
          <label className={styles.field}>
            <span>Change ≥ (%)</span>
            <input
              className={styles.input}
              data-testid="thresholds-pct"
              type="number"
              min={0}
              step="any"
              value={pct}
              onChange={(e) => setPct(e.target.value)}
            />
          </label>
          <label className={styles.field}>
            <span>and ≥ (USD)</span>
            <input
              className={styles.input}
              data-testid="thresholds-abs"
              type="number"
              min={0}
              step="any"
              value={abs}
              onChange={(e) => setAbs(e.target.value)}
            />
          </label>
          <p className={styles.help}>A cell is flagged when its week-over-week change exceeds both thresholds.</p>
          {error ? (
            <p className={styles.error} role="alert" data-testid="thresholds-error">
              {error}
            </p>
          ) : null}
          <div className={styles.actions}>
            <button type="button" className={styles.save} data-testid="thresholds-save" onClick={save} disabled={saving}>
              {saving ? 'Saving…' : 'Save'}
            </button>
            <button type="button" className={styles.cancel} data-testid="thresholds-cancel" onClick={() => setOpen(false)} disabled={saving}>
              Cancel
            </button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
