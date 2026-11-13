import { formatTimestamp } from '@/lib/format';
import { snoozeCountdown, visibleSnoozes } from '@/lib/incidentsFormat';
import type { Snooze } from '@/lib/types';
import styles from './IncidentsPanel.module.css';

interface Props {
  snoozes: Snooze[];
  now: Date;
  onEnd: (s: Snooze) => void;
}

/** Active and upcoming snoozes, each with an End action (via the preview drawer). */
export function SnoozeList({ snoozes, now, onEnd }: Props) {
  const list = visibleSnoozes(snoozes);
  if (list.length === 0) {
    return (
      <p className={styles.muted} data-testid="snoozes-empty">
        No active or upcoming snoozes.
      </p>
    );
  }
  return (
    <ul className={styles.list} data-testid="snooze-list">
      {list.map((s) => (
        <li key={s.name} className={styles.incident} data-testid="snooze-row" data-state={s.state}>
          <div className={styles.incHead}>
            <span className={styles.policy}>{s.display_name || s.name}</span>
            <span className={styles.badge + ' ' + styles.badge_none} data-testid="snooze-state">
              {s.state === 'active' ? 'Active' : 'Scheduled'}
            </span>
            <span className={styles.muted}>{s.project}</span>
            <span className={styles.spacer} />
            <button type="button" className={styles.btn} data-testid="snooze-end" onClick={() => onEnd(s)}>
              End…
            </button>
          </div>
          <div className={styles.facts}>
            <span>{snoozeCountdown(s, now)}</span>
            <span>
              {formatTimestamp(s.start_time)} → {formatTimestamp(s.end_time)}
            </span>
            <span>
              {s.policy_names.length} polic{s.policy_names.length === 1 ? 'y' : 'ies'}: {s.policy_names.map((n) => n.split('/').pop()).join(', ')}
            </span>
          </div>
        </li>
      ))}
    </ul>
  );
}
