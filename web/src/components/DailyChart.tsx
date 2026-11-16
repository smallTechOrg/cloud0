import { useEffect, useMemo, useState } from 'react';
import { getDaily, toApiError } from '@/lib/api';
import { buildDailyCsv, maxStack, niceScale, seriesColors, stackDay } from '@/lib/dailyChart';
import { downloadTextFile } from '@/lib/csv';
import { useCurrency } from '@/lib/currency';
import { formatMoney } from '@/lib/format';
import type { DailyBy, DailyResponse, Scope } from '@/lib/types';
import styles from './DailyChart.module.css';

const RANGES = [14, 28, 56, 90] as const;
const BY_OPTIONS: { id: DailyBy; label: string }[] = [
  { id: 'service', label: 'Service' },
  { id: 'project', label: 'Project' },
  { id: 'product', label: 'Product' },
  { id: 'environment', label: 'Environment' },
];

// Chart geometry (viewBox units; the SVG scales to the panel width).
const W = 960;
const H = 240;
const PAD = { top: 10, right: 8, bottom: 24, left: 64 };
const GAP = 2; // surface gap between stacked segments and between bars

export interface DailyChartViewProps {
  data: DailyResponse;
  currency: string;
  hover: number | null;
  onHover: (i: number | null) => void;
  showTable: boolean;
}

