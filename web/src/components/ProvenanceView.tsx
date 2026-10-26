import { useCurrency } from '@/lib/currency';
import { useState } from 'react';
import { formatBytes, formatDurationMs, formatInteger, formatMoney, formatSignedMoney, formatTimestamp } from '@/lib/format';
import { SCOPE_ALL, type Column, type Scope, type SnapshotProvenance, type WeekID } from '@/lib/types';
import { StatusBadge } from './StatusBadge';
import styles from './ProvenanceView.module.css';

export interface PreviousWeekInfo {
  week: WeekID;
  snapshotId: number;
  total: number;
}

interface Props {
  snapshot: SnapshotProvenance;
  column: Column;
  scope: Scope;
  /** For the % change cell: the previous week's snapshot id and total. */
  previous?: PreviousWeekInfo | null;
}

/** SQL with params appended as a comment block, pasteable into the BigQuery console. */
export function sqlWithParams(sql: string, params: Record<string, string>): string {
  const lines = Object.entries(params).map(([k, v]) => `-- @${k} = ${v}`);
  return `${sql.trimEnd()}\n\n-- parameters\n${lines.join('\n')}\n`;
}

export function ProvenanceView({ snapshot, column, scope, previous = null }: Props) {
  const currency = useCurrency();
  const [copied, setCopied] = useState<'idle' | 'copied' | 'failed'>('idle');
  const src = snapshot.source;

  async function copy() {
    try {
      await navigator.clipboard.writeText(sqlWithParams(src.sql, src.params));
      setCopied('copied');
    } catch {
      setCopied('failed');
    }
    setTimeout(() => setCopied('idle'), 2000);
  }

  const columnLine =
    column.kind === 'service'
      ? `Column = services: ${(column.services ?? []).join(', ')}`
      : column.kind === 'total'
        ? 'Column = Total GCP: sum of every service column'
        : column.kind === 'pct_change'
          ? 'Column = % change: Total GCP vs the previous week'
          : `Column = ${column.label}`;

  return (
    <section className={styles.section} aria-labelledby="provenance-heading" data-testid="provenance">
      <h3 id="provenance-heading" className={styles.heading}>
        Provenance
      </h3>
      <dl className={styles.kv}>
        <dt>Snapshot</dt>
        <dd data-testid="inspector-snapshot-id">#{snapshot.id}</dd>
        <dt>Fetched</dt>
        <dd>{formatTimestamp(snapshot.fetched_at)}</dd>
        <dt>Status at fetch</dt>
        <dd>
          <StatusBadge status={snapshot.status_at_fetch} />
        </dd>
        <dt>Week status now</dt>
        <dd data-testid="inspector-status">
          <StatusBadge status={snapshot.week_status} stale={snapshot.stale} />
        </dd>
        <dt>Run</dt>
        <dd>#{snapshot.run_id}</dd>
        <dt>Rows</dt>
        <dd>{formatInteger(snapshot.row_count)}</dd>
        <dt>Totals</dt>
        <dd>
          net {formatMoney(snapshot.net_total, currency)} · gross {formatMoney(snapshot.gross_total, currency)} · credits{' '}
          {formatSignedMoney(snapshot.credits_total, currency)}
        </dd>
        <dt>Projects</dt>
        <dd>{snapshot.projects.length > 0 ? snapshot.projects.join(', ') : 'none in snapshot'}</dd>
      </dl>
      <p className={styles.line} data-testid="inspector-column-line">
        {columnLine}
      </p>
      <p className={styles.line} data-testid="inspector-scope-line">
        {scope === SCOPE_ALL ? 'Scope filter: all projects' : `Scope filter: project.id = ${scope} (applied to stored rows)`}
      </p>
      {previous ? (
        <p className={styles.line} data-testid="inspector-previous">
          Previous week {previous.week}: snapshot #{previous.snapshotId} · Total GCP {formatMoney(previous.total, currency)}
        </p>
      ) : column.kind === 'pct_change' ? (
        <p className={styles.line} data-testid="inspector-previous">
          Previous week not fetched — no % change.
        </p>
      ) : null}
      <p className={styles.formula}>{snapshot.formula}</p>
      <p className={styles.line}>
        BigQuery job <code>{src.job_id ?? '—'}</code> · {formatBytes(src.bytes_processed)} processed ·{' '}
        <span data-testid="inspector-bytes-billed">{formatBytes(src.bytes_billed)} billed</span> · cache hit{' '}
        {src.cache_hit === null ? '—' : src.cache_hit ? 'yes' : 'no'} · {formatDurationMs(src.duration_ms)}
      </p>
      <div className={styles.sqlHead}>
        <h4 className={styles.subheading}>SQL</h4>
        <button type="button" className={styles.copy} onClick={copy} data-testid="inspector-copy-sql">
          {copied === 'copied' ? 'Copied' : copied === 'failed' ? 'Copy failed' : 'Copy'}
        </button>
      </div>
      <pre className={styles.sql} data-testid="inspector-sql">
        {src.sql}
      </pre>
      <h4 className={styles.subheading}>Params</h4>
      <table className={styles.params} data-testid="inspector-params">
        <tbody>
          {Object.entries(src.params).map(([k, v]) => (
            <tr key={k}>
              <th scope="row">{k}</th>
              <td>
                <code>{v}</code>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
