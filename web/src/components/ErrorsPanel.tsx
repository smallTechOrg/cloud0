'use client';

import { Fragment, useEffect, useMemo, useState } from 'react';
import { ApiError, getErrorSamples, getErrorsTable, toApiError } from '@/lib/api';
import { downloadTextFile } from '@/lib/csv';
import { ERROR_SOURCE_LABELS, buildErrorsCsv, errorsCsvFilename, formatCount } from '@/lib/metricsFormat';
import { errorCodeChips, formatShare, sampleIsEmpty } from '@/lib/errorsFormat';
import type { ErrorSamplesResponse, ErrorTableRow, ErrorTotals, ErrorsTableResponse } from '@/lib/types';
import { EmptyState } from './EmptyState';
import { ErrorBanner, bannerFromError } from './ErrorBanner';
import { MetricProvenance } from './MetricProvenance';
import { StatusBadge } from './StatusBadge';
import styles from './ErrorsPanel.module.css';

export interface ErrorsPanelProps {
  week: string;
  scope: string;
  refreshToken: number;
}

/** On-demand log sample of one Cloud Run row. */
export type SampleState = { status: 'loading' } | { status: 'error'; error: ApiError } | { status: 'done'; data: ErrorSamplesResponse };

interface ViewProps {
  data: ErrorsTableResponse;
  /** Index into `data.rows` of the row whose provenance is open. */
  selected: number | null;
  onSelect: (index: number | null) => void;
  /** Row indexes whose detail (exact codes + sampling) is expanded. */
  expanded?: ReadonlySet<number>;
  onToggleExpand?: (index: number) => void;
  /** Sample state per row index. */
  samples?: Readonly<Record<number, SampleState>>;
  onSample?: (index: number) => void;
}

/** The answer to "how are we getting these errors?", shown at the top of the tab. */
export function HowCounted() {
  return (
    <details className={styles.how} open data-testid="errors-how-counted">
      <summary className={styles.howSummary}>How these are counted</summary>
      <p className={styles.howText}>
        Cloud Run counts come from the Cloud Monitoring metric <code>run.googleapis.com/request_count</code>, grouped by response code. They count{' '}
        <strong>every</strong> 4xx/5xx HTTP response a service sent, <strong>including internet scanners</strong> probing paths such as <code>/.git/config</code>, <code>/.env</code>,{' '}
        <code>/config.php</code> and <code>/wp-*</code> — so a high 4xx number is often bot noise, not broken users. 5xx are the real application-failure signal. Error Reporting counts are a separate source: grouped
        application exceptions. Use <em>Explain &amp; sample</em> on a row to see which paths are behind its numbers.
      </p>
    </details>
  );
}

