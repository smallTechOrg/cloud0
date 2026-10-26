import { useCurrency } from '@/lib/currency';
import { useEffect, useRef } from 'react';
import { formatDelta, formatMoney, formatPct, formatSignedMoney, formatThreshold } from '@/lib/format';
import type { Cell, Column, CostRow, Thresholds, WeekID } from '@/lib/types';
import { formatDateRange } from '@/lib/week';
import { StatusBadge } from './StatusBadge';
import styles from './CostTable.module.css';

interface Props {
  columns: readonly Column[];
  rows: CostRow[];
  selectedWeek: WeekID | null;
  /** Thresholds the flags were computed with (for the tooltip). */
  thresholds: Thresholds | null;
  /** True while the first table response is pending → skeleton rows. */
  loading: boolean;
  /** True when rows are client-generated because GET /cost/table failed. */
  fallback: boolean;
  refreshDisabled: boolean;
  onCellClick: (row: CostRow, column: Column) => void;
  onRefreshWeek: (week: WeekID) => void;
}

const SKELETON_ROWS = 6;
const NOTE_PREVIEW_CHARS = 120;

export function CostTable(p: Props) {
  const currency = useCurrency();
  const rowRefs = useRef(new Map<WeekID, HTMLTableRowElement>());

  useEffect(() => {
    if (!p.selectedWeek) return;
    const el = rowRefs.current.get(p.selectedWeek);
    if (el && typeof el.scrollIntoView === 'function') {
      el.scrollIntoView({ block: 'nearest' });
    }
  }, [p.selectedWeek, p.rows.length]);

  const dataColumns = p.columns.filter((c) => c.kind !== 'week');

  return (
    <div className={styles.scroller}>
      <table className={styles.table} data-testid="cost-table" data-fallback={p.fallback || undefined}>
        <caption className={styles.caption}>
          {p.fallback
            ? 'Cost table unavailable from the API — showing every week as not fetched (see the banner above).'
            : `Cost by service per ISO week · ${currency}, net of credits · click a number for provenance and drill-down.`}
        </caption>
        <thead>
          <tr>
            {p.columns.map((c) => (
              <HeaderCell key={c.key} column={c} />
            ))}
          </tr>
        </thead>
        <tbody>
          {p.loading && p.rows.length === 0
            ? Array.from({ length: SKELETON_ROWS }, (_, i) => (
                <tr key={`skeleton-${i}`} className={styles.skeletonRow} aria-hidden="true">
                  <td className={styles.weekCell}>
                    <span className={styles.skeleton} />
                  </td>
                  <td colSpan={dataColumns.length}>
                    <span className={styles.skeleton} />
                  </td>
                </tr>
              ))
            : p.rows.map((row) => (
                <tr
                  key={row.week}
                  ref={(el) => {
                    if (el) rowRefs.current.set(row.week, el);
                    else rowRefs.current.delete(row.week);
                  }}
                  className={`${styles.row} ${row.week === p.selectedWeek ? styles.selected : ''} ${row.flagged ? styles.flaggedRow : ''}`}
                  data-testid={`cost-row-${row.week}`}
                  data-selected={row.week === p.selectedWeek ? 'true' : 'false'}
                  aria-selected={row.week === p.selectedWeek}
                >
                  <WeekCell row={row} />
                  {row.cells === null ? (
                    <td className={styles.notFetched} colSpan={dataColumns.length} data-testid={`not-fetched-${row.week}`}>
                      not fetched —{' '}
                      <button
                        type="button"
                        className={styles.link}
                        data-testid={`not-fetched-refresh-${row.week}`}
                        disabled={p.refreshDisabled}
                        onClick={() => p.onRefreshWeek(row.week)}
                        title={`Query BigQuery for ${row.week} now`}
                      >
                        Refresh
                      </button>
                    </td>
                  ) : (
                    dataColumns.map((c) => (
                      <DataCell key={c.key} row={row} column={c} thresholds={p.thresholds} onClick={p.onCellClick} />
                    ))
                  )}
                </tr>
              ))}
        </tbody>
      </table>
    </div>
  );
}

