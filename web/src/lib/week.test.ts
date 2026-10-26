import { describe, expect, it } from 'vitest';
import {
  formatDateRange,
  isWeekId,
  isoWeeksInYear,
  parseWeekId,
  statusText,
  weekBounds,
  weekIdOf,
  weekOptionLabel,
  weekRange,
  weekStatus,
} from './week';

describe('isWeekId', () => {
  it('accepts well-formed ISO week ids', () => {
    expect(isWeekId('2026-W18')).toBe(true);
    expect(isWeekId('2026-W01')).toBe(true);
    expect(isWeekId('2026-W53')).toBe(true);
  });

  it('rejects malformed ids and out-of-range weeks', () => {
    for (const bad of ['', '2026-W0', '2026-W00', '2026-W54', '2026W18', '2026-w18', '26-W18', ' 2026-W18']) {
      expect(isWeekId(bad), bad).toBe(false);
    }
  });

  it('rejects W53 in a 52-week year', () => {
    expect(isWeekId('2027-W53')).toBe(false);
  });
});

describe('parseWeekId', () => {
  it('splits year and week', () => {
    expect(parseWeekId('2026-W18')).toEqual({ year: 2026, week: 18 });
  });

  it('throws on invalid input', () => {
    expect(() => parseWeekId('nope')).toThrow(/invalid week id/);
  });
});

describe('isoWeeksInYear', () => {
  it('knows 2026 has 53 weeks and 2027 has 52', () => {
    expect(isoWeeksInYear(2026)).toBe(53);
    expect(isoWeeksInYear(2027)).toBe(52);
    expect(isoWeeksInYear(2020)).toBe(53);
  });
});

describe('weekBounds', () => {
  it('returns Monday..Sunday UTC dates for 2026-W18', () => {
    const b = weekBounds('2026-W18');
    expect(b.start).toBe('2026-04-27');
    expect(b.end).toBe('2026-05-03');
  });

  it('handles week 1 starting in the previous calendar year', () => {
    expect(weekBounds('2026-W01').start).toBe('2025-12-29');
  });

  it('handles the 53rd week of 2026 running into 2027', () => {
    const b = weekBounds('2026-W53');
    expect(b.start).toBe('2026-12-28');
    expect(b.end).toBe('2027-01-03');
  });
});

describe('weekIdOf', () => {
  it('maps a UTC instant to its ISO week', () => {
    expect(weekIdOf(new Date('2026-10-26T00:00:00Z'))).toBe('2026-W44');
    expect(weekIdOf(new Date('2026-10-25T23:59:59Z'))).toBe('2026-W43');
    expect(weekIdOf(new Date('2027-01-01T12:00:00Z'))).toBe('2026-W53');
    expect(weekIdOf(new Date('2027-01-04T00:00:00Z'))).toBe('2027-W01');
  });
});

describe('weekRange', () => {
  it('enumerates ascending, inclusive', () => {
    expect(weekRange('2026-W42', '2026-W44')).toEqual(['2026-W42', '2026-W43', '2026-W44']);
  });

  it('crosses a year boundary with 53 weeks', () => {
    expect(weekRange('2026-W52', '2027-W02')).toEqual(['2026-W52', '2026-W53', '2027-W01', '2027-W02']);
  });

  it('returns a single week when from equals to and nothing when from > to', () => {
    expect(weekRange('2026-W18', '2026-W18')).toEqual(['2026-W18']);
    expect(weekRange('2026-W19', '2026-W18')).toEqual([]);
  });
});

describe('formatDateRange', () => {
  it('collapses the month when both dates share it', () => {
    expect(formatDateRange('2026-10-19', '2026-10-25')).toBe('Oct 19–25');
  });

  it('spells both months when the week spans two', () => {
    expect(formatDateRange('2026-09-28', '2026-10-04')).toBe('Sep 28 – Oct 4');
  });

  it('spans a year boundary without the year', () => {
    expect(formatDateRange('2026-12-28', '2027-01-03')).toBe('Dec 28 – Jan 3');
  });
});

describe('weekStatus', () => {
  const settle = 48;
  it('is partial before the week ends', () => {
    expect(weekStatus('2026-W43', new Date('2026-10-25T23:59:59Z'), settle)).toBe('partial');
  });
  it('is settling until settle_hours have elapsed', () => {
    expect(weekStatus('2026-W43', new Date('2026-10-26T00:00:00Z'), settle)).toBe('settling');
    expect(weekStatus('2026-W43', new Date('2026-10-27T23:00:00Z'), settle)).toBe('settling');
  });
  it('is final afterwards', () => {
    expect(weekStatus('2026-W43', new Date('2026-10-28T00:00:00Z'), settle)).toBe('final');
  });
});

describe('labels', () => {
  it('builds the picker option label', () => {
    expect(weekOptionLabel('2026-W43', 'settling')).toBe('2026-W43 · Oct 19–25 · settling');
    expect(weekOptionLabel('2026-W18', 'not fetched')).toBe('2026-W18 · Apr 27 – May 3 · not fetched');
  });

  it('describes statuses including stale and not fetched', () => {
    expect(statusText('final', false)).toBe('final');
    expect(statusText('final', true)).toBe('stale — Refresh for final');
    expect(statusText(null, false)).toBe('not fetched');
  });
});
