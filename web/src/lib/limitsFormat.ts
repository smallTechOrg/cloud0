// Pure helpers for the Limits tab: level classification and colours, number
// formatting, input parsing, CSV (spec/capabilities/limits-warnings.md).

import { csvField, csvNumber } from './csv';
import type { LimitStatus, LimitStatusLevel, LimitsResponse } from './types';

const CRLF = '\r\n';

export type LevelTone = 'ok' | 'near' | 'over' | 'stub' | 'nodata';

export function levelTone(level: LimitStatusLevel): LevelTone {
  switch (level) {
    case 'ok':
      return 'ok';
    case 'near':
      return 'near';
    case 'over':
      return 'over';
    case 'stub':
      return 'stub';
    default:
      return 'nodata';
  }
}

export interface LevelColors {
  fg: string;
  bg: string;
  border: string;
}

/** Amber for near, red for over, nothing for ok; muted for stub / no data. Same palette as the Metrics tab markers. */
export const LEVEL_COLORS: Record<LevelTone, LevelColors | null> = {
  ok: null,
  near: { fg: '#92400e', bg: '#fef3c7', border: '#f59e0b' },
  over: { fg: '#b42318', bg: '#fee2e2', border: '#f87171' },
  stub: { fg: '#5c6878', bg: '#f3f4f6', border: '#d1d5db' },
  nodata: { fg: '#5c6878', bg: 'transparent', border: '#9ca3af' },
};

export function levelColors(level: LimitStatusLevel): LevelColors | null {
  return LEVEL_COLORS[levelTone(level)];
}

export function levelLabel(level: LimitStatusLevel): string {
  return level;
}

/** Counts print as integers with grouping; fractional quantities with up to 2 decimals; null as an em dash. */
export function formatQuantity(n: number | null): string {
  if (n === null) return '—';
  if (Number.isInteger(n)) return n.toLocaleString('en-US', { maximumFractionDigits: 0 });
  return n.toLocaleString('en-US', { minimumFractionDigits: 0, maximumFractionDigits: 2 });
}

/** `56.6 %`; null as an em dash. */
export function formatLimitPct(pct: number | null): string {
  if (pct === null) return '—';
  const r = (Math.sign(pct) * Math.round(Math.abs(pct) * 10)) / 10;
  return `${r.toFixed(1)} %`;
}

/** Bar fill width in percent, clamped to 0..100 (over-limit rows fill fully). */
export function barWidth(pct: number | null): number {
  if (pct === null || !Number.isFinite(pct)) return 0;
  return Math.min(100, Math.max(0, pct));
}

export type ParseResult = { ok: true; value: number } | { ok: false; error: string };

/** Allowance input: a finite number >= 0. */
export function parseAllowanceInput(text: string): ParseResult {
  const t = text.trim();
  if (t === '') return { ok: false, error: 'Enter a number.' };
  const v = Number(t);
  if (!Number.isFinite(v) || v < 0) return { ok: false, error: 'Allowance must be a finite number ≥ 0.' };
  return { ok: true, value: v };
}

/** Warn percent: a finite number in (0, 100]. */
export function parseWarnPctInput(text: string): ParseResult {
  const t = text.trim();
  if (t === '') return { ok: false, error: 'Enter a number.' };
  const v = Number(t);
  if (!Number.isFinite(v) || v <= 0 || v > 100) return { ok: false, error: 'Warn percent must be greater than 0 and at most 100.' };
  return { ok: true, value: v };
}

export const LIMITS_CSV_HEADER = [
  'Service',
  'Limit',
  'Unit',
  'Used (month to date)',
  'Projected (month end)',
  'Allowance',
  '% of allowance',
  'Level',
  'This week',
] as const;

function num(n: number | null): string {
  return n === null ? '' : csvNumber(n);
}

function row(l: LimitStatus): string {
  return [
    l.service,
    l.label,
    l.unit,
    num(l.used),
    num(l.projected),
    csvNumber(l.allowance),
    num(l.pct),
    l.level,
    num(l.week_used),
  ]
    .map(csvField)
    .join(',');
}

/** One line per allowance (stubs included, numbers empty), CRLF, no BOM. */
export function buildLimitsCsv(res: LimitsResponse): string {
  const lines = [LIMITS_CSV_HEADER.map(csvField).join(','), ...res.limits.map(row)];
  return lines.join(CRLF) + CRLF;
}

export function limitsCsvFilename(week: string): string {
  return `cloud0-limits-${week.replace(/[^A-Za-z0-9._-]/g, '_')}.csv`;
}
