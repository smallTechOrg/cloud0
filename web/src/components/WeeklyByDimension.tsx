import { useEffect, useState } from 'react';
import { getWeeklyByDimension, type WeeklyBreakdownResponse } from '@/lib/attributionApi';
import type { AllocateMode } from '@/lib/attributionTypes';
import { toApiError } from '@/lib/api';
import { buildCsv, downloadTextFile } from '@/lib/csv';
import { useCurrency } from '@/lib/currency';
import type { CostRow, Scope, WeekID } from '@/lib/types';
import { CostTable } from './CostTable';
import { EmptyState } from './EmptyState';
import { ErrorBanner, bannerFromError, type BannerData } from './ErrorBanner';
import styles from './CostByDimension.module.css';

interface Props {
  dimension: 'product' | 'environment';
  scope: Scope;
  selectedWeek: WeekID | null;
  allocate: AllocateMode | null;
  refreshToken: number;
  refreshDisabled: boolean;
  /** A cell click selects that week, so the breakdown below follows. */
  onSelectWeek: (week: WeekID) => void;
  onRefreshWeek: (week: WeekID) => void;
}

/** File name for the weekly product / environment CSV. */
export function weeklyCsvFilename(data: Pick<WeeklyBreakdownResponse, 'dimension' | 'scope' | 'from' | 'to' | 'allocate'>, currency: string): string {
  const safeScope = data.scope.replace(/[^A-Za-z0-9._-]/g, '_');
  return `cloud0-cost-by-${data.dimension}-${safeScope}-${data.from}-${data.to}-${data.allocate}-${currency.toUpperCase()}.csv`;
}

/**
 * Weeks × products (or environments): the Cost table's rows, deltas and
 * anomaly flags with one column per product, so product cost trends read the
 * same way service cost does.
 */
export default function WeeklyByDimension(p: Props) {
  const currency = useCurrency();
  const [data, setData] = useState<WeeklyBreakdownResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [banner, setBanner] = useState<BannerData | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getWeeklyByDimension(p.dimension, { scope: p.scope, allocate: p.allocate ?? undefined })
      .then((res) => {
        if (cancelled) return;
        setData(res);
        setBanner(null);
      })
      .catch((err: unknown) => {
        if (!cancelled) setBanner(bannerFromError(`weekly-${p.dimension}`, `Weekly cost by ${p.dimension}`, toApiError(err)));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [p.dimension, p.scope, p.allocate, p.refreshToken]);

  const word = p.dimension;
  const noGroups = data !== null && data.columns.every((c) => c.kind !== 'service');

  return (
    <section className={styles.panel} data-testid="weekly-dimension" data-dimension={p.dimension} aria-label={`Weekly cost by ${word}`}>
      {banner ? <ErrorBanner banner={banner} onDismiss={() => setBanner(null)} /> : null}
      <div className={styles.toolbar}>
        <strong>Cost by {word} per week</strong>
        <button
          type="button"
          className={styles.secondary}
          data-testid="weekly-dimension-export"
          disabled={data === null}
          onClick={() => data && downloadTextFile(weeklyCsvFilename(data, currency), buildCsv(data.columns, data.rows))}
        >
          Export CSV
        </button>
      </div>
      {data === null ? (
        loading ? (
          <div className={styles.skeleton} aria-busy="true" aria-label={`loading weekly ${word}`} data-testid="weekly-dimension-loading" />
        ) : (
          <EmptyState title={`Weekly cost by ${word} unavailable`} testId="weekly-dimension-unavailable">
            The table could not be loaded (see the banner above).
          </EmptyState>
        )
      ) : noGroups && data.rows.some((r) => r.snapshot !== null) ? (
        <EmptyState title={`No ${word} groups yet`} testId="weekly-dimension-no-groups">
          Add rules (or <code>product</code> / <code>env</code> labels) in the Attribution tab; until then everything is unattributed.
        </EmptyState>
      ) : (
        <CostTable
          columns={data.columns}
          rows={data.rows}
          selectedWeek={p.selectedWeek}
          thresholds={data.thresholds}
          loading={loading}
          fallback={false}
          refreshDisabled={p.refreshDisabled}
          onCellClick={(row: CostRow) => p.onSelectWeek(row.week)}
          onRefreshWeek={p.onRefreshWeek}
          caption={`Cost by ${word} per ISO week · ${currency}, net of credits · (shared) ${data.allocate === 'none' ? 'kept separate' : `allocated ${data.allocate}`} · click a number to break that week down below.`}
        />
      )}
    </section>
  );
}
