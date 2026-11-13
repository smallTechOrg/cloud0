import { useState } from 'react';
import { HISTORY_KINDS, historyKindLabel, prettyJson } from '@/lib/budgetsFormat';
import { formatTimestamp } from '@/lib/format';
import type { ChangeRecord } from '@/lib/types';
import styles from './BudgetsPanel.module.css';

export interface ChangeHistoryTableProps {
  entries: ChangeRecord[];
  kind: string;
  onKind: (kind: string) => void;
  onExport: () => void;
  /** Initially expanded entry ids (tests render the expanded markup statically). */
  initialExpanded?: readonly number[];
}

/** Append-only audit log: who/when/what with expandable before/after JSON. */
export function ChangeHistoryTable({ entries, kind, onKind, onExport, initialExpanded = [] }: ChangeHistoryTableProps) {
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set(initialExpanded));
  function toggle(id: number) {
    setExpanded((cur) => {
      const next = new Set(cur);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }
  return (
    <div className={styles.section} data-testid="change-history">
      <div className={styles.sectionHead}>
        <h3>Change history</h3>
        <label className={styles.option}>
          Kind
          <select className={styles.select} data-testid="history-kind" value={kind} onChange={(e) => onKind(e.target.value)}>
            {HISTORY_KINDS.map((k) => (
              <option key={k} value={k}>
                {historyKindLabel(k)}
              </option>
            ))}
          </select>
        </label>
        <span className={styles.spacer} />
        <button type="button" className={styles.btn} data-testid="history-export" disabled={entries.length === 0} onClick={onExport}>
          Export CSV
        </button>
      </div>
      {entries.length === 0 ? (
        <p className={styles.hint} data-testid="history-empty">
          No changes recorded{kind ? ` for ${historyKindLabel(kind).toLowerCase()}` : ''} yet. Every applied create, update, delete, enable, disable or snooze is logged here.
        </p>
      ) : (
        <div className={styles.scroller}>
          <table className={styles.table} data-testid="history-table">
            <thead>
              <tr>
                <th scope="col">When</th>
                <th scope="col">Who</th>
                <th scope="col">Kind</th>
                <th scope="col">Action</th>
                <th scope="col">Target</th>
                <th scope="col">Before / after</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => {
                const open = expanded.has(e.id);
                return (
                  <tr key={e.id} data-testid="history-row" data-kind={e.kind}>
                    <td>{formatTimestamp(e.at)}</td>
                    <td>{e.actor}</td>
                    <td>{e.kind}</td>
                    <td>{e.action}</td>
                    <td style={{ wordBreak: 'break-all' }}>{e.target}</td>
                    <td>
                      <button type="button" className={styles.link} aria-expanded={open} data-testid="history-toggle" onClick={() => toggle(e.id)}>
                        {open ? 'Hide' : 'Show'} JSON
                      </button>
                      {open ? (
                        <div className={styles.jsonGrid} data-testid="history-json">
                          <div>
                            <div className={styles.label}>Before</div>
                            <pre className={styles.json}>{prettyJson(e.before)}</pre>
                          </div>
                          <div>
                            <div className={styles.label}>After</div>
                            <pre className={styles.json}>{prettyJson(e.after)}</pre>
                          </div>
                        </div>
                      ) : null}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
