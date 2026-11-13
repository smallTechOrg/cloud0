import { describe, expect, it } from 'vitest';
import { alertHistoryFixture, incidentsFixture, snoozesFixture } from './fixtures3';
import {
  buildHistoryCsv,
  buildReviewCsv,
  buildReviewMarkdown,
  durationSeconds,
  endSnoozeRequest,
  formatDuration,
  parseCustomHours,
  relativeTime,
  reviewEntries,
  snoozeCountdown,
  snoozeRequestForIncident,
  sortHistoryRows,
  visibleSnoozes,
  worstSeverity,
} from './incidentsFormat';
import type { AlertHistoryResponse, Incident } from './types';

const NOW = new Date('2026-10-27T09:00:00Z');

describe('formatDuration', () => {
  it('formats units', () => {
    expect(formatDuration(0)).toBe('0 s');
    expect(formatDuration(59)).toBe('59 s');
    expect(formatDuration(600)).toBe('10 min');
    expect(formatDuration(3600)).toBe('1 h');
    expect(formatDuration(7500)).toBe('2 h 05 min');
    expect(formatDuration(90000)).toBe('1 d 1 h');
    expect(formatDuration(172800)).toBe('2 d');
  });
  it('guards bad input', () => {
    expect(formatDuration(-1)).toBe('—');
    expect(formatDuration(Number.NaN)).toBe('—');
  });
});

describe('durationSeconds / relativeTime', () => {
  it('uses the injected clock for open incidents', () => {
    expect(durationSeconds('2026-10-27T07:40:00Z', null, NOW)).toBe(4800);
    expect(durationSeconds('2026-10-27T07:40:00Z', '2026-10-27T07:50:00Z', NOW)).toBe(600);
    expect(durationSeconds('garbage', null, NOW)).toBeNull();
  });
  it('relative past, future and just now', () => {
    expect(relativeTime('2026-10-27T07:40:00Z', NOW)).toBe('1 h 20 min ago');
    expect(relativeTime('2026-10-27T11:00:00Z', NOW)).toBe('in 2 h');
    expect(relativeTime('2026-10-27T08:59:40Z', NOW)).toBe('just now');
    expect(relativeTime(null, NOW)).toBe('—');
    expect(relativeTime('nope', NOW)).toBe('—');
  });
});

describe('severity and sorting', () => {
  it('picks the worst severity', () => {
    expect(worstSeverity([{ severity: 'WARNING' }, { severity: 'CRITICAL' }, { severity: 'ERROR' }])).toBe('CRITICAL');
    expect(worstSeverity([])).toBe('');
  });
  it('sorts by count desc then name', () => {
    const base = alertHistoryFixture.rows[0]!;
    const rows = sortHistoryRows([
      { ...base, policy_display_name: 'b', opened: 1 },
      { ...base, policy_display_name: 'z', opened: 5 },
      { ...base, policy_display_name: 'a', opened: 1 },
    ]);
    expect(rows.map((r) => r.policy_display_name)).toEqual(['z', 'a', 'b']);
  });
});

describe('parseCustomHours', () => {
  it('accepts within range', () => {
    expect(parseCustomHours('2')).toEqual({ ok: true, seconds: 7200 });
    expect(parseCustomHours(' 0.5 ')).toEqual({ ok: true, seconds: 1800 });
    expect(parseCustomHours('168')).toEqual({ ok: true, seconds: 604800 });
  });
  it('rejects out of range and junk', () => {
    expect(parseCustomHours('0.01').ok).toBe(false);
    expect(parseCustomHours('169').ok).toBe(false);
    expect(parseCustomHours('abc').ok).toBe(false);
    expect(parseCustomHours('').ok).toBe(false);
    expect(parseCustomHours('-1').ok).toBe(false);
  });
});

describe('snooze requests', () => {
  it('builds a create request for the incident policy', () => {
    const inc = incidentsFixture.incidents[0] as Incident;
    const req = snoozeRequestForIncident(inc, 3600);
    expect(req).toMatchObject({ kind: 'snooze', action: 'create', target: '', project: 'proj-a' });
    expect(req.params).toMatchObject({ policy_names: [inc.policy_name], duration_seconds: 3600, project: 'proj-a' });
  });
  it('builds an end request and countdown', () => {
    const s = snoozesFixture.snoozes[0]!;
    expect(endSnoozeRequest(s)).toEqual({ kind: 'snooze', action: 'end', target: s.name, project: s.project });
    expect(snoozeCountdown(s, NOW)).toBe('ends in 1 h');
    expect(snoozeCountdown({ ...s, state: 'scheduled', start_time: '2026-10-27T10:00:00Z' }, NOW)).toBe('starts in 1 h');
  });
  it('hides expired and puts active first', () => {
    const s = snoozesFixture.snoozes[0]!;
    const list = visibleSnoozes([{ ...s, name: 'x', state: 'expired' }, { ...s, name: 'y', state: 'scheduled' }, { ...s, name: 'z', state: 'active' }]);
    expect(list.map((x) => x.name)).toEqual(['z', 'y']);
  });
});

describe('CSV and markdown builders', () => {
  const h: AlertHistoryResponse = {
    ...alertHistoryFixture,
    rows: [{ ...alertHistoryFixture.rows[0]!, policy_display_name: 'Policy, "quoted" | pipe', resources: ['vm-a', 'vm-b'] }],
  };
  it('quotes the table CSV', () => {
    const csv = buildHistoryCsv(h);
    const lines = csv.trimEnd().split('\r\n');
    expect(lines[0]).toBe('Policy,Project,Severity,Times fired,Still open,Resources,First opened,Last opened');
    expect(lines[1]).toContain('"Policy, ""quoted"" | pipe"');
    expect(lines[1]).toContain('vm-a; vm-b');
  });
  it('builds sheet-shaped review entries', () => {
    const e = reviewEntries(h)[0]!;
    expect(e.date).toBe('2026-10-20');
    expect(e.details).toBe('warning, 3× in 2026-W43, 1 still open, on vm-a, vm-b (proj-a)');
  });
  it('review CSV has Date,Incident,Details', () => {
    const lines = buildReviewCsv(h).split('\n');
    expect(lines[0]).toBe('Date,Incident,Details');
    expect(lines[1]).toMatch(/^2026-10-20,"Policy, ""quoted"" \| pipe","warning, 3×/);
  });
  it('review markdown escapes pipes', () => {
    const md = buildReviewMarkdown(h).split('\n');
    expect(md[0]).toBe('| Date | Incident | Details |');
    expect(md[2]).toContain('Policy, "quoted" \\| pipe');
  });
  it('empty history yields header only', () => {
    const e = { ...h, rows: [] };
    expect(buildReviewCsv(e)).toBe('Date,Incident,Details');
    expect(buildHistoryCsv(e).trimEnd().split('\r\n')).toHaveLength(1);
  });
});
