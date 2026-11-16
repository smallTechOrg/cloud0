import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { DailyResponse } from '@/lib/types';
import { DailyChartView } from './DailyChart';

const data: DailyResponse = {
  from: '2026-10-12', to: '2026-10-13', scope: 'all', by: 'service', top: 8, generated_at: '2026-10-26T09:00:00Z', total: 150, notes: [],
  days: [
    { date: '2026-10-12', week: '2026-W42', week_status: 'final', snapshot_id: 1, has_daily: true, total: 150 },
    { date: '2026-10-13', week: '2026-W42', week_status: 'final', snapshot_id: null, has_daily: false, total: null },
  ],
  series: [
    { key: 'svc:Compute Engine', label: 'Compute Engine', kind: 'group', total: 100, values: [100, null] },
    { key: 'svc:Cloud SQL', label: 'Cloud SQL', kind: 'group', total: 50, values: [50, null] },
  ],
};

const render = (o: Partial<Parameters<typeof DailyChartView>[0]> = {}) =>
  renderToStaticMarkup(createElement(DailyChartView, { data, currency: 'USD', hover: null, onHover: () => undefined, showTable: false, ...o }));

describe('DailyChartView', () => {
  it('draws one stacked segment per series and a hatched no-data day, with a legend', () => {
    const html = render();
    expect(html.match(/data-series="/g)).toHaveLength(2);
    expect(html).toContain('data-testid="daily-day-2026-10-13" data-has-data="false"');
    expect(html).toContain('daily-legend-svc:Compute Engine');
    expect(html).toContain('$100.00');
  });

  it('shows the hovered day breakdown and the optional table', () => {
    const html = render({ hover: 0, showTable: true });
    expect(html).toContain('data-testid="daily-tooltip"');
    expect(html).toContain('$150.00');
    expect(html).toContain('data-testid="daily-table"');
    expect(render({ hover: 1 })).toContain('Week not fetched yet.');
  });
});
