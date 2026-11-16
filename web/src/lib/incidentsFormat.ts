// Pure, Node-safe helpers for the Incidents tab (durations, relative times with
// an injected clock, snooze requests, CSV/markdown builders).

import { csvField } from './csv';
import type { AlertHistoryResponse, AlertHistoryRow, AlertSeverity, Incident, PreviewRequest, Snooze, WeekID } from './types';

export const SNOOZE_PRESETS: { label: string; seconds: number }[] = [
  { label: '1 hour', seconds: 3600 },
  { label: '4 hours', seconds: 4 * 3600 },
  { label: '24 hours', seconds: 24 * 3600 },
];
export const SNOOZE_MIN_SECONDS = 300;
export const SNOOZE_MAX_SECONDS = 7 * 24 * 3600;

function parse(ts: string | null): number | null {
  if (ts === null) return null;
  const t = Date.parse(ts);
  return Number.isNaN(t) ? null : t;
}

/** `45 s`, `12 min`, `2 h 05 min`, `3 d 4 h`. Negative / non-finite → `—`. */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—';
  const s = Math.floor(seconds);
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 24) {
    const rem = m % 60;
    return rem === 0 ? `${h} h` : `${h} h ${String(rem).padStart(2, '0')} min`;
  }
  const d = Math.floor(h / 24);
  const hr = h % 24;
  return hr === 0 ? `${d} d` : `${d} d ${hr} h`;
}

/** Seconds between two timestamps (`end` null → `now`); null when unparsable. */
export function durationSeconds(start: string, end: string | null, now: Date): number | null {
  const a = parse(start);
  const b = end === null ? now.getTime() : parse(end);
  if (a === null || b === null) return null;
  return Math.max(0, Math.round((b - a) / 1000));
}

/** `5 min ago` / `in 2 h` / `just now`; `now` is injected. */
export function relativeTime(ts: string | null, now: Date): string {
  const t = parse(ts);
  if (t === null) return '—';
  const diff = Math.round((now.getTime() - t) / 1000);
  if (Math.abs(diff) < 45) return 'just now';
  const text = formatDuration(Math.abs(diff));
  return diff >= 0 ? `${text} ago` : `in ${text}`;
}

export function severityTone(s: AlertSeverity | ''): 'critical' | 'error' | 'warning' | 'none' {
  switch (s) {
    case 'CRITICAL':
      return 'critical';
    case 'ERROR':
      return 'error';
    case 'WARNING':
      return 'warning';
    default:
      return 'none';
  }
}

export function severityLabel(s: AlertSeverity | ''): string {
  return s === '' ? 'Unset' : s.charAt(0) + s.slice(1).toLowerCase();
}

const SEV_RANK: Record<string, number> = { CRITICAL: 3, ERROR: 2, WARNING: 1, '': 0 };
export function worstSeverity(rows: { severity: AlertSeverity | '' }[]): AlertSeverity | '' {
  let worst: AlertSeverity | '' = '';
  for (const r of rows) if ((SEV_RANK[r.severity] ?? 0) > (SEV_RANK[worst] ?? 0)) worst = r.severity;
  return worst;
}

/** Sorted by opened desc, then display name (the server order; kept for safety). */
export function sortHistoryRows(rows: AlertHistoryRow[]): AlertHistoryRow[] {
  return [...rows].sort((a, b) => b.opened - a.opened || a.policy_display_name.localeCompare(b.policy_display_name));
}

/** Date part (`YYYY-MM-DD`) of a timestamp, or the input when unparsable. */
export function dateOnly(ts: string): string {
  const t = parse(ts);
  return t === null ? ts : new Date(t).toISOString().slice(0, 10);
}

/** `custom hours` text → seconds within 5 min … 7 d, or an error message. */
export function parseCustomHours(text: string): { ok: true; seconds: number } | { ok: false; error: string } {
  const t = text.trim();
  if (t === '' || !/^\d+(\.\d+)?$/.test(t)) return { ok: false, error: 'Enter a number of hours, e.g. 2 or 0.5.' };
  const seconds = Math.round(Number(t) * 3600);
  if (seconds < SNOOZE_MIN_SECONDS) return { ok: false, error: 'Minimum snooze is 5 minutes (0.1 h).' };
  if (seconds > SNOOZE_MAX_SECONDS) return { ok: false, error: 'Maximum snooze is 7 days (168 h).' };
  return { ok: true, seconds };
}

