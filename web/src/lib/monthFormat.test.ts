import { describe, expect, it } from 'vitest';
import { monthTableFixture, refreshAllFixture } from './fixtures2';
import { addMonths, buildMonthCsv, currentMonthId, formatSummaryLine, isMonthId, monthCsvFilename, monthWindow, refreshOutcome } from './monthFormat';
import type { MonthRow } from './types';

describe('month ids', () => {
  it('validates', () => {
    expect(isMonthId('2026-10')).toBe(true);
    expect(isMonthId('2026-13')).toBe(false);
    expect(isMonthId('2026-00')).toBe(false);
    expect(isMonthId('26-10')).toBe(false);
  });
  it('current month is UTC', () => {
    expect(currentMonthId(new Date('2026-10-31T23:59:59Z'))).toBe('2026-10');
    expect(currentMonthId(new Date('2026-11-01T00:00:00Z'))).toBe('2026-11');
  });
  it('shifts across year boundaries', () => {
    expect(addMonths('2026-02', -3)).toBe('2025-11');
    expect(addMonths('2026-11', 2)).toBe('2027-01');
    expect(addMonths('2026-10', 0)).toBe('2026-10');
    expect(monthWindow('2026-03')).toEqual({ from: '2025-10', to: '2026-03' });
  });
});

describe('formatSummaryLine', () => {
  it('formats net, target, projected', () => {
    expect(formatSummaryLine(monthTableFixture.summary)).toBe('Current month net 812.40 · target 2,500.00 · projected 2,210.90');
  });
  it('shows an em dash for an unset target', () => {
    expect(formatSummaryLine({ ...monthTableFixture.summary, target: null })).toContain('target — ·');
  });
  it('shows dashes for net/projected without data', () => {
    expect(formatSummaryLine({ ...monthTableFixture.summary, has_data: false, net: 0, projected: 0 })).toBe('Current month net — · target 2,500.00 · projected —');
  });
});

describe('buildMonthCsv', () => {
  it('follows the on-screen columns', () => {
    const lines = buildMonthCsv(monthTableFixture.columns, monthTableFixture.rows).split('\r\n');
    expect(lines[0]).toBe('Month,Compute Engine,Networking,Total GCP,% change');
    expect(lines[1]).toBe('2026-09,1100.00,300.00,1400.00,');
    expect(lines[2]).toBe('2026-10,780.00,32.40,812.40,-41.97');
  });
  it('leaves unfetched months empty and quotes labels', () => {
    const row: MonthRow = { ...monthTableFixture.rows[0]!, cells: null, snapshot: null, pct_change: null };
    const csv = buildMonthCsv([{ key: 'month', label: 'Month, UTC', kind: 'month' }, { key: 'total', label: 'Total "net"', kind: 'total' }], [row]);
    expect(csv).toBe('"Month, UTC","Total ""net"""\r\n2026-09,\r\n');
  });
  it('builds a safe filename', () => {
    expect(monthCsvFilename('proj/a b', '2026-05', '2026-10')).toBe('cloud0-month-proj_a_b-2026-05-2026-10.csv');
  });
});

describe('refreshOutcome', () => {
  it('flags a failed source with its hint', () => {
    const o = refreshOutcome(refreshAllFixture);
    expect(o.ok).toBe(false);
    expect(o.failure?.code).toBe('permission_denied');
    expect(o.failure?.hint).toContain('monitoring.viewer');
  });
  it('ok when every source ok', () => {
    const run = { ...refreshAllFixture.run, status: 'ok' as const, sources: refreshAllFixture.run.sources.slice(0, 1) };
    expect(refreshOutcome({ ...refreshAllFixture, run })).toEqual({ ok: true, failure: null });
  });
});
