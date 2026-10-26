import { describe, expect, it } from 'vitest';
import { isWeekId, parseWeekId } from './week';

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
});

describe('parseWeekId', () => {
  it('splits year and week', () => {
    expect(parseWeekId('2026-W18')).toEqual({ year: 2026, week: 18 });
  });

  it('throws on invalid input', () => {
    expect(() => parseWeekId('nope')).toThrow(/invalid week id/);
  });
});