function HeaderCell({ column }: { column: Column }) {
  const isStub = column.kind === 'stub';
  let title: string | undefined;
  if (column.kind === 'service' && column.services) title = `aggregates: ${column.services.join(', ')}`;
  if (column.extra) title = `${title ?? ''}${title ? ' · ' : ''}service not in the reference sheet; appended automatically`;
  if (isStub) title = `Arrives in Phase ${column.phase ?? '?'}`;
  return (
    <th
      scope="col"
      className={`${column.kind === 'week' ? styles.weekHead : styles.numHead} ${isStub ? styles.stubHead : ''}`}
      data-column-key={column.key}
      data-kind={column.kind}
      title={title}
    >
      <span data-testid="cost-header-label">{column.label}</span>
      {isStub ? <span className={styles.stubSuffix}> (stub)</span> : null}
      {column.extra ? (
        <span className={styles.extraMarker} aria-label="extra service column">
          +
        </span>
      ) : null}
    </th>
  );
}

function WeekCell({ row }: { row: CostRow }) {
  const note = row.annotation;
  return (
    <th scope="row" className={styles.weekCell}>
      <div className={styles.weekId}>
        {row.flagged ? <span className={styles.flagDot} title="total change exceeds the anomaly thresholds" aria-label="flagged" /> : null}
        <span data-testid="cost-week-id">{row.week}</span>
        {note ? (
          <span
            className={styles.noteIcon}
            title={note.length > NOTE_PREVIEW_CHARS ? `${note.slice(0, NOTE_PREVIEW_CHARS)}…` : note}
            aria-label="has a note"
            data-testid={`note-icon-${row.week}`}
          >
            ✎
          </span>
        ) : null}
      </div>
      <div className={styles.weekMeta}>
        <span>{formatDateRange(row.week_start, row.week_end)}</span>{' '}
        <StatusBadge status={row.snapshot ? row.week_status : null} stale={row.snapshot?.stale ?? false} />
      </div>
    </th>
  );
}

interface DataCellProps {
  row: CostRow;
  column: Column;
  thresholds: Thresholds | null;
  onClick: (row: CostRow, column: Column) => void;
}

function DataCell({ row, column, thresholds, onClick }: DataCellProps) {
  const currency = useCurrency();
  switch (column.kind) {
    case 'stub':
      return (
        <td className={`${styles.num} ${styles.stubCell}`} title={`Arrives in Phase ${column.phase ?? '?'}`}>
          —
        </td>
      );
    case 'pct_change':
      return (
        <td className={styles.num}>
          <button
            type="button"
            className={styles.cellButton}
            data-testid={`cell-${row.week}-${column.key}`}
            aria-label={`${row.week} ${column.label} ${formatPct(row.pct_change)}, open inspector`}
            title={row.pct_change === null ? 'previous week not fetched' : 'change of Total GCP vs the previous week'}
            onClick={() => onClick(row, column)}
          >
            {formatPct(row.pct_change)}
          </button>
        </td>
      );
    case 'service':
    case 'total': {
      const cell: Cell | undefined = row.cells?.[column.key];
      if (!cell) {
        return (
          <td className={styles.num} title="no value for this column in the stored snapshot">
            —
          </td>
        );
      }
      const flagTitle =
        cell.flagged && thresholds
          ? `change vs the previous week exceeds thresholds (≥ ${formatThreshold(thresholds.pct)} % and ≥ ${formatMoney(thresholds.abs_usd, currency)})`
          : undefined;
      return (
        <td className={`${styles.num} ${cell.flagged ? styles.flaggedCell : ''}`}>
          <button
            type="button"
            className={styles.cellButton}
            data-testid={`cell-${row.week}-${column.key}`}
            data-net={cell.net}
            aria-label={`${row.week} ${column.label} ${formatMoney(cell.net, currency)}, open inspector`}
            title={`gross ${formatMoney(cell.gross, currency)} · credits ${formatSignedMoney(cell.credits, currency)}`}
            onClick={() => onClick(row, column)}
          >
            <span className={styles.value}>{formatMoney(cell.net, currency)}</span>
            {cell.flagged && cell.delta_abs !== null ? (
              <span className={styles.flag} title={flagTitle} data-testid="anomaly-flag">
                {formatDelta(cell.delta_abs, cell.delta_pct, currency)}
              </span>
            ) : null}
          </button>
        </td>
      );
    }
    case 'week':
      return null;
  }
}
