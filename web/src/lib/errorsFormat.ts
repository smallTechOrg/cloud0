// Pure helpers for the Errors tab's explainability view.

import type { ErrorChange, ErrorSamplesResponse, ErrorTableRow, ErrorVerdict, SampleFocus, SamplePathStat } from './types';

export interface CodeChip {
  code: string;
  count: number;
  /** 5xx are the application-failure signal and are emphasised. */
  kind: '4xx' | '5xx';
}

/** Error codes (>= 400) of a by_code map: 5xx first, then by count desc, then code. */
export function errorCodeChips(byCode: Record<string, number> | null): CodeChip[] {
  if (byCode === null) return [];
  const chips: CodeChip[] = [];
  for (const [code, count] of Object.entries(byCode)) {
    const n = Number(code);
    if (!Number.isFinite(n) || n < 400 || n > 599 || count <= 0) continue;
    chips.push({ code, count, kind: n >= 500 ? '5xx' : '4xx' });
  }
  return chips.sort((a, b) => (a.kind === b.kind ? 0 : a.kind === '5xx' ? -1 : 1) || b.count - a.count || a.code.localeCompare(b.code));
}

/** "93%" from a 0..1 share; null stays null. */
export function formatShare(share: number | null): string | null {
  return share === null ? null : `${Math.round(share * 100)}%`;
}

/** True when the sample came back with nothing to show (in retention, zero entries). */
export function sampleIsEmpty(s: ErrorSamplesResponse): boolean {
  return !s.out_of_retention && (s.scanned === 0 || (s.entries.length === 0 && s.top_paths.length === 0));
}

// ---- Triage ------------------------------------------------------------------

export type BadgeTone = 'attention' | 'watch' | 'noise' | 'healthy';

export const VERDICT_LABELS: Record<ErrorVerdict, string> = {
  needs_attention: 'Needs attention',
  watch: 'Watch',
  noise: 'Probably noise',
  healthy: 'Healthy',
};

/** Text label + tone for a verdict badge; the label always carries the meaning, colour is extra. */
export function verdictBadge(v: ErrorVerdict): { label: string; tone: BadgeTone } {
  const tone: Record<ErrorVerdict, BadgeTone> = { needs_attention: 'attention', watch: 'watch', noise: 'noise', healthy: 'healthy' };
  return { label: VERDICT_LABELS[v], tone: tone[v] };
}

export interface IndexedRow {
  row: ErrorTableRow;
  /** Index into the response's `rows`. */
  index: number;
}

export interface TriageGroups {
  attention: IndexedRow[];
  watch: IndexedRow[];
  noise: IndexedRow[];
  healthy: IndexedRow[];
}

function byRank(a: IndexedRow, b: IndexedRow): number {
  // rank 1 first; rank 0 / missing sort last (healthy rows carry rank 0).
  const ra = a.row.rank && a.row.rank > 0 ? a.row.rank : Number.MAX_SAFE_INTEGER;
  const rb = b.row.rank && b.row.rank > 0 ? b.row.rank : Number.MAX_SAFE_INTEGER;
  return ra - rb || (b.row.c5xx ?? 0) - (a.row.c5xx ?? 0) || a.row.service.localeCompare(b.row.service) || a.index - b.index;
}

/** Groups verdict-bearing rows (totals and gap-only rows carry none) and orders each group by rank. */
export function triageGroups(rows: readonly ErrorTableRow[]): TriageGroups {
  const g: TriageGroups = { attention: [], watch: [], noise: [], healthy: [] };
  rows.forEach((row, index) => {
    switch (row.verdict) {
      case 'needs_attention':
        g.attention.push({ row, index });
        break;
      case 'watch':
        g.watch.push({ row, index });
        break;
      case 'noise':
        g.noise.push({ row, index });
        break;
      case 'healthy':
        g.healthy.push({ row, index });
        break;
      default:
    }
  });
  for (const list of Object.values(g)) list.sort(byRank);
  return g;
}

/** "W41" from "2026-W41". */
export function shortWeek(week: string): string {
  const m = /W\d{1,2}$/.exec(week);
  return m ? m[0] : week;
}

/** "3×", "1.5×": whole numbers without a decimal. */
export function formatRatio(r: number): string {
  const rounded = r >= 10 ? Math.round(r) : Math.round(r * 10) / 10;
  return `${Number.isInteger(rounded) ? rounded : rounded.toFixed(1)}×`;
}

