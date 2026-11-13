import type { RefreshRequest, WeekID } from '@/lib/types';
import styles from './RefreshButtons.module.css';

interface Props {
  week: WeekID | null;
  /** The refresh currently in flight, or null. */
  running: RefreshRequest | null;
  /** True until /config has loaded (nothing to refresh yet). */
  disabled?: boolean;
  onRefreshWeek: () => void;
  onRefreshAll: () => void;
}

export function RefreshButtons({ week, running, disabled = false, onRefreshWeek, onRefreshAll }: Props) {
  const busy = running !== null;
  const weekLabel = running?.mode === 'week' ? `Refreshing ${running.week}…` : week ? `Refresh ${week}` : 'Refresh';
  const allLabel = running?.mode === 'all' ? 'Refreshing all weeks…' : 'Refresh all weeks';
  return (
    <div className={styles.group} role="group" aria-label="Refresh">
      <button
        type="button"
        className={`${styles.button} ${styles.primary}`}
        data-testid="refresh-week"
        disabled={disabled || busy || week === null}
        aria-busy={running?.mode === 'week' || undefined}
        onClick={onRefreshWeek}
        title="Query BigQuery for the selected week and store a new snapshot"
      >
        {weekLabel}
      </button>
      <button
        type="button"
        className={styles.button}
        data-testid="refresh-all"
        disabled={disabled || busy}
        aria-busy={running?.mode === 'all' || undefined}
        onClick={onRefreshAll}
        title="One BigQuery job for every week first → current"
      >
        {allLabel}
      </button>
    </div>
  );
}
