import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { alertHistoryFixture, incidentsFixture, snoozesFixture } from '@/lib/fixtures3';
import type { BannerData } from './ErrorBanner';
import { IncidentRow } from './IncidentRow';
import { ActiveCard, HistoryCard, SnoozesCard } from './IncidentsPanel';

const NOW = new Date('2026-10-27T09:00:00Z');
const noop = () => undefined;
const empty = { data: null, loading: false, banner: null };
const banner: BannerData = { key: 'incidents', context: 'Incidents', code: 'permission_denied', message: 'GCP 403', hint: 'Grant roles/monitoring.viewer to the cloud0-reader service account on proj-a' };

describe('ActiveCard', () => {
  it('renders an open incident with badge text, resource, times, link and Snooze action', () => {
    const html = renderToStaticMarkup(createElement(ActiveCard, { load: { ...empty, data: incidentsFixture }, now: NOW, onRefresh: noop, onSnooze: noop }));
    expect(html).toContain('Active now');
    expect(html).toContain('cloud0: VM CPU high');
    expect(html).toContain('>Warning<');
    expect(html).toContain('vm-a');
    expect(html).toContain('Started 1 h 20 min ago');
    expect(html).toContain('2026-10-27 07:40:00Z');
    expect(html).toContain('Open for 1 h 20 min');
    expect(html).toContain('href="https://console.cloud.google.com/monitoring/alerting/alerts/0abc?project=proj-a"');
    expect(html).toContain('rel="noopener noreferrer"');
    expect(html).toContain('Snooze…');
    expect(html).not.toContain('Nothing firing');
  });

  it('renders the green empty state with the last-checked time', () => {
    const html = renderToStaticMarkup(createElement(ActiveCard, { load: { ...empty, data: { ...incidentsFixture, incidents: [], total: 0 } }, now: NOW, onRefresh: noop, onSnooze: noop }));
    expect(html).toContain('Nothing firing');
    expect(html).toContain('Last checked 2026-10-27 09:00:00Z');
    expect(html).toContain('data-testid="incidents-refresh"');
    expect(html).not.toContain('incident-row');
  });

  it('renders the error with its hint and no empty state', () => {
    const html = renderToStaticMarkup(createElement(ActiveCard, { load: { ...empty, banner }, now: NOW, onRefresh: noop, onSnooze: noop }));
    expect(html).toContain('permission_denied');
    expect(html).toContain('Grant roles/monitoring.viewer');
    expect(html).not.toContain('Nothing firing');
  });

  it('renders loading and per-project failures', () => {
    const loading = renderToStaticMarkup(createElement(ActiveCard, { load: { data: null, loading: true, banner: null }, now: NOW, onRefresh: noop, onSnooze: noop }));
    expect(loading).toContain('aria-busy="true"');
    expect(loading).toContain('Refreshing…');
    const failed = renderToStaticMarkup(
      createElement(ActiveCard, {
        load: { ...empty, data: { ...incidentsFixture, projects_failed: [{ project: 'proj-b', code: 'api_disabled', message: 'off', hint: 'gcloud services enable monitoring.googleapis.com' }] } },
        now: NOW,
        onRefresh: noop,
        onSnooze: noop,
      }),
    );
    expect(failed).toContain('api_disabled');
    expect(failed).toContain('gcloud services enable monitoring.googleapis.com');
  });
});

describe('IncidentRow snooze picker', () => {
  it('shows 1 h / 4 h / 24 h / custom choices', () => {
    const html = renderToStaticMarkup(createElement(IncidentRow, { incident: incidentsFixture.incidents[0]!, now: NOW, onSnooze: noop, initialPicker: true }));
    for (const t of ['1 hour', '4 hours', '24 hours', 'Custom (up to 7 days)', 'Preview snooze']) expect(html).toContain(t);
  });
});

describe('HistoryCard', () => {
  it('renders the table, counts, and enabled copy/export buttons', () => {
    const html = renderToStaticMarkup(createElement(HistoryCard, { week: '2026-W43', load: { ...empty, data: alertHistoryFixture }, onCopy: noop, onExport: noop }));
    expect(html).toContain('history-table');
    expect(html).toContain('3 opened, 1 still open');
    expect(html).toContain('Copy for weekly review');
    expect(html).toContain('Export CSV');
    expect(html).not.toMatch(/data-testid="history-copy"[^>]*disabled/);
    expect(html).toContain('vm-a');
  });

  it('empty week disables actions and says so', () => {
    const html = renderToStaticMarkup(createElement(HistoryCard, { week: '2026-W42', load: { ...empty, data: { ...alertHistoryFixture, week: '2026-W42', rows: [], total_opened: 0, still_open: 0 } }, onCopy: noop, onExport: noop }));
    expect(html).toContain('No alerts opened in 2026-W42');
    expect(html).toMatch(/disabled=""[^>]*data-testid="history-copy"|data-testid="history-copy"[^>]*disabled/);
  });

  it('error state shows the banner hint', () => {
    const html = renderToStaticMarkup(createElement(HistoryCard, { week: '2026-W43', load: { ...empty, banner: { ...banner, code: 'api_disabled', hint: 'Enable monitoring.googleapis.com' } }, onCopy: noop, onExport: noop }));
    expect(html).toContain('Enable monitoring.googleapis.com');
    expect(html).not.toContain('history-table');
  });
});

describe('SnoozesCard', () => {
  it('lists active snoozes with an End action', () => {
    const html = renderToStaticMarkup(createElement(SnoozesCard, { load: { ...empty, data: snoozesFixture }, now: NOW, onEnd: noop }));
    expect(html).toContain('deploy window');
    expect(html).toContain('ends in 1 h');
    expect(html).toContain('End…');
  });
  it('empty list', () => {
    const html = renderToStaticMarkup(createElement(SnoozesCard, { load: { ...empty, data: { ...snoozesFixture, snoozes: [] } }, now: NOW, onEnd: noop }));
    expect(html).toContain('No active or upcoming snoozes');
  });
});
