import { describe, expect, it } from 'vitest';
import { FOCUS_SECTIONS, changeText, errorCodeChips, failingFocus, formatRatio, formatShare, needsCodeRefresh, nothingToAttend, sampleIsEmpty, shortWeek, splitNotFoundPaths, triageGroups, verdictBadge } from './errorsFormat';
import { errorSamplesFixture, errorsTableFixture } from './fixtures2';
import type { ErrorTableRow } from './types';

describe('errorCodeChips', () => {
  it('lists only >=400 codes, 5xx first then by count', () => {
    const chips = errorCodeChips({ '200': 9000, '301': 5, '404': 1463, '401': 14, '502': 2, '500': 7, '403': 0 });
    expect(chips.map((c) => c.code)).toEqual(['500', '502', '404', '401']);
    expect(chips.map((c) => c.kind)).toEqual(['5xx', '5xx', '4xx', '4xx']);
  });
  it('is empty for null or no error codes', () => {
    expect(errorCodeChips(null)).toEqual([]);
    expect(errorCodeChips({ '200': 1 })).toEqual([]);
  });
});

describe('formatShare / sampleIsEmpty', () => {
  it('formats a share as a whole percent', () => {
    expect(formatShare(0.934)).toBe('93%');
    expect(formatShare(null)).toBeNull();
  });
  it('distinguishes empty from out-of-retention', () => {
    expect(sampleIsEmpty({ ...errorSamplesFixture, scanned: 0 })).toBe(true);
    expect(sampleIsEmpty({ ...errorSamplesFixture, scanned: 0, out_of_retention: true })).toBe(false);
    expect(sampleIsEmpty(errorSamplesFixture)).toBe(false);
  });
});

const baseRow = errorsTableFixture.rows[0] as ErrorTableRow;
const mk = (o: Partial<ErrorTableRow>): ErrorTableRow => ({ ...baseRow, ...o });

describe('triageGroups', () => {
  it('groups by verdict and orders each group by rank, ignoring verdict-less rows', () => {
    const rows = [
      mk({ service: 'w2', verdict: 'watch', rank: 4 }),
      mk({ service: 'a2', verdict: 'needs_attention', rank: 2 }),
      mk({ service: 'tot', verdict: undefined }),
      mk({ service: 'a1', verdict: 'needs_attention', rank: 1 }),
      mk({ service: 'w1', verdict: 'watch', rank: 3 }),
      mk({ service: 'n', verdict: 'noise', rank: 5 }),
      mk({ service: 'h-b', verdict: 'healthy', rank: 0, c5xx: 0 }),
      mk({ service: 'h-a', verdict: 'healthy', rank: 0, c5xx: 0 }),
    ];
    const g = triageGroups(rows);
    expect(g.attention.map((x) => x.row.service)).toEqual(['a1', 'a2']);
    expect(g.watch.map((x) => x.row.service)).toEqual(['w1', 'w2']);
    expect(g.noise.map((x) => x.row.service)).toEqual(['n']);
    expect(g.healthy.map((x) => x.row.service)).toEqual(['h-a', 'h-b']);
    expect(g.attention[0]?.index).toBe(3);
  });
  it('is empty for no rows and for rows without verdicts (old backend)', () => {
    expect(triageGroups([])).toEqual({ attention: [], watch: [], noise: [], healthy: [] });
    const g = triageGroups(errorsTableFixture.rows.map((r) => ({ ...r, verdict: undefined })));
    expect(g.attention.length + g.watch.length + g.noise.length + g.healthy.length).toBe(0);
  });
  it('splits the shared fixture 1/1/1/1', () => {
    const g = triageGroups(errorsTableFixture.rows);
    expect([g.attention.length, g.watch.length, g.noise.length, g.healthy.length]).toEqual([1, 1, 1, 1]);
  });
});

describe('verdictBadge', () => {
  it('always has a text label alongside the tone', () => {
    expect(verdictBadge('needs_attention')).toEqual({ label: 'Needs attention', tone: 'attention' });
    expect(verdictBadge('watch')).toEqual({ label: 'Watch', tone: 'watch' });
    expect(verdictBadge('noise')).toEqual({ label: 'Probably noise', tone: 'noise' });
    expect(verdictBadge('healthy')).toEqual({ label: 'Healthy', tone: 'healthy' });
  });
});

