import { describe, expect, it } from 'vitest';
import { limitsFixture } from './fixtures2';
import {
  barWidth,
  buildLimitsCsv,
  formatLimitPct,
  formatQuantity,
  levelColors,
  levelTone,
  limitsCsvFilename,
  parseAllowanceInput,
  parseWarnPctInput,
} from './limitsFormat';
import type { LimitsResponse } from './types';

describe('levelTone / levelColors', () => {
  it('classifies every level', () => {
    expect(levelTone('ok')).toBe('ok');
    expect(levelTone('near')).toBe('near');
    expect(levelTone('over')).toBe('over');
    expect(levelTone('stub')).toBe('stub');
    expect(levelTone('no data')).toBe('nodata');
  });
  it('ok has no colour, near is amber, over is red', () => {
    expect(levelColors('ok')).toBeNull();
    expect(levelColors('near')?.border).toBe('#f59e0b');
    expect(levelColors('over')?.fg).toBe('#b42318');
    expect(levelColors('stub')).not.toBeNull();
    expect(levelColors('no data')).not.toBeNull();
  });
});

describe('formatting', () => {
  it('formats quantities', () => {
    expect(formatQuantity(null)).toBe('—');
    expect(formatQuantity(5000)).toBe('5,000');
    expect(formatQuantity(28.3)).toBe('28.3');
    expect(formatQuantity(0.123456)).toBe('0.12');
    expect(formatQuantity(0)).toBe('0');
  });
  it('formats percentages and bar widths', () => {
    expect(formatLimitPct(null)).toBe('—');
    expect(formatLimitPct(56.64)).toBe('56.6 %');
    expect(formatLimitPct(102.6)).toBe('102.6 %');
    expect(barWidth(null)).toBe(0);
    expect(barWidth(56.6)).toBe(56.6);
    expect(barWidth(250)).toBe(100);
    expect(barWidth(-5)).toBe(0);
  });
});

describe('input parsing', () => {
  it('allowance: finite >= 0', () => {
    expect(parseAllowanceInput(' 75 ')).toEqual({ ok: true, value: 75 });
    expect(parseAllowanceInput('0')).toEqual({ ok: true, value: 0 });
    expect(parseAllowanceInput('').ok).toBe(false);
    expect(parseAllowanceInput('-1').ok).toBe(false);
    expect(parseAllowanceInput('abc').ok).toBe(false);
    expect(parseAllowanceInput('Infinity').ok).toBe(false);
  });
  it('warn percent: (0, 100]', () => {
    expect(parseWarnPctInput('90')).toEqual({ ok: true, value: 90 });
    expect(parseWarnPctInput('100')).toEqual({ ok: true, value: 100 });
    expect(parseWarnPctInput('0').ok).toBe(false);
    expect(parseWarnPctInput('100.1').ok).toBe(false);
    expect(parseWarnPctInput('').ok).toBe(false);
  });
});

describe('buildLimitsCsv', () => {
  it('writes header + one CRLF line per allowance, stubs with empty numbers', () => {
    const lines = buildLimitsCsv(limitsFixture).split('\r\n');
    expect(lines[0]).toBe('Service,Limit,Unit,Used (month to date),Projected (month end),Allowance,% of allowance,Level,This week');
    expect(lines[1]).toBe('Cloud Logging,Log ingestion,GB,21.50,28.30,50.00,56.60,ok,5.10');
    expect(lines[3]).toBe('Cloud Run,Egress,GB,,,1.00,,no data,');
    expect(lines[4]).toContain(',stub,');
    expect(lines[4]).toContain(',,,30.00,,stub,');
    expect(lines).toHaveLength(6); // header + 4 + trailing empty
    expect(lines[5]).toBe('');
  });
  it('quotes commas, quotes and newlines', () => {
    const res: LimitsResponse = {
      ...limitsFixture,
      limits: [
        { ...limitsFixture.limits[0]!, service: 'Svc, Inc', label: 'say "hi"\nthere' },
      ],
    };
    const csv = buildLimitsCsv(res);
    expect(csv).toContain('"Svc, Inc","say ""hi""\nthere",GB,');
  });
  it('builds a safe filename', () => {
    expect(limitsCsvFilename('2026-W43')).toBe('cloud0-limits-2026-W43.csv');
  });
});
