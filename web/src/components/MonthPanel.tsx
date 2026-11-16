import { useCurrency } from '@/lib/currency';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { ApiError, getMonth, getRun, refreshMonth, toApiError } from '@/lib/api';
import { downloadTextFile } from '@/lib/csv';
import { formatBytes, formatDurationMs, formatInteger, formatMoney, formatPct, formatSignedMoney, formatThreshold, formatTimestamp } from '@/lib/format';
import { buildMonthCsv, currentMonthId, formatSummaryLine, isMonthId, monthCsvFilename, monthWindow, refreshOutcome } from '@/lib/monthFormat';
import type { Cell, MonthColumn, MonthID, MonthRow, MonthTableResponse, Run, RunSource, Scope, Thresholds } from '@/lib/types';
import { ErrorBanner, bannerFromError, type BannerData } from './ErrorBanner';
import { EmptyState } from './EmptyState';
import { StatusBadge } from './StatusBadge';
import tableStyles from './CostTable.module.css';
import styles from './MonthPanel.module.css';

export interface MonthSelection {
  row: MonthRow;
  column: MonthColumn;
}

// ---- presentational view (rendered by the panel; also used by tests) ---------

export interface MonthViewProps {
  data: MonthTableResponse;
  selection: MonthSelection | null;
  onCellClick: (row: MonthRow, column: MonthColumn) => void;
  onRefreshMonth: (month: MonthID) => void;
  refreshDisabled: boolean;
}