/** Presentational stacked daily bars + legend (+ optional table); used by render tests. */
export function DailyChartView({ data, currency, hover, onHover, showTable }: DailyChartViewProps) {
  const colors = useMemo(() => seriesColors(data.series), [data.series]);
  const { top, step } = niceScale(maxStack(data));
  const n = Math.max(data.days.length, 1);
  const plotW = W - PAD.left - PAD.right;
  const plotH = H - PAD.top - PAD.bottom;
  const slot = plotW / n;
  const barW = Math.max(1, slot - GAP * 2);
  const y = (v: number) => PAD.top + plotH - (v / top) * plotH;
  const ticks: number[] = [];
  for (let t = 0; t <= top + 1e-9; t += step) ticks.push(t);
  const labelEvery = Math.max(1, Math.ceil(n / 10));
  const fmt = (v: number) => formatMoney(v, currency);
  const shortDate = (d: string) => `${d.slice(8, 10)}/${d.slice(5, 7)}`;
  const hovered = hover !== null ? data.days[hover] : undefined;
  const r = Math.min(4, barW / 2);

  return (
    <>
      <div className={styles.chartWrap} onMouseLeave={() => onHover(null)}>
        <svg
          className={styles.svg}
          viewBox={`0 0 ${W} ${H}`}
          role="img"
          aria-label={`Daily net cost by ${data.by}, ${data.from} to ${data.to}, total ${fmt(data.total)}`}
          data-testid="daily-svg"
        >
          <defs>
            <pattern id="daily-nodata" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
              <line x1="0" y1="0" x2="0" y2="6" stroke="#d9dee5" strokeWidth="2" />
            </pattern>
          </defs>
          {ticks.map((t) => (
            <g key={t}>
              <line className={styles.grid} x1={PAD.left} x2={W - PAD.right} y1={y(t)} y2={y(t)} />
              <text className={styles.axis} x={PAD.left - 6} y={y(t) + 4} textAnchor="end">
                {fmt(t)}
              </text>
            </g>
          ))}
          {data.days.map((d, i) => {
            const x = PAD.left + i * slot + GAP;
            const segs = stackDay(data.series, i);
            return (
              <g key={d.date} data-testid={`daily-day-${d.date}`} data-has-data={d.total !== null}>
                {d.total === null ? (
                  <rect className={styles.noData} x={x} y={PAD.top} width={barW} height={plotH} rx={2} />
                ) : (
                  segs.map((s, j) => {
                    const yTop = y(s.y1);
                    const h = Math.max(0, y(s.y0) - yTop - (j > 0 ? GAP : 0));
                    const last = j === segs.length - 1;
                    // Rounded data-end on the top segment only; the baseline end stays square.
                    const path = last && h > r
                      ? `M${x},${yTop + h}V${yTop + r}Q${x},${yTop} ${x + r},${yTop}H${x + barW - r}Q${x + barW},${yTop} ${x + barW},${yTop + r}V${yTop + h}Z`
                      : `M${x},${yTop + h}V${yTop}H${x + barW}V${yTop + h}Z`;
                    return <path key={s.key} d={path} fill={colors.get(s.key)} data-series={s.key} />;
                  })
                )}
                {i % labelEvery === 0 ? (
                  <text className={styles.axis} x={x + barW / 2} y={H - 6} textAnchor="middle">
                    {shortDate(d.date)}
                  </text>
                ) : null}
                <rect
                  className={hover === i ? `${styles.hit} ${styles.hitActive}` : styles.hit}
                  x={PAD.left + i * slot}
                  y={PAD.top}
                  width={slot}
                  height={plotH}
                  onMouseEnter={() => onHover(i)}
                  onFocus={() => onHover(i)}
                  tabIndex={0}
                  aria-label={`${d.date}: ${d.total === null ? 'no data' : fmt(d.total)}`}
                />
              </g>
            );
          })}
        </svg>
        {hovered && hover !== null ? (
          <div
            className={styles.tooltip}
            data-testid="daily-tooltip"
            style={(hover + 0.5) / n > 0.6 ? { right: `${(1 - (PAD.left + hover * slot) / W) * 100}%` } : { left: `${((PAD.left + (hover + 1) * slot) / W) * 100}%` }}
          >
            <strong>{hovered.date}</strong> <span className={styles.meta}>· {hovered.week}</span>
            {hovered.total === null ? (
              <p className={styles.note}>{hovered.snapshot_id === null ? 'Week not fetched yet.' : 'Fetched before daily data was captured — refresh the week.'}</p>
            ) : (
              <table>
                <tbody>
                  {data.series
                    .filter((s) => (s.values[hover] ?? 0) !== 0)
                    .map((s) => (
                      <tr key={s.key}>
                        <td>
                          <span className={styles.swatch} style={{ background: colors.get(s.key) }} />
                          {s.label}
                        </td>
                        <td>{fmt(s.values[hover] ?? 0)}</td>
                      </tr>
                    ))}
                  <tr className={styles.tipTotal}>
                    <td>Total</td>
                    <td>{fmt(hovered.total)}</td>
                  </tr>
                </tbody>
              </table>
            )}
          </div>
        ) : null}
      </div>

      <ul className={styles.legend} data-testid="daily-legend" aria-label="Series">
        {data.series.map((s) => (
          <li key={s.key} data-testid={`daily-legend-${s.key}`}>
            <span className={styles.swatch} style={{ background: colors.get(s.key) }} />
            {s.label}
            <span className={styles.legendValue}>{fmt(s.total)}</span>
          </li>
        ))}
      </ul>

      {showTable ? (
        <div className={styles.tableWrap}>
          <table className={styles.table} data-testid="daily-table">
            <thead>
              <tr>
                <th>Date</th>
                {data.series.map((s) => (
                  <th key={s.key}>{s.label}</th>
                ))}
                <th>Total</th>
              </tr>
            </thead>
            <tbody>
              {data.days.map((d, i) => (
                <tr key={d.date}>
                  <td>{d.date}</td>
                  {data.series.map((s) => (
                    <td key={s.key}>{s.values[i] === null ? '—' : fmt(s.values[i]!)}</td>
                  ))}
                  <td>{d.total === null ? '—' : fmt(d.total)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </>
  );
}

interface Props {
  scope: Scope;
  /** Bumped by the parent after any refresh; re-fetches. */
  refreshToken: number;
}

/** Daily net cost for the last N days, stacked by service / project / product / environment. */
export default function DailyChart({ scope, refreshToken }: Props) {
  const currency = useCurrency();
  const [by, setBy] = useState<DailyBy>('service');
  const [days, setDays] = useState<number>(28);
  const [data, setData] = useState<DailyResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [hover, setHover] = useState<number | null>(null);
  const [showTable, setShowTable] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getDaily({ scope, by, days })
      .then((res) => {
        if (cancelled) return;
        setData(res);
        setError(null);
        setHover(null);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const e = toApiError(err);
        setError(`${e.message}${e.hint ? ` ${e.hint}` : ''}`);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [scope, by, days, refreshToken]);

  return (
    <section className={styles.panel} data-testid="daily-chart" aria-labelledby="daily-title">
      <div className={styles.head}>
        <div id="daily-title" className={styles.title}>
          Daily cost — {currency}, net of credits
          {data ? (
            <span className={styles.meta}>
              {' '}
              · {data.from} → {data.to} · total {formatMoney(data.total, currency)}
            </span>
          ) : null}
        </div>
        <div className={styles.controls}>
          <label>
            Break down by
            <select value={by} onChange={(e) => setBy(e.target.value as DailyBy)} data-testid="daily-by">
              {BY_OPTIONS.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.label}
                </option>
              ))}
            </select>
          </label>
          <label>
            Range
            <select value={days} onChange={(e) => setDays(Number(e.target.value))} data-testid="daily-range">
              {RANGES.map((d) => (
                <option key={d} value={d}>
                  Last {d} days
                </option>
              ))}
            </select>
          </label>
          <button type="button" className={styles.button} aria-pressed={showTable} onClick={() => setShowTable((v) => !v)} data-testid="daily-table-toggle">
            {showTable ? 'Hide table' : 'Show table'}
          </button>
          <button
            type="button"
            className={styles.button}
            disabled={!data}
            data-testid="daily-export"
            onClick={() => data && downloadTextFile(`cloud0-daily-${data.by}-${data.scope.replace(/[^A-Za-z0-9._-]/g, '_')}-${data.from}-${data.to}-${currency.toUpperCase()}.csv`, buildDailyCsv(data))}
          >
            Export CSV
          </button>
        </div>
      </div>
      {error ? (
        <p className={styles.err} role="alert" data-testid="daily-error">
          {error}
        </p>
      ) : null}
      {data === null ? (
        loading ? <div className={styles.skeleton} aria-busy="true" aria-label="loading daily cost" /> : null
      ) : (
        <>
          <DailyChartView data={data} currency={currency} hover={hover} onHover={setHover} showTable={showTable} />
          {data.notes.map((note) => (
            <p key={note} className={styles.note} data-testid="daily-note">
              {note}
            </p>
          ))}
        </>
      )}
    </section>
  );
}
