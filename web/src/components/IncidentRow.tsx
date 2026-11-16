import { useState } from 'react';
import { formatTimestamp } from '@/lib/format';
import {
  SNOOZE_PRESETS,
  durationSeconds,
  formatDuration,
  parseCustomHours,
  relativeTime,
  severityLabel,
  severityTone,
} from '@/lib/incidentsFormat';
import type { AlertSeverity, Incident } from '@/lib/types';
import styles from './IncidentsPanel.module.css';

export function SeverityBadge({ severity }: { severity: AlertSeverity | '' }) {
  const tone = severityTone(severity);
  return (
    <span className={`${styles.badge} ${styles[`badge_${tone}`] ?? ''}`} data-testid="severity-badge" data-severity={severity}>
      {severityLabel(severity)}
    </span>
  );
}

interface Props {
  incident: Incident;
  now: Date;
  /** Opens the preview/apply drawer for a snooze of this incident's policy. */
  onSnooze: (incident: Incident, seconds: number) => void;
  /** Initial picker state (static render tests). */
  initialPicker?: boolean;
}

const CUSTOM = 'custom';

export function IncidentRow({ incident, now, onSnooze, initialPicker = false }: Props) {
  const [picker, setPicker] = useState(initialPicker);
  const [choice, setChoice] = useState(String(SNOOZE_PRESETS[0]!.seconds));
  const [custom, setCustom] = useState('2');
  const [error, setError] = useState<string | null>(null);

  const dur = durationSeconds(incident.opened_at, incident.closed_at, now);
  const resource = incident.resource_name || '—';

  function submit() {
    if (choice === CUSTOM) {
      const p = parseCustomHours(custom);
      if (!p.ok) {
        setError(p.error);
        return;
      }
      setError(null);
      onSnooze(incident, p.seconds);
      return;
    }
    setError(null);
    onSnooze(incident, Number(choice));
  }

  return (
    <li className={`${styles.incident} ${styles[`tone_${severityTone(incident.severity)}`] ?? ''}`} data-testid="incident-row" data-incident-id={incident.id}>
      <div className={styles.incHead}>
        <SeverityBadge severity={incident.severity} />
        <span className={styles.policy} data-testid="incident-policy">
          {incident.policy_display_name}
        </span>
        <span className={styles.muted}>{incident.project}</span>
        <span className={styles.spacer} />
        <a className={styles.link} href={incident.url} target="_blank" rel="noopener noreferrer" data-testid="incident-link">
          Open in GCP
        </a>
        <button type="button" className={styles.btn} data-testid="incident-snooze" aria-expanded={picker} onClick={() => setPicker((v) => !v)}>
          Snooze…
        </button>
      </div>
      <div className={styles.facts}>
        <span data-testid="incident-resource">
          Resource: <strong>{resource}</strong>
          {incident.resource_type ? <span className={styles.muted}> ({incident.resource_type})</span> : null}
        </span>
        <span data-testid="incident-started" title={incident.opened_at}>
          Started {relativeTime(incident.opened_at, now)} · {formatTimestamp(incident.opened_at)}
        </span>
        <span data-testid="incident-duration">Open for {dur === null ? '—' : formatDuration(dur)}</span>
      </div>
      {incident.summary ? <div className={styles.muted}>{incident.summary}</div> : null}
      {picker ? (
        <div className={styles.snoozeBox} data-testid="snooze-picker">
          <label>
            Mute this policy for{' '}
            <select className={styles.select} data-testid="snooze-duration" value={choice} onChange={(e) => setChoice(e.target.value)}>
              {SNOOZE_PRESETS.map((p) => (
                <option key={p.seconds} value={String(p.seconds)}>
                  {p.label}
                </option>
              ))}
              <option value={CUSTOM}>Custom (up to 7 days)…</option>
            </select>
          </label>
          {choice === CUSTOM ? (
            <label>
              <input
                className={styles.input}
                data-testid="snooze-custom-hours"
                aria-label="Custom snooze hours"
                inputMode="decimal"
                value={custom}
                onChange={(e) => {
                  setCustom(e.target.value);
                  setError(null);
                }}
              />{' '}
              hours
            </label>
          ) : null}
          <button type="button" className={styles.btn} data-testid="snooze-preview" onClick={submit}>
            Preview snooze
          </button>
          {error ? (
            <span className={styles.fieldError} role="alert" data-testid="snooze-error">
              {error}
            </span>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}
