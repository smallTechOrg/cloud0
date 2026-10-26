import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { errorsTableFixture, metricsTableFixture } from '@/lib/fixtures2';
import type { ErrorsTableResponse, MetricsTableResponse } from '@/lib/types';
import { ErrorsView } from './ErrorsPanel';
import { MetricsView } from './MetricsPanel';

const noop = () => {};

function metricsHtml(data: MetricsTableResponse = metricsTableFixture, selected: Parameters<typeof MetricsView>[0]['selected'] = null) {
  return renderToStaticMarkup(createElement(MetricsView, { data, selected, onSelect: noop }));
}
function errorsHtml(data: ErrorsTableResponse = errorsTableFixture, selected: number | null = null) {
  return renderToStaticMarkup(createElement(ErrorsView, { data, selected, onSelect: noop }));
}

describe('MetricsView', () => {
  it('renders the seven group headings in sheet order', () => {
    const html = metricsHtml();
    const order = ['VMs', 'Cloud SQL', 'Networking', 'Cloud Run', 'Cloud Storage', 'Cloud Logging', 'Errors'];
    const positions = order.map((h) => html.indexOf(`>${h}</h3>`));
    expect(positions.every((p) => p >= 0)).toBe(true);
    expect([...positions].sort((a, b) => a - b)).toEqual(positions);
  });

  it('renders gaps as text with a tooltip, never 0', () => {
    const html = metricsHtml();
    expect(html).toContain('no agent');
    expect(html).toContain('not collected');
    expect(html).toContain('no agent: install the Ops Agent');
    expect(html).toContain('data-gap="true"');
    expect(html).toContain('12.50');
  });

  it('shows the gap hint inline and provenance when a gap cell is selected', () => {
    const html = metricsHtml(metricsTableFixture, { group: 'vm', project: 'project-a', resource: 'vm-a', column: 'memory_pct' });
    expect(html).toContain('data-testid="metrics-gap-hint"');
    expect(html).toContain('install the Ops Agent');
    expect(html).toContain('data-testid="metric-provenance"');
    expect(html).toContain('agent.googleapis.com/memory/percent_used');
    expect(html).toContain('ALIGN_MEAN');
  });

  it('shows provenance filter, aggregation, window and fetched_at for a value cell', () => {
    const html = metricsHtml(metricsTableFixture, { group: 'vm', project: 'project-a', resource: 'vm-a', column: 'cpu_pct' });
    expect(html).toContain('instance_name=&quot;vm-a&quot;');
    expect(html).toContain('metric-provenance-window');
    expect(html).toContain('metric-provenance-fetched');
  });

  it('colours near and over cells, leaves ok uncoloured', () => {
    const html = metricsHtml();
    expect(html).toContain('data-limit-level="near"');
    expect(html).toContain('limitNear');
    expect(html).toContain('data-limit-level="ok"');
    const okTd = html.slice(html.indexOf('data-limit-level="ok"') - 80, html.indexOf('data-limit-level="ok"'));
    expect(okTd).not.toContain('limitNear');
    expect(okTd).not.toContain('limitOver');

    const over: MetricsTableResponse = JSON.parse(JSON.stringify(metricsTableFixture));
    const cell = over.groups[2]?.rows[0]?.cells.outbound_gb;
    if (!cell) throw new Error('fixture changed');
    cell.limit_level = 'over';
    const overHtml = metricsHtml(over);
    expect(overHtml).toContain('limitOver');
    expect(overHtml).toContain('over limit');
  });
});

describe('ErrorsView', () => {
  it('renders the Cloud Run table with 4xx/5xx emphasised and a totals row', () => {
    const html = errorsHtml();
    expect(html).toContain('data-emphasis="4xx"');
    expect(html).toContain('data-emphasis="5xx"');
    expect(html).toContain('data-testid="errors-cloudrun-total"');
    expect(html).toContain('1,000');
  });

  it('does not emphasise zero counts', () => {
    const zero: ErrorsTableResponse = JSON.parse(JSON.stringify(errorsTableFixture));
    const r = zero.rows[0];
    if (!r) throw new Error('fixture changed');
    r.c4xx = 0;
    r.c5xx = 0;
    expect(errorsHtml(zero)).not.toContain('data-emphasis');
  });

  it('renders Error Reporting groups and occurrences', () => {
    const html = errorsHtml();
    expect(html).toContain('data-testid="errors-reporting-row"');
    expect(html).toContain('>40<');
  });

  it('renders one gap card per gap source with reason and fix hint', () => {
    const html = errorsHtml();
    expect(html.match(/data-testid="errors-gap-card"/g)?.length).toBe(2);
    expect(html).toContain('logging disabled');
    expect(html).toContain('backend service');
    expect(html).toContain('add the Ops Agent nginx receiver');
  });

  it('opens provenance for the selected row', () => {
    const html = errorsHtml(errorsTableFixture, 2);
    expect(html).toContain('data-testid="metric-provenance"');
    expect(html).toContain('loadbalancing.googleapis.com');
  });
});
