import { describe, expect, it } from 'vitest';
import { buildDailyCsv, maxStack, niceScale, seriesColors, stackDay } from './dailyChart';
import type { DailyResponse } from './types';

const data: DailyResponse = {
  from: '2026-10-12', to: '2026-10-14', scope: 'all', by: 'product', top: 8, generated_at: '2026-10-26T09:00:00Z', total: 270, notes: [],
  days: [
    { date: '2026-10-12', week: '2026-W42', week_status: 'final', snapshot_id: 1, has_daily: true, total: 100 },
    { date: '2026-10-13', week: '2026-W42', week_status: 'final', snapshot_id: 1, has_daily: true, total: 110 },
    { date: '2026-10-14', week: '2026-W43', week_status: 'final', snapshot_id: null, has_daily: false, total: null },
  ],
  series: [
    { key: 'alpha', label: 'alpha', kind: 'group', total: 150, values: [100, 50, null] },
    { key: 'beta, inc', label: 'beta, inc', kind: 'group', total: 70, values: [0, 70, null] },
    { key: '(other)', label: 'Other', kind: 'other', total: -10, values: [0, -10, null] },
    { key: '(shared)', label: '(shared)', kind: 'group', total: 0, values: [0, 0, null] },
  ],
};

describe('daily chart helpers', () => {
  it('colors named series in slot order and residue buckets gray', () => {
    const c = seriesColors(data.series);
    expect(c.get('alpha')).toBe('#2a78d6');
    expect(c.get('beta, inc')).toBe('#eb6834');
    expect(c.get('(other)')).toBe('#8a929c');
    expect(c.get('(shared)')).toBe('#a9b0b9');
  });

  it('stacks positive values only and finds the tallest day', () => {
    expect(stackDay(data.series, 1)).toEqual([
      { key: 'alpha', y0: 0, y1: 50 },
      { key: 'beta, inc', y0: 50, y1: 120 },
    ]);
    expect(stackDay(data.series, 2)).toEqual([]);
    expect(maxStack(data)).toBe(120);
  });

  it('picks nice ticks', () => {
    expect(niceScale(120)).toEqual({ top: 150, step: 50 });
    expect(niceScale(0)).toEqual({ top: 1, step: 0.25 });
    expect(niceScale(9.3)).toEqual({ top: 10, step: 2.5 });
  });

  it('builds a CSV with quoted labels and empty no-data cells', () => {
    const lines = buildDailyCsv(data).split('\r\n');
    expect(lines[0]).toBe('Date,alpha,"beta, inc",Other,(shared),Total');
    expect(lines[2]).toBe('2026-10-13,50.00,70.00,-10.00,0.00,110.00');
    expect(lines[3]).toBe('2026-10-14,,,,,');
  });
});
