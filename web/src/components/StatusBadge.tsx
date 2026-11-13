import type { WeekStatus } from '@/lib/types';
import styles from './StatusBadge.module.css';

interface Props {
  /** `null` = no snapshot for the week ("not fetched"). */
  status: WeekStatus | null;
  /** Snapshot taken at an earlier stage than the week's current status. */
  stale?: boolean;
  testId?: string;
}

const TITLES: Record<WeekStatus, string> = {
  final: 'week ended and the export lag has elapsed',
  settling: 'week ended; export lag not yet elapsed',
  partial: 'week in progress',
};

export function StatusBadge({ status, stale = false, testId }: Props) {
  if (status === null) {
    return (
      <span className={`${styles.badge} ${styles.notFetched}`} title="no snapshot stored for this week" data-testid={testId}>
        not fetched
      </span>
    );
  }
  return (
    <>
      <span className={`${styles.badge} ${styles[status] ?? ''}`} title={TITLES[status]} data-testid={testId}>
        {status}
      </span>
      {stale ? (
        <span
          className={`${styles.badge} ${styles.stale}`}
          title="the snapshot was taken while the week was still in progress or settling; refresh to get the final numbers"
        >
          Refresh for final
        </span>
      ) : null}
    </>
  );
}
