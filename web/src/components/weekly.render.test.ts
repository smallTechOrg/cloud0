import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { buildCsv } from '@/lib/csv';
import type { Column, CostRow } from '@/lib/types';
import { CostTable } from './CostTable';
import { weeklyCsvFilename } from './WeeklyByDimension';

const columns: Column[] = [
  { key: 'week', label: 'Week', kind: 'week' },
  { key: 'alpha', label: 'alpha', kind: 'service', group: 'product' },
  { key: '(shared)', label: '(shared)', kind: 'service', group: 'shared' },
  { key: '(unattributed)', label: '(unattributed)', kind: 'service', group: 'unattributed' },
  { key: 'total', label: 'Total GCP', kind: 'total' },
  { key: 'pct_change', label: '% change', kind: 'pct_change' },
];
const cell = (net: number) => ({ net, gross: net, credits: 0, delta_abs: null, delta_pct: null, flagged: false });
const rows: CostRow[] = [
  {
    week: '2026-W42', week_start: '2026-10-12', week_end: '2026-10-18', week_status: 'final',
    snapshot: { id: 7, fetched_at: '2026-10-20T00:00:00Z', status_at_fetch: 'final', run_id: 3, row_count: 9, stale: false },
    cells: { alpha: cell(170), '(shared)': cell(60), '(unattributed)': cell(25), total: cell(255) },
    pct_change: null, flagged: false, annotation: null,
  },
];

describe('weekly product table', () => {
  it('renders product columns through the Cost table with a product caption', () => {
    const html = renderToStaticMarkup(
      createElement(CostTable, {
        columns, rows, selectedWeek: '2026-W42', thresholds: { pct: 20, abs_usd: 50 }, loading: false, fallback: false,
        refreshDisabled: false, onCellClick: () => undefined, onRefreshWeek: () => undefined, caption: 'Cost by product per ISO week',
      }),
    );
    expect(html).toContain('Cost by product per ISO week');
    expect(html).not.toContain('Cost by service');
    expect(html.indexOf('alpha')).toBeLessThan(html.indexOf('(unattributed)'));
    expect(html).toContain('170');
  });

  it('exports the same columns as CSV with a dimension-specific file name', () => {
    expect(buildCsv(columns, rows).split('\r\n')[0]).toBe('Week,alpha,(shared),(unattributed),Total GCP,% change');
    expect(weeklyCsvFilename({ dimension: 'product', scope: 'all', from: '2026-W40', to: '2026-W42', allocate: 'even' }, 'inr')).toBe(
      'cloud0-cost-by-product-all-2026-W40-2026-W42-even-INR.csv',
    );
  });
});