/** Week-over-week phrase for a row, e.g. "up 3× vs W41"; null when there is nothing to compare. */
export function changeText(row: Pick<ErrorTableRow, 'c5xx' | 'c4xx' | 'change' | 'by_code'>): string | null {
  const ch: ErrorChange | null | undefined = row.change;
  if (!ch) return null;
  const prev = shortWeek(ch.prev_week);
  const now5 = row.c5xx ?? 0;
  if (ch.ratio_5xx !== null) {
    if (ch.ratio_5xx >= 1.5) return `up ${formatRatio(ch.ratio_5xx)} vs ${prev}`;
    if (ch.ratio_5xx <= 2 / 3) return now5 === 0 ? `none vs ${ch.prev_5xx.toLocaleString('en-US')} in ${prev}` : `down to ${formatRatio(ch.ratio_5xx)} of ${prev}`;
    return `about the same as ${prev}`;
  }
  if (now5 > 0 && ch.prev_5xx === 0) return `new: no 5xx in ${prev}`;
  if (now5 === 0 && ch.prev_5xx > 0) return `none vs ${ch.prev_5xx.toLocaleString('en-US')} in ${prev}`;
  // No server errors on either side: describe client errors instead.
  const clientNow = clientErrorCount(row);
  if (ch.prev_client_4xx > 0 && clientNow > 0) {
    const r = clientNow / ch.prev_client_4xx;
    if (r >= 1.5) return `client errors up ${formatRatio(r)} vs ${prev}`;
    if (r <= 2 / 3) return `client errors down vs ${prev}`;
    return `about the same as ${prev}`;
  }
  if (ch.prev_client_4xx === 0 && clientNow > 0) return `new: no client errors in ${prev}`;
  return null;
}

/** 4xx other than 404 from by_code when available; falls back to the whole 4xx count. */
function clientErrorCount(row: Pick<ErrorTableRow, 'c4xx' | 'by_code'>): number {
  if (row.by_code) {
    let n = 0;
    for (const [code, count] of Object.entries(row.by_code)) if (code !== '404' && Number(code) >= 400 && Number(code) < 500) n += count;
    return n;
  }
  return row.c4xx ?? 0;
}

/** Which sample answers "show me the failing requests" for a triage row. */
export function failingFocus(row: Pick<ErrorTableRow, 'verdict' | 'c5xx'>): SampleFocus {
  if (row.verdict === 'noise') return 'notfound';
  if (row.verdict === 'watch' && (row.c5xx ?? 0) === 0) return 'client';
  return 'server';
}

export const FOCUS_SECTIONS: readonly { focus: Exclude<SampleFocus, 'all'>; title: string; empty: string }[] = [
  { focus: 'server', title: 'Server errors (5xx)', empty: 'No server errors in the sampled logs.' },
  { focus: 'client', title: 'Client errors that may be real', empty: 'No 400/401/403/405/422/429 responses in the sampled logs.' },
  { focus: 'notfound', title: '404s: scanner-like vs possible broken links', empty: 'No 404s in the sampled logs.' },
];

/** Splits 404 paths into probable scanner probes and paths that might be real pages. */
export function splitNotFoundPaths(paths: readonly SamplePathStat[]): { scanner: SamplePathStat[]; possibleBroken: SamplePathStat[] } {
  return { scanner: paths.filter((p) => p.scanner_like), possibleBroken: paths.filter((p) => !p.scanner_like) };
}

/** True when a Cloud Run row predates exact status codes (its detail needs a fresh refresh). */
export function needsCodeRefresh(rows: readonly ErrorTableRow[]): boolean {
  return rows.some((r) => r.source === 'cloudrun' && r.service !== '' && r.gap === null && r.by_code === null);
}

/** "2026-10-18 23:59:01" from an RFC 3339 timestamp. */
export function formatEntryTime(ts: string): string {
  return ts.replace('T', ' ').replace(/(\.\d+)?Z$/, '');
}

/** True when the response has no triage items to show (green state). */
export function nothingToAttend(summary: { needs_attention: number; watch: number } | undefined): boolean {
  return !summary || summary.needs_attention + summary.watch === 0;
}
