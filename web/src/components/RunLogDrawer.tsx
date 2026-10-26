import { useEffect, useState } from 'react';
import { ApiError, getRun, toApiError } from '@/lib/api';
import { formatBytes, formatDurationMs, formatInteger, formatTimeZ, formatTimestamp } from '@/lib/format';
import type { Run, RunListItem, RunSource, RunStatus } from '@/lib/types';
import styles from './RunLogDrawer.module.css';

interface Props {
  runs: RunListItem[] | null;
  error: ApiError | null;
  loading: boolean;
  open: boolean;
  onToggle: () => void;
  /** Run to expand (set by "View run" on a failure banner). */
  highlightRunId: number | null;
}

type Detail = { state: 'loading' } | { state: 'ok'; run: Run } | { state: 'error'; error: ApiError };

export function RunLogDrawer({ runs, error, loading, open, onToggle, highlightRunId }: Props) {
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [details, setDetails] = useState<Record<number, Detail>>({});

  useEffect(() => {
    if (highlightRunId !== null) {
      setExpanded((prev) => new Set(prev).add(highlightRunId));
    }
  }, [highlightRunId]);

  function toggleRun(id: number) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  async function showSql(id: number) {
    setDetails((d) => ({ ...d, [id]: { state: 'loading' } }));
    try {
      const run = await getRun(id);
      setDetails((d) => ({ ...d, [id]: { state: 'ok', run } }));
    } catch (err) {
      setDetails((d) => ({ ...d, [id]: { state: 'error', error: toApiError(err) } }));
    }
  }

  const latest = runs?.[0] ?? null;
  const count = runs?.length ?? 0;

  return (
    <section className={styles.drawer} aria-label="Run log" data-testid="run-log">
      <button type="button" className={styles.toggle} onClick={onToggle} aria-expanded={open} data-testid="run-log-toggle">
        <span className={styles.chevron} aria-hidden="true">
          {open ? '▾' : '▸'}
        </span>
        <span className={styles.toggleTitle}>Runs ({runs === null ? '…' : count})</span>
        <span className={styles.latest} data-testid="run-log-latest">
          {error
            ? `runs unavailable: ${error.code}`
            : latest
              ? `latest #${latest.id} ${latest.status} · ${formatTimeZ(latest.started_at)} · ${formatBytes(bytesBilled(latest))} billed`
              : loading
                ? 'loading…'
                : 'no runs yet'}
        </span>
      </button>

      {open ? (
        <div className={styles.content}>
          {error ? (
            <p className={styles.error} role="alert" data-testid="run-log-error">
              <code>{error.code}</code> — {error.message}
              {error.hint ? <strong> {error.hint}</strong> : null}
            </p>
          ) : runs === null ? (
            <p className={styles.muted}>Loading runs…</p>
          ) : runs.length === 0 ? (
            <p className={styles.muted} data-testid="run-log-empty">
              No runs yet — click Refresh to record the first one.
            </p>
          ) : (
            <table className={styles.table} data-testid="run-log-table">
              <thead>
                <tr>
                  <th scope="col" />
                  <th scope="col">#</th>
                  <th scope="col">trigger</th>
                  <th scope="col">weeks</th>
                  <th scope="col">started</th>
                  <th scope="col" className={styles.num}>
                    duration
                  </th>
                  <th scope="col">status</th>
                  <th scope="col" className={styles.num}>
                    rows
                  </th>
                  <th scope="col" className={styles.num}>
                    bytes billed
                  </th>
                  <th scope="col">snapshots</th>
                </tr>
              </thead>
              <tbody>
                {runs.map((run) => {
                  const isOpen = expanded.has(run.id);
                  return (
                    <RunRows
                      key={run.id}
                      run={run}
                      open={isOpen}
                      highlighted={run.id === highlightRunId}
                      detail={details[run.id]}
                      onToggle={() => toggleRun(run.id)}
                      onShowSql={() => showSql(run.id)}
                    />
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      ) : null}
    </section>
  );
}

function bytesBilled(run: Run): number | null {
  let total: number | null = null;
  for (const s of run.sources) {
    if (s.bytes_billed !== null) total = (total ?? 0) + s.bytes_billed;
  }
  return total;
}

function rowsReturned(run: Run): number | null {
  let total: number | null = null;
  for (const s of run.sources) {
    if (s.rows_returned !== null) total = (total ?? 0) + s.rows_returned;
  }
  return total;
}

interface RunRowsProps {
  run: RunListItem;
  open: boolean;
  highlighted: boolean;
  detail: Detail | undefined;
  onToggle: () => void;
  onShowSql: () => void;
}

function RunRows({ run, open, highlighted, detail, onToggle, onShowSql }: RunRowsProps) {
  const colSpan = 10;
  return (
    <>
      <tr className={`${styles.row} ${highlighted ? styles.highlighted : ''}`} data-testid={`run-row-${run.id}`} data-status={run.status}>
        <td>
          <button type="button" className={styles.expand} onClick={onToggle} aria-expanded={open} aria-label={`toggle sources of run ${run.id}`}>
            {open ? '▾' : '▸'}
          </button>
        </td>
        <td>#{run.id}</td>
        <td>{run.trigger}</td>
        <td>{run.from === run.to ? run.from : `${run.from} → ${run.to}`}</td>
        <td title={formatTimestamp(run.started_at)}>{formatTimestamp(run.started_at)}</td>
        <td className={styles.num}>{formatDurationMs(run.duration_ms)}</td>
        <td>
          <StatusChip status={run.status} />
        </td>
        <td className={styles.num}>{formatInteger(rowsReturned(run))}</td>
        <td className={styles.num} data-testid={`run-bytes-billed-${run.id}`}>
          {formatBytes(bytesBilled(run))}
        </td>
        <td>{run.snapshot_ids.length > 0 ? run.snapshot_ids.map((id) => `#${id}`).join(', ') : '—'}</td>
      </tr>
      {open ? (
        <tr className={styles.detailRow} data-testid={`run-sources-${run.id}`}>
          <td colSpan={colSpan}>
            {run.error ? (
              <p className={styles.error}>
                run error: <code>{run.error}</code>
              </p>
            ) : null}
            {run.sources.length === 0 ? <p className={styles.muted}>No sources recorded.</p> : null}
            {run.sources.map((s) => (
              <SourceDetail key={s.id} source={s} detail={detail} onShowSql={onShowSql} />
            ))}
          </td>
        </tr>
      ) : null}
    </>
  );
}

function SourceDetail({ source: s, detail, onShowSql }: { source: RunSource; detail: Detail | undefined; onShowSql: () => void }) {
  const detailSource = detail?.state === 'ok' ? detail.run.sources.find((x) => x.id === s.id) : undefined;
  return (
    <div className={styles.source}>
      <div className={styles.sourceHead}>
        <strong>{s.source}</strong> <StatusChip status={s.status} /> · {formatDurationMs(s.duration_ms)} · job{' '}
        <code data-testid="run-source-job-id">{s.job_id ?? '—'}</code> · {formatBytes(s.bytes_processed)} processed ·{' '}
        {formatBytes(s.bytes_billed)} billed · cache hit {s.cache_hit === null ? '—' : s.cache_hit ? 'yes' : 'no'} ·{' '}
        {formatInteger(s.rows_returned)} rows
      </div>
      {s.status === 'failed' ? (
        <div className={styles.failure} data-testid="run-source-failure">
          <div>
            <code>{s.error_code ?? 'unknown'}</code> — {s.error_message ?? 'no message'}
          </div>
          {s.fix_hint ? <div className={styles.hint}>{s.fix_hint}</div> : null}
        </div>
      ) : null}
      <div className={styles.sqlRow}>
        {detail === undefined ? (
          <button type="button" className={styles.link} onClick={onShowSql} data-testid="run-show-sql">
            Show SQL
          </button>
        ) : detail.state === 'loading' ? (
          <span className={styles.muted}>Loading SQL…</span>
        ) : detail.state === 'error' ? (
          <span className={styles.error}>
            <code>{detail.error.code}</code> — {detail.error.message}
          </span>
        ) : detailSource?.sql ? (
          <>
            <pre className={styles.sql}>{detailSource.sql}</pre>
            {detailSource.params ? (
              <dl className={styles.params}>
                {Object.entries(detailSource.params).map(([k, v]) => (
                  <div key={k} className={styles.param}>
                    <dt>{k}</dt>
                    <dd>
                      <code>{v}</code>
                    </dd>
                  </div>
                ))}
              </dl>
            ) : null}
          </>
        ) : (
          <span className={styles.muted}>No SQL recorded for this source.</span>
        )}
      </div>
    </div>
  );
}

function StatusChip({ status }: { status: RunStatus }) {
  return (
    <span className={`${styles.chip} ${styles[`chip_${status}`] ?? ''}`} data-testid="run-status">
      {status}
    </span>
  );
}
