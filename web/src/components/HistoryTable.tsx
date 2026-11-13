import { formatTimestamp } from '@/lib/format';
import { sortHistoryRows, worstSeverity } from '@/lib/incidentsFormat';
import type { AlertHistoryResponse } from '@/lib/types';
import { SeverityBadge } from './IncidentRow';
import tableStyles from './CostTable.module.css';
import styles from './IncidentsPanel.module.css';

/** Per-policy alert history for one ISO week (sorted by times fired). */
export function HistoryTable({ data }: { data: AlertHistoryResponse }) {
  const rows = sortHistoryRows(data.rows);
  if (rows.length === 0) {
    return (
      <p className={styles.muted} data-testid="history-empty">
        No alerts opened in {data.week}.
      </p>
    );
  }
  return (
    <div className={tableStyles.scroller}>
      <table className={tableStyles.table} data-testid="history-table">
        <caption className={tableStyles.caption}>
          {data.week}: {data.total_opened} opened, {data.still_open} still open · worst severity {worstSeverity(rows) || 'unset'}
        </caption>
        <thead>
          <tr>
            <th scope="col" className={tableStyles.weekHead}>
              Policy
            </th>
            <th scope="col">Severity</th>
            <th scope="col" className={tableStyles.numHead}>
              Times fired
            </th>
            <th scope="col" className={tableStyles.numHead}>
              Still open
            </th>
            <th scope="col">First</th>
            <th scope="col">Last</th>
            <th scope="col">Example resources</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.policy_name} className={tableStyles.row} data-testid="history-row">
              <th scope="row" className={tableStyles.weekCell}>
                <div className={tableStyles.weekId}>{r.policy_display_name}</div>
                <div className={tableStyles.weekMeta}>{r.project}</div>
              </th>
              <td>
                <SeverityBadge severity={r.severity} />
              </td>
              <td className={tableStyles.num} data-testid="history-count">
                {r.opened}
              </td>
              <td className={tableStyles.num}>{r.still_open}</td>
              <td title={r.first_opened_at}>{formatTimestamp(r.first_opened_at)}</td>
              <td>{formatTimestamp(r.last_opened_at)}</td>
              <td className={styles.resources}>{r.resources.slice(0, 5).join(', ') || '—'}{r.resources.length > 5 ? ` +${r.resources.length - 5}` : ''}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
