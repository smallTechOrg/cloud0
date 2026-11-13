import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ApiError } from '@/lib/api';
import { errorSamplesFixture, errorsTableFixture, metricsTableFixture } from '@/lib/fixtures2';
import type { ErrorsTableResponse, MetricsTableResponse } from '@/lib/types';
import { sampleKey } from './ErrorSample';
import { ErrorsView, type SampleState } from './ErrorsPanel';
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

  it('renders a bold TOTAL row per group, dashes for percentages, and the active/idle columns', () => {
    const html = metricsHtml();
    expect(html.match(/data-testid="metrics-total-row"/g)?.length).toBe(5);
    expect(html).toContain('>Total<');
    expect(html).toContain('Active memory (GiB-s)');
    expect(html).toContain('Idle (min-instance) memory (GiB-s)');
    // VM total: percentage columns are dashes (a gap cell), not sums.
    const vm = html.slice(html.indexOf('metrics-group-vm'), html.indexOf('metrics-group-cloudsql'));
    const total = vm.slice(vm.indexOf('metrics-total-row'));
    expect(total.match(/data-gap="true"/g)?.length).toBe(3);
    expect(total).toContain('>—<');
    // Selecting the total row's cell opens its provenance.
    const sel = metricsHtml(metricsTableFixture, { group: 'cloudrun', project: '', resource: '', column: 'service_memory_gib_s', total: true });
    expect(sel).toContain('Cloud Run · Total · Memory total (GiB-s)');
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
    for (const r of zero.rows) {
      if (r.source !== 'cloudrun') continue;
      r.c4xx = 0;
      r.c5xx = 0;
    }
    zero.totals.c4xx = 0;
    zero.totals.c5xx = 0;
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
    const html = errorsHtml(errorsTableFixture, 5);
    expect(html).toContain('data-testid="metric-provenance"');
    expect(html).toContain('loadbalancing.googleapis.com');
  });
});

describe('ErrorsView explainability', () => {
  const view = (props: Partial<Parameters<typeof ErrorsView>[0]> = {}) =>
    renderToStaticMarkup(createElement(ErrorsView, { data: errorsTableFixture, selected: null, onSelect: noop, ...props }));

  it('keeps the short "How these are counted" note with the scanner explanation under All sources', () => {
    const html = view();
    expect(html).toContain('data-testid="errors-how-counted"');
    expect(html).toContain('How these are counted');
    expect(html).toContain('run.googleapis.com/request_count');
    expect(html).toContain('internet scanners');
    expect(html).toContain('5xx are the real application-failure signal');
    expect(html.indexOf('errors-look-at')).toBeLessThan(html.indexOf('All sources'));
  });

  it('offers Details on triage items, closed until expanded; the table has no detail rows', () => {
    const collapsed = view({ onToggleExpand: noop });
    expect(collapsed).toContain('data-testid="triage-details-button"');
    expect(collapsed).not.toContain('data-testid="errors-detail"');
    const open = view({ onToggleExpand: noop, expanded: new Set([0]), onSample: noop });
    expect(open).toContain('Server errors (5xx)');
    expect(open).toContain('Client errors that may be real');
    expect(open).toContain('404s: scanner-like vs possible broken links');
  });

  it('shows code chips (5xx first), the Logs Explorer link and the sample button on a triage item', () => {
    const html = view({ onSample: noop });
    expect(html).toContain('404 ×11');
    expect(html).toContain('data-kind="5xx"');
    expect(html).not.toContain('200 ×');
    expect(html.indexOf('502 ×3')).toBeLessThan(html.indexOf('404 ×11'));
    expect(html).toContain('Show failing requests');
    expect(html).toContain('target="_blank"');
    expect(html).toContain('rel="noopener noreferrer"');
    expect(html).toContain('console.cloud.google.com/logs/query');
  });

  it('says so when a stored batch predates exact codes', () => {
    const old: ErrorsTableResponse = JSON.parse(JSON.stringify(errorsTableFixture));
    for (const r of old.rows) if (r.source === 'cloudrun') r.by_code = null;
    const html = view({ data: old });
    expect(html).toContain('refresh the week for exact codes');
    expect(html).toContain('data-testid="errors-refresh-hint"');
  });

  it('renders loading, error, result, empty and out-of-retention sample states', () => {
    const at = (sample: SampleState) => view({ samples: { [sampleKey(0, 'server')]: sample }, onSample: noop });
    expect(at({ status: 'loading' })).toContain('data-testid="errors-sample-loading"');
    const err = at({ status: 'error', error: new ApiError(403, 'permission_denied', 'denied', 'grant roles/logging.viewer') });
    expect(err).toContain('data-testid="errors-sample-error"');
    expect(err).toContain('denied');
    expect(err).toContain('roles/logging.viewer');
    const done = at({ status: 'done', data: { ...errorSamplesFixture, focus: 'server' } });
    expect(done).toContain('/.git/config');
    expect(done).toContain('projects/project-a/traces/abc');
    expect(done).toContain('most recent entries');
    expect(at({ status: 'done', data: { ...errorSamplesFixture, scanned: 0, top_paths: [], entries: [], scanner_share_4xx: null } })).toContain('data-testid="errors-sample-empty"');
    expect(at({ status: 'done', data: { ...errorSamplesFixture, scanned: 0, out_of_retention: true, retention_note: '30 days' } })).toContain('Out of retention');
  });
});
