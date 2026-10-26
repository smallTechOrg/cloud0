'use client';

import { useEffect, useMemo, useState } from 'react';
import { ApiError, getMetricsTable, toApiError } from '@/lib/api';
import { downloadTextFile } from '@/lib/csv';
import {
  buildMetricsCsv,
  cellText,
  cellTitle,
  groupHeading,
  headerUnit,
  isGap,
  limitClassKey,
  limitLabel,
  metricsCsvFilename,
  orderGroups,
  resourceLabel,
} from '@/lib/metricsFormat';
import type { MetricCell, MetricGroup, MetricsTableResponse } from '@/lib/types';
import { EmptyState } from './EmptyState';
import { ErrorBanner, bannerFromError } from './ErrorBanner';
import { MetricProvenance } from './MetricProvenance';
import { StatusBadge } from './StatusBadge';
import styles from './MetricsPanel.module.css';

export interface MetricsPanelProps {
  week: string;
  scope: string;
  refreshToken: number;
}

export interface SelectedMetricCell {
  group: string;
  project: string;
  resource: string;
  column: string;
}

function isSelected(sel: SelectedMetricCell | null, group: string, project: string, resource: string, column: string): boolean {
  return !!sel && sel.group === group && sel.project === project && sel.resource === resource && sel.column === column;
}

interface ViewProps {
  data: MetricsTableResponse;
  selected: SelectedMetricCell | null;
  onSelect: (sel: SelectedMetricCell | null) => void;
}

function findCell(data: MetricsTableResponse, sel: SelectedMetricCell | null): { cell: MetricCell; title: string } | null {
  if (!sel) return null;
  const g = data.groups.find((x) => x.group === sel.group);
  const row = g?.rows.find((r) => r.project === sel.project && r.resource === sel.resource);
  const cell = row?.cells[sel.column];
  const col = g?.columns.find((c) => c.key === sel.column);
  if (!g || !row || !cell || !col) return null;
  const who = resourceLabel(row.project, row.resource).name;
  return { cell, title: `${groupHeading(g.group, g.label)} · ${who} · ${col.label}` };
}

/** Pure render of the Metrics block (used by the panel and by render tests). */
export function MetricsView({ data, selected, onSelect }: ViewProps) {
  const groups = orderGroups(data.groups);
  const found = findCell(data, selected);
  return (
    <div data-testid="metrics-block">
      {groups.map((g) => (
        <GroupTable key={g.group} group={g} selected={selected} onSelect={onSelect} />
      ))}
      {found ? (
        <MetricProvenance
          title={found.title}
          provenance={found.cell.provenance}
          gap={isGap(found.cell) ? { text: found.cell.gap ?? '', hint: found.cell.hint } : null}
          onClose={() => onSelect(null)}
        />
      ) : null}
    </div>
  );
}

