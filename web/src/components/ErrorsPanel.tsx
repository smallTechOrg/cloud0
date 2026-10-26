'use client';

import { useEffect, useMemo, useState } from 'react';
import { ApiError, getErrorsTable, toApiError } from '@/lib/api';
import { downloadTextFile } from '@/lib/csv';
import { ERROR_SOURCE_LABELS, buildErrorsCsv, errorsCsvFilename, formatCount } from '@/lib/metricsFormat';
import type { ErrorTableRow, ErrorTotals, ErrorsTableResponse } from '@/lib/types';
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

interface ViewProps {
  data: ErrorsTableResponse;
  /** Index into `data.rows` of the row whose provenance is open. */
  selected: number | null;
  onSelect: (index: number | null) => void;
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
export function ErrorsView({ data, selected, onSelect }: ViewProps) {
  const indexed = data.rows.map((row, index) => ({ row, index }));
  const cloudrun = indexed.filter((x) => x.row.source === 'cloudrun');
  const reporting = indexed.filter((x) => x.row.source === 'error_reporting');
  const gapCards = indexed.filter((x) => x.row.source === 'load_balancer' || x.row.source === 'nginx');
  const t: ErrorTotals = data.totals;
  const sel = selected !== null ? data.rows[selected] : undefined;
  const toggle = (i: number) => onSelect(selected === i ? null : i);

  return (
    <div data-testid="errors-block">
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
                  <tr key={index} data-testid="errors-cloudrun-row">
                    <th scope="row" className={styles.svcCell}>
                      {r.service}
                      <span className={styles.project}>{r.project}</span>
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

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    setSelected(null);
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
        <ErrorsView data={data} selected={selected} onSelect={setSelected} />
      ) : null}
    </section>
  );
}
