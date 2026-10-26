import { useEffect, useState } from 'react';
import { ApiError, getSnapshot, toApiError } from '@/lib/api';
import { formatMoney, formatPct } from '@/lib/format';
import type { Column, CostRow, Scope, SnapshotProvenance } from '@/lib/types';
import { DrilldownTable } from './DrilldownTable';
import { ErrorBanner } from './ErrorBanner';
import { ProvenanceView, type PreviousWeekInfo } from './ProvenanceView';
import { StatusBadge } from './StatusBadge';
import styles from './CellInspector.module.css';

export interface InspectorTarget {
  row: CostRow;
  column: Column;
  /** The previous week's row in the table (for the % change cell). */
  prevRow: CostRow | null;
}

interface Props {
  target: InspectorTarget;
  scope: Scope;
  columns: readonly Column[];
  onClose: () => void;
}

/** Right panel: header + provenance (GET /snapshots/{id}) + drill-down (breakdown). */
export function CellInspector({ target, scope, columns, onClose }: Props) {
  const { row, column } = target;
  const snapshotId = row.snapshot?.id ?? null;
  const [snapshot, setSnapshot] = useState<SnapshotProvenance | null>(null);
  const [error, setError] = useState<ApiError | null>(null);

  useEffect(() => {
    if (snapshotId === null) return;
    let cancelled = false;
    setSnapshot(null);
    setError(null);
    getSnapshot(snapshotId)
      .then((s) => {
        if (!cancelled) setSnapshot(s);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(toApiError(err));
      });
    return () => {
      cancelled = true;
    };
  }, [snapshotId]);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose();
    }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const cell = column.kind === 'service' || column.kind === 'total' ? row.cells?.[column.key] : undefined;
  const valueText = column.kind === 'pct_change' ? formatPct(row.pct_change) : cell ? formatMoney(cell.net, { grouped: true }) : '—';

  const previous: PreviousWeekInfo | null =
    column.kind === 'pct_change' && target.prevRow?.snapshot && target.prevRow.cells?.total
      ? { week: target.prevRow.week, snapshotId: target.prevRow.snapshot.id, total: target.prevRow.cells.total.net }
      : null;

  return (
    <aside className={styles.panel} aria-label="Cell inspector" data-testid="inspector" data-week={row.week} data-column={column.key}>
      <header className={styles.header}>
        <div className={styles.title} data-testid="inspector-title">
          <strong>{row.week}</strong> · {column.label} · <span className={styles.value}>{valueText}</span>{' '}
          <StatusBadge status={row.snapshot ? row.week_status : null} stale={row.snapshot?.stale ?? false} />
        </div>
        <button type="button" className={styles.close} onClick={onClose} aria-label="Close inspector" data-testid="inspector-close">
          ×
        </button>
      </header>

      <div className={styles.body}>
        {snapshotId === null ? (
          <p className={styles.muted}>This week has no snapshot yet — Refresh it to see provenance.</p>
        ) : error ? (
          <ErrorBanner banner={{ key: 'inspector', context: 'Provenance', code: error.code, message: error.message, hint: error.hint, requestId: error.requestId }} />
        ) : snapshot === null ? (
          <div className={styles.skeletonBlock} aria-busy="true" aria-label="loading provenance">
            <span className={styles.skeleton} />
            <span className={styles.skeleton} />
            <span className={`${styles.skeleton} ${styles.skeletonTall}`} />
          </div>
        ) : (
          <ProvenanceView snapshot={snapshot} column={column} scope={scope} previous={previous} />
        )}

        {snapshotId !== null && (column.kind === 'service' || column.kind === 'total') ? (
          <DrilldownTable snapshotId={snapshotId} scope={scope} column={column} cellNet={cell?.net ?? 0} columns={columns} />
        ) : null}
      </div>
    </aside>
  );
}