/** Drawer request that snoozes the policy behind `incident`. */
export function snoozeRequestForIncident(incident: Incident, seconds: number): PreviewRequest {
  return {
    kind: 'snooze',
    action: 'create',
    target: '',
    project: incident.project,
    params: {
      project: incident.project,
      display_name: `cloud0: snooze ${incident.policy_display_name}`.slice(0, 120),
      policy_names: [incident.policy_name],
      duration_seconds: seconds,
    },
  };
}

/** Drawer request that ends a snooze now. */
export function endSnoozeRequest(s: Snooze): PreviewRequest {
  return { kind: 'snooze', action: 'end', target: s.name, project: s.project };
}

export function snoozeCountdown(s: Snooze, now: Date): string {
  if (s.state === 'active') return `ends ${relativeTime(s.end_time, now)}`;
  if (s.state === 'scheduled') return `starts ${relativeTime(s.start_time, now)}`;
  return `ended ${relativeTime(s.end_time, now)}`;
}

/** Active first, then scheduled; expired are dropped. */
export function visibleSnoozes(list: Snooze[]): Snooze[] {
  const rank = { active: 0, scheduled: 1, expired: 2 } as const;
  return list.filter((s) => s.state !== 'expired').sort((a, b) => rank[a.state] - rank[b.state] || a.end_time.localeCompare(b.end_time));
}

// ---- History: table CSV and the weekly-review lines ----

export const HISTORY_CSV_HEADER = ['Policy', 'Project', 'Severity', 'Times fired', 'Still open', 'Resources', 'First opened', 'Last opened'];

export function buildHistoryCsv(h: AlertHistoryResponse): string {
  const lines = [HISTORY_CSV_HEADER.map(csvField).join(',')];
  for (const r of sortHistoryRows(h.rows)) {
    lines.push(
      [r.policy_display_name, r.project, r.severity, String(r.opened), String(r.still_open), r.resources.join('; '), r.first_opened_at, r.last_opened_at]
        .map(csvField)
        .join(','),
    );
  }
  return lines.join('\r\n') + '\r\n';
}

export function historyCsvFilename(week: WeekID): string {
  return `cloud0-alerts-${week}.csv`;
}

/** Sheet Incidents columns: Date, Incident, Details — one entry per policy row. */
export function reviewEntries(h: AlertHistoryResponse): { date: string; incident: string; details: string }[] {
  return sortHistoryRows(h.rows).map((r) => {
    const parts = [`${r.severity === '' ? 'severity unset' : r.severity.toLowerCase()}`, `${r.opened}× in ${h.week}`];
    if (r.still_open > 0) parts.push(`${r.still_open} still open`);
    if (r.resources.length > 0) parts.push(`on ${r.resources.join(', ')}`);
    return { date: dateOnly(r.first_opened_at), incident: r.policy_display_name, details: `${parts.join(', ')} (${r.project})` };
  });
}

/** Tab-free CSV (header + one line per policy) for pasting into the sheet. */
export function buildReviewCsv(h: AlertHistoryResponse): string {
  const lines = ['Date,Incident,Details'];
  for (const e of reviewEntries(h)) lines.push([e.date, e.incident, e.details].map(csvField).join(','));
  return lines.join('\n');
}

function mdCell(s: string): string {
  return s.replace(/\|/g, '\\|').replace(/\r?\n/g, ' ');
}

/** Markdown table with the sheet's Incidents columns. */
export function buildReviewMarkdown(h: AlertHistoryResponse): string {
  const lines = ['| Date | Incident | Details |', '| --- | --- | --- |'];
  for (const e of reviewEntries(h)) lines.push(`| ${mdCell(e.date)} | ${mdCell(e.incident)} | ${mdCell(e.details)} |`);
  return lines.join('\n');
}
