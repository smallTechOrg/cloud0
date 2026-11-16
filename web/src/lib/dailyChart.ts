// Pure helpers for the daily cost chart (GET /cost/daily): colors by role,
// stacked-bar geometry and the CSV of the table view. No DOM, unit-tested.

import type { DailyResponse, DailySeries } from './types';

/** Categorical slots in fixed order (validated reference palette, light mode). */
export const SERIES_COLORS = ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#6250d6', '#e34948'] as const;

/** Residue buckets are neutral, never a categorical hue. */
const NEUTRALS: Record<string, string> = {
  '(other)': '#8a929c',
  '(shared)': '#a9b0b9',
  '(unattributed)': '#c7ccd2',
  '(no environment)': '#c7ccd2',
};

/** Color per series key: residue buckets gray, the rest in slot order. */
export function seriesColors(series: readonly DailySeries[]): Map<string, string> {
  const out = new Map<string, string>();
  let slot = 0;
  for (const s of series) {
    const neutral = NEUTRALS[s.key];
    if (neutral) out.set(s.key, neutral);
    else out.set(s.key, SERIES_COLORS[slot++ % SERIES_COLORS.length]!);
  }
  return out;
}

export interface Segment {
  key: string;
  y0: number;
  y1: number;
}

/** Positive values stack upward from 0 in series order; negatives are not drawn (they stay in the tooltip/table). */
export function stackDay(series: readonly DailySeries[], dayIndex: number): Segment[] {
  const out: Segment[] = [];
  let y = 0;
  for (const s of series) {
    const v = s.values[dayIndex];
    if (v === null || v === undefined || v <= 0) continue;
    out.push({ key: s.key, y0: y, y1: y + v });
    y += v;
  }
  return out;
}

/** Largest stacked height across days (for the y scale). */
export function maxStack(data: DailyResponse): number {
  let max = 0;
  data.days.forEach((_, i) => {
    const segs = stackDay(data.series, i);
    if (segs.length) max = Math.max(max, segs[segs.length - 1]!.y1);
  });
  return max;
}

/** A "nice" axis top and tick step for max (1, 2, 2.5, 5 × 10^n). */
export function niceScale(max: number, ticks = 4): { top: number; step: number } {
  if (!(max > 0)) return { top: 1, step: 0.25 };
  const raw = max / ticks;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = ([1, 2, 2.5, 5, 10].find((m) => m * mag >= raw) ?? 10) * mag;
  return { top: Math.ceil(max / step) * step, step };
}

const csvField = (s: string) => (/[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s);
const num = (v: number | null | undefined) => (v === null || v === undefined ? '' : (Math.sign(v) * Math.round(Math.abs(v) * 100) / 100).toFixed(2));

/** Date × series CSV (RFC 4180, CRLF), empty cells for days without data. */
export function buildDailyCsv(data: DailyResponse): string {
  const lines = [['Date', ...data.series.map((s) => s.label), 'Total'].map(csvField).join(',')];
  data.days.forEach((d, i) => {
    lines.push([d.date, ...data.series.map((s) => num(s.values[i])), num(d.total)].map(csvField).join(','));
  });
  return lines.join('\r\n') + '\r\n';
}