function SampleResult({ s }: { s: ErrorSamplesResponse }) {
  const share = formatShare(s.scanner_share_4xx);
  if (s.out_of_retention) {
    return (
      <p className={styles.sampleNote} data-testid="errors-sample-retention">
        Out of retention: {s.retention_note}
      </p>
    );
  }
  if (sampleIsEmpty(s)) {
    return (
      <p className={styles.sampleNote} data-testid="errors-sample-empty">
        No matching log entries were found for this service in this week. {s.retention_note}
      </p>
    );
  }
  return (
    <div data-testid="errors-sample-result">
      <p className={styles.sampleNote}>
        Scanned {formatCount(s.scanned)} log entries. {s.note}
      </p>
      {share !== null ? (
        <p className={styles.shareLine} data-testid="errors-sample-share">
          <strong>{share}</strong> of the sampled 4xx responses look like scanner traffic.
        </p>
      ) : null}
      <table className={styles.sampleTable} data-testid="errors-sample-paths">
        <caption className={styles.caption}>Top paths</caption>
        <thead>
          <tr>
            <th scope="col">Path</th>
            <th scope="col">Status</th>
            <th scope="col" className={styles.numHead}>Count</th>
            <th scope="col">Kind</th>
          </tr>
        </thead>
        <tbody>
          {s.top_paths.map((p, i) => (
            <tr key={`${p.path}-${p.status}-${i}`} data-testid="errors-sample-path-row">
              <td className={styles.mono}>{p.path}</td>
              <td>{p.status}</td>
              <td className={styles.count}>{formatCount(p.count)}</td>
              <td>{p.scanner_like ? <span className={styles.scannerTag} title={s.heuristic_note}>looks like scanner traffic</span> : null}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className={styles.heuristic}>{s.heuristic_note}</p>
      <table className={styles.sampleTable} data-testid="errors-sample-entries">
        <caption className={styles.caption}>Recent entries (newest first, client IPs truncated)</caption>
        <thead>
          <tr>
            <th scope="col">Time (UTC)</th>
            <th scope="col">Status</th>
            <th scope="col">Request</th>
            <th scope="col">Client</th>
            <th scope="col">User agent</th>
            <th scope="col" className={styles.numHead}>Latency</th>
          </tr>
        </thead>
        <tbody>
          {s.entries.map((e, i) => (
            <tr key={i} data-testid="errors-sample-entry-row">
              <td className={styles.nowrap}>{e.timestamp.replace('T', ' ').replace(/(\.\d+)?Z$/, '')}</td>
              <td>{e.status}</td>
              <td className={styles.mono}>
                {e.method} {e.path}
                {e.scanner_like ? <span className={styles.scannerTag}>scanner-like</span> : null}
              </td>
              <td className={styles.mono}>{e.remote_ip === '' ? '-' : e.remote_ip}</td>
              <td className={styles.ua} title={e.user_agent}>{e.user_agent === '' ? '-' : e.user_agent}</td>
              <td className={styles.count}>{e.latency_ms.toLocaleString('en-US', { maximumFractionDigits: 0 })} ms</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function RowDetail({ row, sample, onSample }: { row: ErrorTableRow; sample?: SampleState; onSample?: () => void }) {
  const chips = errorCodeChips(row.by_code);
  return (
    <div className={styles.detail} data-testid="errors-detail">
      <div className={styles.chips} data-testid="errors-codes">
        {row.by_code === null ? (
          <span className={styles.muted}>Exact status codes are not stored for this batch — refresh the week to collect them.</span>
        ) : chips.length === 0 ? (
          <span className={styles.muted}>No 4xx/5xx responses this week.</span>
        ) : (
          chips.map((c) => (
            <span key={c.code} className={`${styles.chip} ${c.kind === '5xx' ? styles.chip5 : styles.chip4}`} data-testid="errors-code-chip" data-kind={c.kind}>
              {c.code} ×{formatCount(c.count)}
            </span>
          ))
        )}
      </div>
      <div className={styles.actions}>
        <button type="button" className={styles.export} onClick={onSample} disabled={sample?.status === 'loading'} data-testid="errors-sample-button">
          {sample?.status === 'loading' ? 'Sampling…' : 'Explain & sample'}
        </button>
        {row.logs_url ? (
          <a href={row.logs_url} target="_blank" rel="noopener noreferrer" className={styles.logsLink} data-testid="errors-logs-link">
            Open in Logs Explorer
          </a>
        ) : null}
      </div>
      {sample?.status === 'loading' ? (
        <div className={styles.loading} aria-busy="true" aria-label="loading log sample" data-testid="errors-sample-loading">
          <span className={styles.skeleton} />
          <span className={styles.skeleton} />
        </div>
      ) : sample?.status === 'error' ? (
        <div className={styles.sampleError} role="alert" data-testid="errors-sample-error">
          <strong>{sample.error.message}</strong>
          {sample.error.hint ? <span> — {sample.error.hint}</span> : null}
        </div>
      ) : sample?.status === 'done' ? (
        <SampleResult s={sample.data} />
      ) : null}
    </div>
  );
}

function rowTitle(r: ErrorTableRow): string {
  const who = r.service === '' ? r.project : `${r.service} · ${r.project}`;
  return `${ERROR_SOURCE_LABELS[r.source] ?? r.source} · ${who}`;
}

function countClass(kind: '4xx' | '5xx' | null, n: number | null): string {
  if (n === null || n <= 0 || kind === null) return styles.count ?? '';
  return `${styles.count} ${kind === '5xx' ? styles.emph5 : styles.emph4}`;
}

/** Pure render of the Errors tab (used by the panel and by render tests). */
export function ErrorsView({ data, selected, onSelect, expanded, onToggleExpand, samples, onSample }: ViewProps) {
  const indexed = data.rows.map((row, index) => ({ row, index }));
  const cloudrun = indexed.filter((x) => x.row.source === 'cloudrun');
  const reporting = indexed.filter((x) => x.row.source === 'error_reporting');
  const gapCards = indexed.filter((x) => x.row.source === 'load_balancer' || x.row.source === 'nginx');
  const t: ErrorTotals = data.totals;
  const sel = selected !== null ? data.rows[selected] : undefined;
  const toggle = (i: number) => onSelect(selected === i ? null : i);

  return (
    <div data-testid="errors-block">
      <HowCounted />
      <section className={styles.section} data-testid="errors-cloudrun">
        <h3 className={styles.heading}>Cloud Run — responses by status class</h3>
        <div className={styles.scroller}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th className={styles.svcHead} scope="col">Service</th>
                <th className={styles.numHead} scope="col">2xx</th>
                <th className={styles.numHead} scope="col">3xx</th>
                <th className={styles.numHead} scope="col">4xx</th>
                <th className={styles.numHead} scope="col">5xx</th>
              </tr>
            </thead>
            <tbody>
              {cloudrun.length === 0 ? (
                <tr>
                  <td colSpan={5} className={styles.emptyRow}>no Cloud Run rows for this scope</td>
                </tr>
              ) : (
                cloudrun.map(({ row: r, index }) => (
                  <Fragment key={index}>
                  <tr data-testid="errors-cloudrun-row">
                    <th scope="row" className={styles.svcCell}>
                      {r.service}
                      <span className={styles.project}>{r.project}</span>
                      {r.gap === null && r.service !== '' && onToggleExpand ? (
                        <button
                          type="button"
                          className={styles.expandLink}
                          aria-expanded={expanded?.has(index) ?? false}
                          onClick={() => onToggleExpand(index)}
                          data-testid="errors-expand"
                        >
                          {expanded?.has(index) ? 'Hide details' : 'Details'}
                        </button>
                      ) : null}
                    </th>
                    {r.gap !== null ? (
                      <td colSpan={4} className={styles.gapCell}>
                        <button type="button" className={`${styles.cellButton} ${styles.gapText}`} title={r.hint ?? undefined} onClick={() => toggle(index)}>
                          {r.gap}
                        </button>
                        {selected === index && r.hint ? <span className={styles.inlineHint}>{r.hint}</span> : null}
                      </td>
                    ) : (
                      (
                        [
                          ['2xx', r.c2xx, null],
                          ['3xx', r.c3xx, null],
                          ['4xx', r.c4xx, '4xx'],
                          ['5xx', r.c5xx, '5xx'],
                        ] as const
                      ).map(([label, n, kind]) => (
                        <td key={label} className={countClass(kind, n)} data-emphasis={kind !== null && n !== null && n > 0 ? kind : undefined}>
                          <button type="button" className={styles.cellButton} aria-label={`${r.service} ${label} ${formatCount(n)}, open provenance`} onClick={() => toggle(index)}>
                            {formatCount(n)}
                          </button>
                        </td>
                      ))
                    )}
                  </tr>
                  {expanded?.has(index) && r.gap === null ? (
                    <tr data-testid="errors-detail-row">
                      <td colSpan={5} className={styles.detailCell}>
                        <RowDetail row={r} sample={samples?.[index]} onSample={onSample ? () => onSample(index) : undefined} />
                      </td>
                    </tr>
                  ) : null}
                  </Fragment>
                ))
              )}
            </tbody>
            <tfoot>
              <tr className={styles.totalRow} data-testid="errors-cloudrun-total">
                <th scope="row" className={styles.svcCell}>Total</th>
                <td className={styles.count}>{formatCount(t.c2xx)}</td>
                <td className={styles.count}>{formatCount(t.c3xx)}</td>
                <td className={countClass('4xx', t.c4xx)}>{formatCount(t.c4xx)}</td>
                <td className={countClass('5xx', t.c5xx)}>{formatCount(t.c5xx)}</td>
              </tr>
            </tfoot>
          </table>
        </div>
      </section>

      <section className={styles.section} data-testid="errors-reporting">
        <h3 className={styles.heading}>Error Reporting — error groups</h3>
        <div className={styles.scroller}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th className={styles.svcHead} scope="col">Service</th>
                <th className={styles.numHead} scope="col">Groups</th>
                <th className={styles.numHead} scope="col">Occurrences</th>
              </tr>
            </thead>
            <tbody>
              {reporting.length === 0 ? (
                <tr>
                  <td colSpan={3} className={styles.emptyRow}>no Error Reporting rows for this scope</td>
                </tr>
              ) : (
                reporting.map(({ row: r, index }) => (
                  <tr key={index} data-testid="errors-reporting-row">
                    <th scope="row" className={styles.svcCell}>
                      {r.service}
                      <span className={styles.project}>{r.project}</span>
                    </th>
                    {r.gap !== null ? (
                      <td colSpan={2} className={styles.gapCell}>
                        <button type="button" className={`${styles.cellButton} ${styles.gapText}`} title={r.hint ?? undefined} onClick={() => toggle(index)}>
                          {r.gap}
                        </button>
                        {selected === index && r.hint ? <span className={styles.inlineHint}>{r.hint}</span> : null}
                      </td>
                    ) : (
                      <>
                        <td className={styles.count}>
                          <button type="button" className={styles.cellButton} aria-label={`${r.service} groups ${formatCount(r.groups)}, open provenance`} onClick={() => toggle(index)}>
                            {formatCount(r.groups)}
                          </button>
                        </td>
                        <td className={styles.count}>
                          <button type="button" className={styles.cellButton} aria-label={`${r.service} occurrences ${formatCount(r.occurrences)}, open provenance`} onClick={() => toggle(index)}>
                            {formatCount(r.occurrences)}
                          </button>
                        </td>
                      </>
                    )}
                  </tr>
                ))
              )}
            </tbody>
            <tfoot>
              <tr className={styles.totalRow} data-testid="errors-reporting-total">
                <th scope="row" className={styles.svcCell}>Total</th>
                <td className={styles.count}>{formatCount(t.groups)}</td>
                <td className={styles.count}>{formatCount(t.occurrences)}</td>
              </tr>
            </tfoot>
          </table>
        </div>
      </section>

      <section className={styles.section} data-testid="errors-gaps">
        <h3 className={styles.heading}>Other error sources</h3>
        {gapCards.length === 0 ? (
          <p className={styles.muted}>Load balancer and nginx sources are not in this batch.</p>
        ) : (
          <div className={styles.cards}>
            {gapCards.map(({ row: r, index }) => (
              <article key={index} className={styles.card} data-testid="errors-gap-card" data-source={r.source}>
                <h4 className={styles.cardTitle}>
                  {ERROR_SOURCE_LABELS[r.source]}
                  {r.service !== '' ? <span className={styles.project}> {r.service}</span> : null}
                </h4>
                {r.gap !== null ? (
                  <>
                    <p className={styles.cardGap} data-testid="errors-gap-reason">
                      <span className={styles.gapBadge}>gap</span> <em>{r.gap}</em>
                    </p>
                    <p className={styles.cardHint} data-testid="errors-gap-hint">
                      <strong>Fix:</strong> {r.hint ?? 'no hint available'}
                    </p>
                  </>
                ) : (
                  <p className={styles.cardCounts} data-testid="errors-gap-counts">
                    2xx {formatCount(r.c2xx)} · 3xx {formatCount(r.c3xx)} · <span className={countClass('4xx', r.c4xx)}>4xx {formatCount(r.c4xx)}</span> ·{' '}
                    <span className={countClass('5xx', r.c5xx)}>5xx {formatCount(r.c5xx)}</span>
                  </p>
                )}
                <button type="button" className={styles.link} onClick={() => toggle(index)}>
                  {selected === index ? 'Hide provenance' : 'Provenance'}
                </button>
              </article>
            ))}
          </div>
        )}
      </section>

      {sel ? <MetricProvenance title={rowTitle(sel)} provenance={sel.provenance} gap={sel.gap !== null ? { text: sel.gap, hint: sel.hint } : null} onClose={() => onSelect(null)} /> : null}
    </div>
  );
}

export default function ErrorsPanel({ week, scope, refreshToken }: ErrorsPanelProps) {
  const [data, setData] = useState<ErrorsTableResponse | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState<number | null>(null);
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set());
  const [samples, setSamples] = useState<Record<number, SampleState>>({});

  const toggleExpand = (i: number) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(i)) next.delete(i);
      else next.add(i);
      return next;
    });

  const runSample = (i: number) => {
    const row = data?.rows[i];
    if (!row) return;
    setSamples((prev) => ({ ...prev, [i]: { status: 'loading' } }));
    getErrorSamples({ week, service: row.service, project: row.project })
      .then((d) => setSamples((prev) => ({ ...prev, [i]: { status: 'done', data: d } })))
      .catch((err: unknown) => setSamples((prev) => ({ ...prev, [i]: { status: 'error', error: toApiError(err) } })));
  };

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    setSelected(null);
    setExpanded(new Set());
    setSamples({});
    getErrorsTable({ week, scope })
      .then((d) => {
        if (!cancelled) setData(d);
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setData(null);
          setError(toApiError(err));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [week, scope, refreshToken]);

  const csv = useMemo(() => (data && data.snapshot ? buildErrorsCsv(data) : null), [data]);

  return (
    <section className={styles.panel} aria-label="Errors" data-testid="errors-panel">
      <div className={styles.toolbar}>
        <div className={styles.title}>
          Errors — {week} · scope: {scope === 'all' ? 'All billed projects' : scope}
          {data ? (
            <>
              {' '}
              <StatusBadge status={data.snapshot ? data.snapshot.status_at_fetch : null} stale={data.snapshot?.stale ?? false} />
            </>
          ) : null}
        </div>
        <button type="button" className={styles.export} disabled={csv === null} onClick={() => csv !== null && downloadTextFile(errorsCsvFilename(scope, week), csv)} data-testid="errors-export">
          Export CSV
        </button>
      </div>

      {error ? (
        <ErrorBanner banner={bannerFromError('errors', 'Errors table', error)} />
      ) : loading && !data ? (
        <div className={styles.loading} aria-busy="true" aria-label="loading errors" data-testid="errors-loading">
          <span className={styles.skeleton} />
          <span className={styles.skeleton} />
          <span className={styles.skeleton} />
        </div>
      ) : data && data.snapshot === null ? (
        <EmptyState title="Errors not fetched yet" testId="errors-empty">
          Refresh this week to fetch error counts
        </EmptyState>
      ) : data ? (
        <ErrorsView data={data} selected={selected} onSelect={setSelected} expanded={expanded} onToggleExpand={toggleExpand} samples={samples} onSample={runSample} />
      ) : null}
    </section>
  );
}