describe('changeText', () => {
  const ch = (o: Partial<NonNullable<ErrorTableRow['change']>>) => ({ prev_week: '2026-W41', prev_5xx: 1, prev_client_4xx: 0, prev_by_code: null, ratio_5xx: null, ...o });
  it('formats week and ratio', () => {
    expect(shortWeek('2026-W41')).toBe('W41');
    expect(formatRatio(3)).toBe('3×');
    expect(formatRatio(1.54)).toBe('1.5×');
    expect(formatRatio(12.4)).toBe('12×');
  });
  it('says up / down / same / new / none for 5xx', () => {
    expect(changeText({ c4xx: 0, c5xx: 3, by_code: null, change: ch({ ratio_5xx: 3 }) })).toBe('up 3× vs W41');
    expect(changeText({ c4xx: 0, c5xx: 1, by_code: null, change: ch({ prev_5xx: 4, ratio_5xx: 0.25 }) })).toBe('down to 0.3× of W41');
    expect(changeText({ c4xx: 0, c5xx: 1, by_code: null, change: ch({ ratio_5xx: 1.1 }) })).toBe('about the same as W41');
    expect(changeText({ c4xx: 0, c5xx: 5, by_code: null, change: ch({ prev_5xx: 0 }) })).toBe('new: no 5xx in W41');
    expect(changeText({ c4xx: 0, c5xx: 0, by_code: null, change: ch({ prev_5xx: 2, ratio_5xx: 0 }) })).toBe('none vs 2 in W41');
  });
  it('falls back to client errors when there are no 5xx', () => {
    expect(changeText({ c4xx: 45, c5xx: 0, by_code: { '401': 30, '404': 15 }, change: ch({ prev_5xx: 0, prev_client_4xx: 10 }) })).toBe('client errors up 3× vs W41');
    expect(changeText({ c4xx: 45, c5xx: 0, by_code: { '401': 30 }, change: ch({ prev_5xx: 0, prev_client_4xx: 0 }) })).toBe('new: no client errors in W41');
  });
  it('is null without a comparison or any change signal', () => {
    expect(changeText({ c4xx: 0, c5xx: 0, by_code: null, change: null })).toBeNull();
    expect(changeText({ c4xx: 0, c5xx: 0, by_code: null, change: undefined })).toBeNull();
    expect(changeText({ c4xx: 0, c5xx: 0, by_code: null, change: ch({ prev_5xx: 0 }) })).toBeNull();
  });
});

describe('failingFocus / sections / paths', () => {
  it('picks the focus a row needs', () => {
    expect(failingFocus({ verdict: 'needs_attention', c5xx: 3 })).toBe('server');
    expect(failingFocus({ verdict: 'watch', c5xx: 0 })).toBe('client');
    expect(failingFocus({ verdict: 'watch', c5xx: 2 })).toBe('server');
    expect(failingFocus({ verdict: 'noise', c5xx: 0 })).toBe('notfound');
  });
  it('has the three titled sections in order', () => {
    expect(FOCUS_SECTIONS.map((s) => s.title)).toEqual(['Server errors (5xx)', 'Client errors that may be real', '404s: scanner-like vs possible broken links']);
    expect(FOCUS_SECTIONS.map((s) => s.focus)).toEqual(['server', 'client', 'notfound']);
  });
  it('splits 404 paths into scanner-like and possible broken links', () => {
    const r = splitNotFoundPaths(errorSamplesFixture.top_paths);
    expect(r.scanner.map((p) => p.path)).toEqual(['/.git/config', '/.env']);
    expect(r.possibleBroken.map((p) => p.path)).toEqual(['/api/me']);
    expect(splitNotFoundPaths([])).toEqual({ scanner: [], possibleBroken: [] });
  });
});

describe('needsCodeRefresh / nothingToAttend / sampleIsEmpty with filters', () => {
  it('flags Cloud Run service rows without by_code only', () => {
    expect(needsCodeRefresh(errorsTableFixture.rows)).toBe(false);
    expect(needsCodeRefresh([mk({ by_code: null })])).toBe(true);
    expect(needsCodeRefresh([mk({ by_code: null, service: '' })])).toBe(false);
    expect(needsCodeRefresh([])).toBe(false);
  });
  it('detects an empty summary', () => {
    expect(nothingToAttend({ needs_attention: 0, watch: 0 })).toBe(true);
    expect(nothingToAttend({ needs_attention: 0, watch: 1 })).toBe(false);
  });
  it('treats a scanned sample with no matching entries as empty', () => {
    expect(sampleIsEmpty({ ...errorSamplesFixture, entries: [], top_paths: [] })).toBe(true);
  });
});
