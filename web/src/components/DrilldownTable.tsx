import { useEffect, useState } from 'react';
import { ApiError, getBreakdown, toApiError } from '@/lib/api';
import { formatInteger, formatMoney, formatSignedMoney } from '@/lib/format';
import type { BreakdownResponse, BreakdownRow, Column, Scope } from '@/lib/types';
import styles from './DrilldownTable.module.css';

interface Props {
  snapshotId: number;
  scope: Scope;
  /** The clicked column: `service` → SKU level; `total` → raw services. */
  column: Column;
  /** Net of the clicked cell, for the reconciliation check. */
  cellNet: number;
  /** All table columns, to label `column_key` at the service level. */
  columns: readonly Column[];
}

type View =
  | { level: 'service' }
  | { level: 'sku' }
  | { level: 'resource'; service: string; sku: string; skuNet: number };

interface Section {
  heading: string | null;
  data: BreakdownResponse;
}

/** Display rounding is 2 dp, so allow a cent of slack before warning. */
const MISMATCH_TOLERANCE = 0.01;
const GLOBAL_NAME_MAX = 48;

export function DrilldownTable({ snapshotId, scope, column, cellNet, columns }: Props) {
  const initialView: View = column.kind === 'total' ? { level: 'service' } : { level: 'sku' };
  const [view, setView] = useState<View>(initialView);
  const [sections, setSections] = useState<Section[] | null>(null);
  const [error, setError] = useState<ApiError | null>(null);

  // Reset to the first level when the target cell changes.
  useEffect(() => {
    setView(column.kind === 'total' ? { level: 'service' } : { level: 'sku' });
  }, [snapshotId, column.key, column.kind]);

  useEffect(() => {
    let cancelled = false;
    setSections(null);
    setError(null);
    (async () => {
      try {
        let next: Section[];
        if (view.level === 'service') {
          next = [{ heading: null, data: await getBreakdown(snapshotId, { level: 'service', scope }) }];
        } else if (view.level === 'sku') {
          const services = column.services ?? [];
          const results = await Promise.all(services.map((s) => getBreakdown(snapshotId, { level: 'sku', scope, service: s })));
          next = results.map((data, i) => ({ heading: services.length > 1 ? (services[i] ?? null) : null, data }));
        } else {
          next = [
            {
              heading: null,
              data: await getBreakdown(snapshotId, { level: 'resource', scope, service: view.service, sku: view.sku }),
            },
          ];
        }
        if (!cancelled) setSections(next);
      } catch (err) {
        if (!cancelled) setError(toApiError(err));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [snapshotId, scope, column.services, view]);

  const expected = view.level === 'resource' ? view.skuNet : cellNet;
  const totals = sections?.reduce(
    (acc, s) => ({ gross: acc.gross + s.data.totals.gross, credits: acc.credits + s.data.totals.credits, net: acc.net + s.data.totals.net }),
    { gross: 0, credits: 0, net: 0 },
  );
  const mismatch = totals ? Math.abs(totals.net - expected) > MISMATCH_TOLERANCE : false;
  const rowCount = sections?.reduce((n, s) => n + s.data.rows.length, 0) ?? 0;

  return (
    <section className={styles.section} aria-labelledby="drilldown-heading" data-testid="drilldown" data-level={view.level}>
      <div className={styles.head}>
        <h3 id="drilldown-heading" className={styles.heading}>
          Drill-down <span className={styles.level} data-testid="drilldown-level">{view.level}</span>
        </h3>
      </div>
      <nav className={styles.crumbs} aria-label="Drill-down path">
        <span>{column.label}</span>
        {view.level === 'resource' ? (
          <>
            <span className={styles.sep}>›</span>
            <span className={styles.crumbSku} title={view.sku}>
              {view.sku}
            </span>
            <button type="button" className={styles.back} data-testid="drilldown-back" onClick={() => setView({ level: 'sku' })}>
              ‹ back to SKUs
            </button>
          </>
        ) : null}
      </nav>

      {error ? (
        <p className={styles.error} role="alert" data-testid="drilldown-error">
          <code>{error.code}</code> — {error.message}
          {error.hint ? <strong> {error.hint}</strong> : null}
        </p>
      ) : sections === null ? (
        <div className={styles.skeletonBlock} aria-busy="true" aria-label="loading breakdown">
          <span className={styles.skeleton} />
          <span className={styles.skeleton} />
          <span className={styles.skeleton} />
        </div>
      ) : rowCount === 0 ? (
        <p className={styles.empty}>No rows at this level for the current scope.</p>
      ) : (
        <>
          {mismatch && totals ? (
            <p className={styles.mismatch} role="alert" data-testid="drilldown-mismatch">
              mismatch: rows sum to {formatMoney(totals.net)} but the cell shows {formatMoney(expected)} — this should never happen;
              please report it with the snapshot id.
            </p>
          ) : null}
          <table className={styles.table} data-testid="drilldown-table">
            <thead>
              <tr>
                {view.level === 'service' ? (
                  <>
                    <th scope="col">Service</th>
                    <th scope="col">Column</th>
                  </>
                ) : view.level === 'sku' ? (
                  <th scope="col">SKU</th>
                ) : (
                  <>
                    <th scope="col">Resource</th>
                    <th scope="col">Project</th>
                    <th scope="col">Global name</th>
                  </>
                )}
                <th scope="col" className={styles.num}>
                  gross
                </th>
                <th scope="col" className={styles.num}>
                  credits
                </th>
                <th scope="col" className={styles.num}>
                  net
                </th>
                {view.level === 'sku' ? (
                  <th scope="col" className={styles.num}>
                    lines
                  </th>
                ) : null}
              </tr>
            </thead>
            <tbody>
              {sections.map((s, si) => (
                <SectionRows
                  key={s.heading ?? si}
                  section={s}
                  view={view}
                  columns={columns}
                  colSpan={view.level === 'sku' ? 5 : view.level === 'service' ? 5 : 6}
                  onSku={(row) => setView({ level: 'resource', service: s.data.service ?? '', sku: row.key, skuNet: row.net })}
                />
              ))}
            </tbody>
            {totals ? (
              <tfoot>
                <tr data-testid="drilldown-totals">
                  <th scope="row" colSpan={view.level === 'sku' ? 1 : view.level === 'service' ? 2 : 3}>
                    Total
                  </th>
                  <td className={styles.num}>{formatMoney(totals.gross)}</td>
                  <td className={styles.num}>{formatSignedMoney(totals.credits)}</td>
                  <td className={`${styles.num} ${mismatch ? styles.mismatchCell : ''}`} data-testid="drilldown-total-net">
                    {formatMoney(totals.net)}
                  </td>
                  {view.level === 'sku' ? <td /> : null}
                </tr>
              </tfoot>
            ) : null}
          </table>
        </>
      )}
    </section>
  );
}

interface SectionRowsProps {
  section: Section;
  view: View;
  columns: readonly Column[];
  colSpan: number;
  onSku: (row: BreakdownRow) => void;
}

function SectionRows({ section, view, columns, colSpan, onSku }: SectionRowsProps) {
  return (
    <>
      {section.heading ? (
        <tr className={styles.subheading}>
          <th scope="rowgroup" colSpan={colSpan}>
            {section.heading}
          </th>
        </tr>
      ) : null}
      {section.data.rows.map((row, i) => (
        <tr key={`${row.key}-${row.project_id ?? ''}-${row.resource_global_name ?? ''}-${i}`} data-testid="drilldown-row">
          {view.level === 'service' ? (
            <>
              <td>{row.key}</td>
              <td className={styles.muted}>{columns.find((c) => c.key === row.column_key)?.label ?? row.column_key ?? '—'}</td>
            </>
          ) : view.level === 'sku' ? (
            <td>
              <button
                type="button"
                className={styles.skuLink}
                data-testid="drilldown-row-link"
                onClick={() => onSku(row)}
                title={row.sku_id ? `SKU id ${row.sku_id} — click for resources` : 'click for resources'}
              >
                {row.key}
              </button>
            </td>
          ) : (
            <>
              <td>
                {row.resource_name ? (
                  row.resource_name
                ) : (
                  <>
                    <em>(unattributed)</em> <span className={styles.muted}>no resource name</span>
                  </>
                )}
              </td>
              <td className={styles.muted}>{row.project_id ?? '—'}</td>
              <td className={styles.muted} title={row.resource_global_name ?? undefined}>
                {truncate(row.resource_global_name)}
              </td>
            </>
          )}
          <td className={styles.num}>{formatMoney(row.gross)}</td>
          <td className={styles.num}>{formatSignedMoney(row.credits)}</td>
          <td className={styles.num} data-testid="drilldown-row-net">
            {formatMoney(row.net)}
          </td>
          {view.level === 'sku' ? <td className={styles.num}>{formatInteger(row.line_items)}</td> : null}
        </tr>
      ))}
    </>
  );
}

function truncate(s: string | null | undefined): string {
  if (!s) return '—';
  return s.length > GLOBAL_NAME_MAX ? `…${s.slice(-GLOBAL_NAME_MAX)}` : s;
}