export function MonthView({ data, selection, onCellClick, onRefreshMonth, refreshDisabled }: MonthViewProps) {
  const currency = useCurrency();
  const dataColumns = data.columns.filter((c) => c.kind !== 'month');
  return (
    <>
      <p className={styles.summary} data-testid="month-summary">
        {formatSummaryLine(data.summary, currency)} <StatusBadge status={data.summary.status} />
      </p>
      {!data.summary.has_data ? (
        <EmptyState title={`No data for ${data.summary.month} yet`} testId="month-empty">
          Refresh the month to fetch
        </EmptyState>
      ) : null}
      <div className={tableStyles.scroller}>
        <table className={tableStyles.table} data-testid="month-table">
          <caption className={tableStyles.caption}>
            Cost by service per calendar month (UTC) · {currency}, net of credits · click a number for provenance.
          </caption>
          <thead>
            <tr>
              {data.columns.map((c) => (
                <th
                  key={c.key}
                  scope="col"
                  className={`${c.kind === 'month' ? tableStyles.weekHead : tableStyles.numHead} ${c.kind === 'stub' ? tableStyles.stubHead : ''}`}
                  data-column-key={c.key}
                  title={c.kind === 'service' && c.services ? `aggregates: ${c.services.join(', ')}` : undefined}
                >
                  {c.label}
                  {c.kind === 'stub' ? <span className={tableStyles.stubSuffix}> (stub)</span> : null}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {data.rows.map((row) => (
              <tr
                key={row.month}
                className={`${tableStyles.row} ${row.flagged ? tableStyles.flaggedRow : ''}`}
                data-testid={`month-row-${row.month}`}
                aria-selected={selection?.row.month === row.month}
              >
                <th scope="row" className={tableStyles.weekCell}>
                  <div className={tableStyles.weekId}>
                    {row.flagged ? <span className={tableStyles.flagDot} title="total change exceeds the anomaly thresholds" aria-label="flagged" /> : null}
                    <span>{row.month}</span>
                  </div>
                  <div className={tableStyles.weekMeta}>
                    <StatusBadge status={row.snapshot ? row.month_status : null} stale={row.snapshot?.stale ?? false} />
                  </div>
                </th>
                {row.cells === null ? (
                  <td className={tableStyles.notFetched} colSpan={dataColumns.length} data-testid={`month-not-fetched-${row.month}`}>
                    not fetched —{' '}
                    <button
                      type="button"
                      className={tableStyles.link}
                      disabled={refreshDisabled}
                      onClick={() => onRefreshMonth(row.month)}
                      title={`Query BigQuery for ${row.month} now`}
                    >
                      Refresh
                    </button>
                  </td>
                ) : (
                  dataColumns.map((c) => <MonthDataCell key={c.key} row={row} column={c} thresholds={data.thresholds} onClick={onCellClick} />)
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

function MonthDataCell({ row, column, thresholds, onClick }: { row: MonthRow; column: MonthColumn; thresholds: Thresholds; onClick: (r: MonthRow, c: MonthColumn) => void }) {
  const currency = useCurrency();
  if (column.kind === 'stub') {
    return (
      <td className={`${tableStyles.num} ${tableStyles.stubCell}`} title={`Arrives in Phase ${column.phase ?? '?'}`}>
        —
      </td>
    );
  }
  if (column.kind === 'pct_change') {
    return (
      <td className={tableStyles.num}>
        <button
          type="button"
          className={tableStyles.cellButton}
          data-testid={`month-cell-${row.month}-${column.key}`}
          aria-label={`${row.month} ${column.label} ${formatPct(row.pct_change)}, open provenance`}
          title={row.pct_change === null ? 'previous month not fetched' : 'change of Total GCP vs the previous month'}
          onClick={() => onClick(row, column)}
        >
          {formatPct(row.pct_change)}
        </button>
      </td>
    );
  }
  const cell: Cell | undefined = row.cells?.[column.key];
  if (!cell) {
    return (
      <td className={tableStyles.num} title="no value for this column in the stored snapshot">
        —
      </td>
    );
  }
  return (
    <td className={`${tableStyles.num} ${cell.flagged ? tableStyles.flaggedCell : ''}`}>
      <button
        type="button"
        className={tableStyles.cellButton}
        data-testid={`month-cell-${row.month}-${column.key}`}
        aria-label={`${row.month} ${column.label} ${formatMoney(cell.net, currency)}, open provenance`}
        title={`gross ${formatMoney(cell.gross, currency)} · credits ${formatSignedMoney(cell.credits, currency)}${
          cell.flagged ? ` · change exceeds thresholds (≥ ${formatThreshold(thresholds.pct)} % and ≥ ${formatMoney(thresholds.abs_usd, currency)})` : ''
        }`}
        onClick={() => onClick(row, column)}
      >
        <span>{formatMoney(cell.net, currency)}</span>
      </button>
    </td>
  );
}

// ---- provenance / SQL --------------------------------------------------------

export interface MonthProvenanceProps {
  selection: MonthSelection;
  /** The refresh run that produced the snapshot (GET /runs/{id}); null while loading or unavailable. */
  run: Run | null;
  error: BannerData | null;
  onClose: () => void;
}

function monthSource(run: Run | null): RunSource | null {
  return run?.sources.find((s) => s.source === 'bigquery_month') ?? null;
}

export function MonthProvenance({ selection, run, error, onClose }: MonthProvenanceProps) {
  const currency = useCurrency();
  const { row, column } = selection;
  const snap = row.snapshot;
  const src = monthSource(run);
  const cell = row.cells?.[column.key];
  const value = column.kind === 'pct_change' ? formatPct(row.pct_change) : cell ? formatMoney(cell.net, currency) : '—';
  return (
    <section className={styles.provenance} aria-label="Month provenance" data-testid="month-provenance">
      <header className={styles.provHead}>
        <strong>
          {row.month} · {column.label} · {value}
        </strong>
        <button type="button" className={styles.close} onClick={onClose} aria-label="Close provenance">
          ×
        </button>
      </header>
      {snap === null ? (
        <p className={styles.muted}>This month has no snapshot yet — Refresh it to see provenance.</p>
      ) : (
        <dl className={styles.kv}>
          <dt>Snapshot</dt>
          <dd>#{snap.id}</dd>
          <dt>Fetched</dt>
          <dd>{formatTimestamp(snap.fetched_at)}</dd>
          <dt>Status</dt>
          <dd>
            <StatusBadge status={row.month_status} stale={snap.stale} />
          </dd>
          <dt>Run</dt>
          <dd>#{snap.run_id}</dd>
          <dt>Rows</dt>
          <dd>{formatInteger(snap.row_count)}</dd>
          <dt>Window</dt>
          <dd>
            {row.month_start} … {row.month_end} (UTC days)
          </dd>
          <dt>Invoice months</dt>
          <dd>{row.invoice_months.length > 0 ? row.invoice_months.join(', ') : '—'}</dd>
        </dl>
      )}
      {error ? <ErrorBanner banner={error} /> : null}
      {snap !== null && !error && run === null ? <p className={styles.muted}>Loading query…</p> : null}
      {snap !== null && run !== null && src === null ? <p className={styles.muted}>Run #{run.id} recorded no month-cost query.</p> : null}
      {src ? (
        <>
          <p className={styles.line}>
            BigQuery job <code>{src.job_id ?? '—'}</code> · {formatBytes(src.bytes_processed)} processed · {formatBytes(src.bytes_billed)} billed · cache hit{' '}
            {src.cache_hit === null ? '—' : src.cache_hit ? 'yes' : 'no'} · {formatDurationMs(src.duration_ms)}
          </p>
          <h4 className={styles.sub}>SQL</h4>
          <pre className={styles.sql} data-testid="month-sql">
            {src.sql ?? '(not recorded)'}
          </pre>
          {src.params ? (
            <table className={styles.params}>
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
          ) : null}
        </>
      ) : null}
    </section>
  );
}

// ---- the panel ----------------------------------------------------------------

interface Props {
  scope: Scope;
  /** Bumped by the parent after any refresh; re-fetches the table. */
  refreshToken: number;
}

export default function MonthPanel({ scope, refreshToken }: Props) {
  const currency = useCurrency();
  const [month, setMonth] = useState<MonthID>(() => currentMonthId(new Date()));
  const [data, setData] = useState<MonthTableResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [banner, setBanner] = useState<BannerData | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [selection, setSelection] = useState<MonthSelection | null>(null);
  const [run, setRun] = useState<Run | null>(null);
  const [runError, setRunError] = useState<BannerData | null>(null);
  const [localToken, setLocalToken] = useState(0);

  const window_ = useMemo(() => (isMonthId(month) ? monthWindow(month) : null), [month]);

  useEffect(() => {
    if (!window_) return;
    let cancelled = false;
    setLoading(true);
    getMonth({ from: window_.from, to: window_.to, scope })
      .then((res) => {
        if (cancelled) return;
        setData(res);
        setBanner((b) => (b?.key === 'month-table' ? null : b));
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setBanner(bannerFromError('month-table', 'Month table', toApiError(err)));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [window_, scope, refreshToken, localToken]);

  const runId = selection?.row.snapshot?.run_id ?? null;
  useEffect(() => {
    setRun(null);
    setRunError(null);
    if (runId === null) return;
    let cancelled = false;
    getRun(runId)
      .then((r) => {
        if (!cancelled) setRun(r);
      })
      .catch((err: unknown) => {
        if (!cancelled) setRunError(bannerFromError('month-run', 'Month query', toApiError(err)));
      });
    return () => {
      cancelled = true;
    };
  }, [runId]);

  const doRefresh = useCallback(
    async (target: MonthID) => {
      setRefreshing(true);
      setNotice(`Refreshing ${target}… querying BigQuery, this can take up to two minutes.`);
      setBanner((b) => (b?.key === 'month-refresh' ? null : b));
      try {
        const res = await refreshMonth(target);
        const outcome = refreshOutcome(res);
        if (outcome.failure) {
          setBanner({ key: 'month-refresh', context: `Refresh ${target} (${outcome.failure.source})`, code: outcome.failure.code, message: outcome.failure.message, hint: outcome.failure.hint });
          setNotice(null);
        } else {
          setNotice(`Refreshed ${target} · run #${res.run.id} ${res.run.status}.`);
        }
        setLocalToken((t) => t + 1);
      } catch (err: unknown) {
        setBanner(bannerFromError('month-refresh', `Refresh ${target}`, toApiError(err as ApiError)));
        setNotice(null);
      } finally {
        setRefreshing(false);
      }
    },
    [],
  );

  function exportCsv() {
    if (!data) return;
    downloadTextFile(monthCsvFilename(data.scope, data.from, data.to, currency), buildMonthCsv(data.columns, data.rows));
  }

  return (
    <div className={styles.panel} data-testid="month-panel">
      <div className={styles.toolbar}>
        <label className={styles.picker}>
          <span>Month</span>
          <input
            type="month"
            data-testid="month-picker"
            value={month}
            max={currentMonthId(new Date())}
            onChange={(e) => {
              setMonth(e.target.value);
              setSelection(null);
            }}
          />
        </label>
        <button
          type="button"
          className={styles.primary}
          data-testid="month-refresh"
          disabled={refreshing || !isMonthId(month)}
          aria-busy={refreshing || undefined}
          onClick={() => void doRefresh(month)}
          title="Query BigQuery for this calendar month and store a month snapshot"
        >
          {refreshing ? `Refreshing ${month}…` : 'Refresh month'}
        </button>
        <button type="button" className={styles.secondary} data-testid="month-export" disabled={!data} onClick={exportCsv}>
          Export CSV
        </button>
        {notice ? (
          <span className={styles.notice} role="status" data-testid="month-notice">
            {notice}
          </span>
        ) : null}
      </div>
      {!isMonthId(month) ? <p className={styles.muted}>Pick a month as YYYY-MM.</p> : null}
      {banner ? <ErrorBanner banner={banner} onDismiss={() => setBanner(null)} /> : null}
      {data === null ? (
        loading ? (
          <div className={styles.skeleton} aria-busy="true" aria-label="loading month table" />
        ) : (
          <EmptyState title="Month table unavailable" testId="month-unavailable">
            The month table could not be loaded (see the banner above).
          </EmptyState>
        )
      ) : (
        <MonthView
          data={data}
          selection={selection}
          onCellClick={(row, column) => setSelection({ row, column })}
          onRefreshMonth={(m) => void doRefresh(m)}
          refreshDisabled={refreshing}
        />
      )}
      {selection ? <MonthProvenance selection={selection} run={run} error={runError} onClose={() => setSelection(null)} /> : null}
    </div>
  );
}
