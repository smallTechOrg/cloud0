import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ApiError } from '@/lib/api';
import { errorSamplesFixture, errorsTableFixture } from '@/lib/fixtures2';
import type { ErrorTableRow, ErrorVerdict, ErrorsSummary } from '@/lib/types';
import { sampleKey, type SampleMap } from './ErrorSample';
import { TriageView } from './ErrorsTriage';

const noop = () => undefined;
const base = errorsTableFixture.rows[0] as ErrorTableRow;
const summary: ErrorsSummary = { needs_attention: 0, watch: 0, noise: 0, healthy: 0, headline: 'Headline text' };

function one(verdict: ErrorVerdict, over: Partial<ErrorTableRow> = {}, samples?: SampleMap, expanded?: ReadonlySet<number>) {
  const row: ErrorTableRow = { ...base, verdict, rank: verdict === 'healthy' ? 0 : 1, ...over };
  return renderToStaticMarkup(createElement(TriageView, { rows: [row], summary, samples, expanded, onSample: noop, onToggleExpand: noop }));
}

describe('TriageView', () => {
  it('renders needs_attention with badge text, bold headline, reasons, 5xx-first codes, change and the three buttons', () => {
    const html = one('needs_attention');
    expect(html).toContain('What to look at');
    expect(html).toContain('<strong>Headline text</strong>');
    expect(html).toContain('>Needs attention<');
    expect(html).toContain('5xx up 3x vs last week');
    expect(html.indexOf('502 ×3')).toBeLessThan(html.indexOf('404 ×11'));
    expect(html).toContain('up 3× vs W42');
    expect(html).toContain('Show failing requests');
    expect(html).toContain('data-focus="server"');
    expect(html).toContain('Open in Logs Explorer');
    expect(html).toContain('console.cloud.google.com/logs/query');
    expect(html).toContain('>Details<');
    expect(html).not.toContain('errors-all-clear');
  });

  it('renders watch with a client focus when it has no 5xx', () => {
    const html = one('watch', { c5xx: 0, by_code: { '401': 30 }, reasons: ['30 client errors'] });
    expect(html).toContain('>Watch<');
    expect(html).toContain('data-focus="client"');
    expect(html).toContain('Show failing requests');
  });

  it('puts noise behind a collapsed section with the guidance line and Check paths', () => {
    const html = one('noise', { c5xx: 0, by_code: { '404': 100 } });
    expect(html).toContain('data-testid="errors-noise"');
    expect(html).toContain('Probably noise (1)');
    expect(html).toContain('No action needed unless these paths are real pages');
    expect(html).toContain('Check paths');
    expect(html).toContain('data-focus="notfound"');
    expect(html).toContain('errors-all-clear');
    expect(html).not.toMatch(/<details[^>]*errors-noise[^>]* open/);
  });

  it('shows healthy only as a collapsed count with no sample button', () => {
    const html = one('healthy', { c5xx: 0, c4xx: 0, by_code: { '200': 5 }, reasons: [] });
    expect(html).toContain('Healthy (1)');
    expect(html).toContain('data-testid="errors-healthy"');
    expect(html).not.toContain('triage-sample-button');
  });

  it('shows the green empty state when nothing needs attention', () => {
    const html = renderToStaticMarkup(createElement(TriageView, { rows: [], summary }));
    expect(html).toContain('Nothing needs attention this week');
    expect(html).toContain('errors-all-clear');
  });

  it('hints to refresh when exact codes are missing', () => {
    const html = one('needs_attention', { by_code: null });
    expect(html).toContain('refresh the week for exact codes');
  });

  it('orders needs_attention before watch by rank', () => {
    const rows = [
      { ...base, service: 'watch-svc', verdict: 'watch' as const, rank: 2 },
      { ...base, service: 'bad-svc', verdict: 'needs_attention' as const, rank: 1 },
    ];
    const html = renderToStaticMarkup(createElement(TriageView, { rows, summary }));
    expect(html.indexOf('bad-svc')).toBeLessThan(html.indexOf('watch-svc'));
  });

  it('renders inline sample states without a bare error', () => {
    const k = sampleKey(0, 'server');
    expect(one('needs_attention', {}, { [k]: { status: 'loading' } })).toContain('errors-sample-loading');
    const err = one('needs_attention', {}, { [k]: { status: 'error', error: new ApiError(503, 'api_disabled', 'Logging API is disabled', 'enable logging.googleapis.com') } });
    expect(err).toContain('Logging API is disabled');
    expect(err).toContain('enable logging.googleapis.com');
    expect(err).not.toContain('data-testid="error-banner"');
    expect(one('needs_attention', {}, { [k]: { status: 'done', data: { ...errorSamplesFixture, scanned: 0, entries: [], top_paths: [] } } })).toContain('errors-sample-empty');
    expect(one('needs_attention', {}, { [k]: { status: 'done', data: { ...errorSamplesFixture, out_of_retention: true, retention_note: 'older than 30 days' } } })).toContain('Out of retention: older than 30 days');
    const done = one('needs_attention', {}, { [k]: { status: 'done', data: { ...errorSamplesFixture, focus: 'server' } } });
    for (const h of ['Time (UTC)', 'Method', 'Path', 'Status', 'Latency', 'Trace']) expect(done).toContain(`>${h}<`);
  });

  it('check-paths result shows scanner share and highlights non-scanner paths as possible broken links', () => {
    const k = sampleKey(0, 'notfound');
    const html = one('noise', { c5xx: 0 }, { [k]: { status: 'done', data: { ...errorSamplesFixture, focus: 'notfound' } } });
    expect(html).toContain('90%');
    expect(html).toContain('look like scanner traffic');
    expect(html).toContain('Possible broken links');
    expect(html).toContain('data-testid="errors-broken-link"');
    expect(html).toContain('possible broken link');
    const clean = one(
      'noise',
      { c5xx: 0 },
      { [k]: { status: 'done', data: { ...errorSamplesFixture, focus: 'notfound', top_paths: errorSamplesFixture.top_paths.filter((p) => p.scanner_like) } } },
    );
    expect(clean).toContain('errors-no-broken-links');
    expect(clean).not.toContain('errors-broken-link"');
  });

  it('Details shows three titled sections each with its own state', () => {
    const samples: SampleMap = {
      [sampleKey(0, 'server')]: { status: 'loading' },
      [sampleKey(0, 'client')]: { status: 'done', data: { ...errorSamplesFixture, scanned: 0, entries: [], top_paths: [] } },
    };
    const html = one('needs_attention', {}, samples, new Set([0]));
    expect(html.match(/data-testid="errors-detail-section"/g)?.length).toBe(3);
    for (const t of ['Server errors (5xx)', 'Client errors that may be real', '404s: scanner-like vs possible broken links']) expect(html).toContain(t);
    expect(html).toContain('errors-sample-loading');
    expect(html).toContain('errors-sample-empty');
    expect(html).toContain('Not loaded yet.');
    expect(html).toContain('>Hide details<');
  });
});
