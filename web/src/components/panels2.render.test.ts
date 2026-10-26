import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { limitsFixture, monthTableFixture } from '@/lib/fixtures2';
import { LimitsView } from './LimitsPanel';
import { MonthProvenance, MonthView } from './MonthPanel';

const noop = () => undefined;

function month(overrides: Partial<Parameters<typeof MonthView>[0]> = {}) {
  return renderToStaticMarkup(
    createElement(MonthView, { data: monthTableFixture, selection: null, onCellClick: noop, onRefreshMonth: noop, refreshDisabled: false, ...overrides }),
  );
}

describe('MonthView', () => {
  it('renders rows in column order with badges and the summary line', () => {
    const html = month();
    expect(html).toContain('Current month net 812.40 · target 2,500.00 · projected 2,210.90');
    const heads = ['Month', 'Compute Engine', 'Networking', 'Total GCP', '% change'].map((h) => html.indexOf(`>${h}<`));
    expect(heads.every((i) => i > 0)).toBe(true);
    expect([...heads].sort((a, b) => a - b)).toEqual(heads);
    expect(html).toContain('month-row-2026-09');
    expect(html).toContain('>final<');
    expect(html).toContain('>partial<');
    expect(html).toContain('812.40');
    expect(html).toContain('−42.0 %');
  });
  it('shows the empty state and dash target when nothing is fetched', () => {
    const html = month({ data: { ...monthTableFixture, summary: { ...monthTableFixture.summary, has_data: false, target: null, net: 0, projected: 0 } } });
    expect(html).toContain('Refresh the month to fetch');
    expect(html).toContain('target —');
  });
  it('renders unfetched months with a refresh link', () => {
    const rows = [{ ...monthTableFixture.rows[0]!, snapshot: null, cells: null, pct_change: null }];
    const html = month({ data: { ...monthTableFixture, rows } });
    expect(html).toContain('month-not-fetched-2026-09');
    expect(html).toContain('not fetched');
  });
});

describe('MonthProvenance', () => {
  it('shows snapshot facts and a loading hint before the run arrives', () => {
    const row = monthTableFixture.rows[1]!;
    const column = monthTableFixture.columns[3]!;
    const html = renderToStaticMarkup(createElement(MonthProvenance, { selection: { row, column }, run: null, error: null, onClose: noop }));
    expect(html).toContain('Snapshot');
    expect(html).toContain('#4');
    expect(html).toContain('202610');
    expect(html).toContain('Loading query');
  });
});

function limits(props: Partial<Parameters<typeof LimitsView>[0]> = {}) {
  return renderToStaticMarkup(createElement(LimitsView, { data: limitsFixture, onSave: async () => undefined, ...props }));
}

describe('LimitsView', () => {
  it('renders one row per allowance with level badges', () => {
    const html = limits();
    for (const l of limitsFixture.limits) expect(html).toContain(`limit-row-${l.id}`);
    expect(html).toMatch(/data-level="ok"/);
    expect(html).toMatch(/data-level="over"/);
    expect(html).toContain('data-level="no data"');
    expect(html).toContain('data-level="stub"');
    expect(html).toContain('5,000');
    expect(html).toContain('102.6 %');
  });
  it('stub rows show their reason and have no edit control', () => {
    const html = limits();
    expect(html).toContain('Groq usage arrives in Phase 4');
    expect(html).not.toContain('limit-edit-groq_llama4_scout_rpm');
    expect(html).toContain('limit-edit-logging_gb');
  });
  it('renders the inline server error under the edited row', () => {
    const html = limits({
      initialEdit: { id: 'logging_gb', draft: '-5', error: { code: 'invalid_limit', message: 'limit.logging_gb must be >= 0', hint: 'Use a non-negative number.' } },
    });
    expect(html).toContain('limit-input-logging_gb');
    expect(html).toContain('invalid_limit — limit.logging_gb must be &gt;= 0 Use a non-negative number.');
  });
  it('renders the warn-percent error', () => {
    const html = limits({ initialWarn: { draft: '0', error: { code: 'invalid_limit', message: 'warn_pct out of range', hint: null } } });
    expect(html).toContain('limits-warn-error');
    expect(html).toContain('warn_pct out of range');
  });
});
