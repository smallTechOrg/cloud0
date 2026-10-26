// ISO-week helpers for the UI (UTC, Monday–Sunday). The server is the source
// of truth for statuses in API responses; these helpers label picker options
// and fill in rows when the table endpoint is unavailable.

import type { WeekID, WeekStatus } from './types';

export const WEEK_ID_PATTERN = /^(\d{4})-W(\d{2})$/;

const DAY_MS = 86_400_000;
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'] as const;

/** Number of ISO weeks in a year: 53 when Jan 1 is a Thursday (or a Wednesday in a leap year). */
export function isoWeeksInYear(year: number): 52 | 53 {
  const jan1 = new Date(Date.UTC(year, 0, 1)).getUTCDay();
  const leap = (year % 4 === 0 && year % 100 !== 0) || year % 400 === 0;
  return jan1 === 4 || (leap && jan1 === 3) ? 53 : 52;
}

/** True when s has the YYYY-Www shape and the week exists in that ISO year. */
export function isWeekId(s: string): boolean {
  const m = WEEK_ID_PATTERN.exec(s);
  if (!m) return false;
  const year = Number(m[1]);
  const week = Number(m[2]);
  return week >= 1 && week <= isoWeeksInYear(year);
}

/** Splits a valid week id into its numeric parts. */
export function parseWeekId(s: string): { year: number; week: number } {
  const m = WEEK_ID_PATTERN.exec(s);
  if (!m || !isWeekId(s)) throw new Error(`invalid week id: ${s}`);
  return { year: Number(m[1]), week: Number(m[2]) };
}

function formatWeekId(year: number, week: number): WeekID {
  return `${year}-W${String(week).padStart(2, '0')}`;
}

function isoDate(d: Date): string {
  return d.toISOString().slice(0, 10);
}

/** Monday 00:00 UTC of the week as a Date. */
export function weekStartDate(week: WeekID): Date {
  const { year, week: w } = parseWeekId(week);
  // Jan 4 is always in ISO week 1; step back to its Monday.
  const jan4 = new Date(Date.UTC(year, 0, 4));
  const dow = jan4.getUTCDay() || 7; // Sunday → 7
  const mondayW1 = new Date(jan4.getTime() - (dow - 1) * DAY_MS);
  return new Date(mondayW1.getTime() + (w - 1) * 7 * DAY_MS);
}

/** Inclusive Monday..Sunday ISO dates of the week. */
export function weekBounds(week: WeekID): { start: string; end: string } {
  const start = weekStartDate(week);
  const end = new Date(start.getTime() + 6 * DAY_MS);
  return { start: isoDate(start), end: isoDate(end) };
}

/** The ISO week containing a UTC instant. */
export function weekIdOf(instant: Date): WeekID {
  // Shift to the Thursday of the same ISO week; its calendar year is the ISO year.
  const d = new Date(Date.UTC(instant.getUTCFullYear(), instant.getUTCMonth(), instant.getUTCDate()));
  const dow = d.getUTCDay() || 7;
  d.setUTCDate(d.getUTCDate() + 4 - dow);
  const isoYear = d.getUTCFullYear();
  const yearStart = Date.UTC(isoYear, 0, 1);
  const week = Math.ceil(((d.getTime() - yearStart) / DAY_MS + 1) / 7);
  return formatWeekId(isoYear, week);
}

/** Every ISO week from `from` to `to` inclusive, ascending; empty when from > to. */
export function weekRange(from: WeekID, to: WeekID): WeekID[] {
  const out: WeekID[] = [];
  const end = weekStartDate(to).getTime();
  for (let t = weekStartDate(from).getTime(); t <= end; t += 7 * DAY_MS) {
    out.push(weekIdOf(new Date(t)));
  }
  return out;
}

/** "Oct 19–25" within a month, "Sep 28 – Oct 4" across months. */
export function formatDateRange(startIso: string, endIso: string): string {
  const s = new Date(`${startIso}T00:00:00Z`);
  const e = new Date(`${endIso}T00:00:00Z`);
  const sm = MONTHS[s.getUTCMonth()] ?? '';
  const em = MONTHS[e.getUTCMonth()] ?? '';
  if (sm === em && s.getUTCFullYear() === e.getUTCFullYear()) {
    return `${sm} ${s.getUTCDate()}–${e.getUTCDate()}`;
  }
  return `${sm} ${s.getUTCDate()} – ${em} ${e.getUTCDate()}`;
}

/** Short date range for a week id, computed client-side. */
export function weekDatesLabel(week: WeekID): string {
  const b = weekBounds(week);
  return formatDateRange(b.start, b.end);
}

/**
 * partial while now < week end; settling until settle_hours have elapsed;
 * final afterwards. Mirrors the server's isoweek.Status.
 */
export function weekStatus(week: WeekID, now: Date, settleHours: number): WeekStatus {
  const endExclusive = weekStartDate(week).getTime() + 7 * DAY_MS;
  const t = now.getTime();
  if (t < endExclusive) return 'partial';
  if (t < endExclusive + settleHours * 3_600_000) return 'settling';
  return 'final';
}

/** Badge/option text: a status, "stale — Refresh for final", or "not fetched". */
export function statusText(status: WeekStatus | null, stale: boolean): string {
  if (status === null) return 'not fetched';
  if (stale) return 'stale — Refresh for final';
  return status;
}

/** `2026-W43 · Oct 19–25 · settling` */
export function weekOptionLabel(week: WeekID, status: string): string {
  return `${week} · ${weekDatesLabel(week)} · ${status}`;
}