function GroupTable({ group, selected, onSelect }: { group: MetricGroup; selected: SelectedMetricCell | null; onSelect: ViewProps['onSelect'] }) {
  return (
    <section className={styles.group} data-testid={`metrics-group-${group.group}`} data-group={group.group}>
      <h3 className={styles.groupHeading}>{groupHeading(group.group, group.label)}</h3>
      <div className={styles.scroller}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th className={styles.resHead} scope="col">
                Resource
              </th>
              {group.columns.map((c) => {
                const unit = headerUnit(c.label, c.unit);
                return (
                  <th key={c.key} className={styles.numHead} scope="col">
                    {c.label}
                    {unit ? <span className={styles.unit}>{unit}</span> : null}
                  </th>
                );
              })}
            </tr>
          </thead>
          <tbody>
            {group.rows.length === 0 ? (
              <tr>
                <td className={styles.emptyRow} colSpan={group.columns.length + 1}>
                  no rows for this scope
                </td>
              </tr>
            ) : (
              group.rows.map((r) => {
                const who = resourceLabel(r.project, r.resource);
                return (
                  <tr key={`${r.project}|${r.resource}`} className={styles.row} data-testid="metrics-row">
                    <th scope="row" className={who.total ? `${styles.resCell} ${styles.totalRes}` : styles.resCell}>
                      {who.name}
                      {!who.total ? <span className={styles.project}>{r.project}</span> : null}
                    </th>
                    {group.columns.map((c) => (
                      <MetricTd
                        key={c.key}
                        cell={r.cells[c.key]}
                        label={`${who.name} ${c.label}`}
                        selected={isSelected(selected, group.group, r.project, r.resource, c.key)}
                        onClick={() =>
                          onSelect(isSelected(selected, group.group, r.project, r.resource, c.key) ? null : { group: group.group, project: r.project, resource: r.resource, column: c.key })
                        }
                      />
                    ))}
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function MetricTd({ cell, label, selected, onClick }: { cell: MetricCell | undefined; label: string; selected: boolean; onClick: () => void }) {
  const gap = isGap(cell);
  const text = cellText(cell);
  const limitKey = !gap && cell ? limitClassKey(cell.limit_level) : null;
  const classes = [styles.num];
  if (limitKey) classes.push(styles[limitKey]);
  if (selected) classes.push(styles.selectedCell);
  const lvl = limitLabel(cell?.limit_level ?? null);
  return (
    <td className={classes.join(' ')} data-limit-level={!gap && cell?.limit_level ? cell.limit_level : undefined} data-gap={gap ? 'true' : undefined}>
      <button
        type="button"
        className={gap ? `${styles.cellButton} ${styles.gapText}` : styles.cellButton}
        title={cellTitle(cell)}
        aria-label={`${label} ${text}, open provenance`}
        aria-pressed={selected}
        disabled={!cell}
        onClick={onClick}
      >
        <span>{text}</span>
        {lvl ? <span className={styles.limitTag}>{lvl}</span> : null}
      </button>
      {gap && selected && cell?.hint ? (
        <span className={styles.inlineHint} data-testid="metrics-gap-hint">
          {cell.hint}
        </span>
      ) : null}
    </td>
  );
}

export default function MetricsPanel({ week, scope, refreshToken }: MetricsPanelProps) {
  const [data, setData] = useState<MetricsTableResponse | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState<SelectedMetricCell | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    setSelected(null);
    getMetricsTable({ week, scope })
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

  const csv = useMemo(() => (data && data.snapshot ? buildMetricsCsv(data) : null), [data]);

  return (
    <section className={styles.panel} aria-label="Metrics" data-testid="metrics-panel">
      <div className={styles.toolbar}>
        <div className={styles.title}>
          Metrics — {week} · scope: {scope === 'all' ? 'All billed projects' : scope}
          {data ? (
            <>
              {' '}
              <StatusBadge status={data.snapshot ? data.week_status : null} stale={data.snapshot?.stale ?? false} />
            </>
          ) : null}
        </div>
        <button
          type="button"
          className={styles.export}
          disabled={csv === null}
          onClick={() => csv !== null && downloadTextFile(metricsCsvFilename(scope, week), csv)}
          data-testid="metrics-export"
        >
          Export CSV
        </button>
      </div>

      {error ? (
        <ErrorBanner banner={bannerFromError('metrics', 'Metrics table', error)} />
      ) : loading && !data ? (
        <div className={styles.loading} aria-busy="true" aria-label="loading metrics" data-testid="metrics-loading">
          <span className={styles.skeleton} />
          <span className={styles.skeleton} />
          <span className={styles.skeleton} />
        </div>
      ) : data && data.snapshot === null ? (
        <EmptyState title="Metrics not fetched yet" testId="metrics-empty">
          Refresh this week to fetch metrics
        </EmptyState>
      ) : data ? (
        <MetricsView data={data} selected={selected} onSelect={setSelected} />
      ) : null}
    </section>
  );
}
