// Pure helpers for the Rightsizing tab (spec/api.md → GET /rightsizing,
// spec/capabilities/rightsizing-hints.md). No React, no I/O.

import type { RightsizingCandidate, RightsizingKind, RightsizingResponse, RightsizingSource } from './attributionTypes';
import { csvField, csvNumber } from './csv';
import { formatMoney } from './format';
import { weekIdOf, weekStartDate } from './week';
import type { Scope, WeekID } from './types';

export const KIND_ORDER: RightsizingKind[] = ['low_cpu_vm', 'idle_disk', 'unused_ip', 'stale_snapshot', 'cloud_run_min_instances'];

export const KIND_LABELS: Record<RightsizingKind, string> = {
  low_cpu_vm: 'Low-CPU VM',
  idle_disk: 'Idle disk',
  unused_ip: 'Unused static IP',
  stale_snapshot: 'Stale snapshot',
  cloud_run_min_instances: 'Cloud Run idle minimum instance',
};

export function kindLabel(kind: string): string {
  return KIND_LABELS[kind as RightsizingKind] ?? kind;
}

export const CONFIDENCE_LABELS = { high: 'High confidence', medium: 'Medium confidence', low: 'Low confidence' } as const;

export const SOURCE_LABELS: Record<string, string> = {
  monitoring_vm: 'VM CPU metrics',
  compute_inventory: 'Compute inventory (disks, IPs, snapshots)',
};

export function sourceLabel(source: string): string {
  return SOURCE_LABELS[source] ?? source;
}

/** Descending by saving (null last), then id — the order the API promises, re-applied defensively. */
export function sortCandidates(list: readonly RightsizingCandidate[]): RightsizingCandidate[] {
  return [...list].sort((a, b) => {
    const sa = a.estimated_monthly_saving;
    const sb = b.estimated_monthly_saving;
    if (sa === null && sb !== null) return 1;
    if (sa !== null && sb === null) return -1;
    if (sa !== null && sb !== null && sa !== sb) return sb - sa;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
}

export function filterCandidates(list: readonly RightsizingCandidate[], kind: string, project: string): RightsizingCandidate[] {
  return sortCandidates(list.filter((c) => (kind === '' || c.kind === kind) && (project === '' || c.project_id === project)));
}

export interface SavingTotal {
  /** Sum of the known estimates (null savings count as unknown, never as 0 saved). */
  total: number;
  count: number;
  /** Candidates with no estimate (no billing row matched). */
  unknown: number;
}

export function totalSaving(list: readonly RightsizingCandidate[]): SavingTotal {
  let total = 0;
  let unknown = 0;
  for (const c of list) {
    if (c.estimated_monthly_saving === null) unknown += 1;
    else total += c.estimated_monthly_saving;
  }
  return { total: Math.round(total * 100) / 100, count: list.length, unknown };
}

export function projectsOf(list: readonly RightsizingCandidate[]): string[] {
  return [...new Set(list.map((c) => c.project_id))].sort();
}

/** Kinds present in the data, in display order, with counts. */
export function kindCounts(list: readonly RightsizingCandidate[]): { kind: RightsizingKind; count: number }[] {
  return KIND_ORDER.map((kind) => ({ kind, count: list.filter((c) => c.kind === kind).length })).filter((k) => k.count > 0);
}

export function headlineText(sum: SavingTotal, currency: string): string {
  if (sum.count === 0) return 'No savings candidates';
  const money = formatMoney(sum.total, currency);
  return `potential saving ≈ ${money} / month`;
}

export function headlineCaveat(sum: SavingTotal): string {
  const base = 'An estimate, not a promise: confidence is medium or lower for VMs, Cloud Run and snapshots, and nothing is changed by cloud0.';
  return sum.unknown > 0 ? `${base} ${sum.unknown} candidate${sum.unknown === 1 ? ' has' : 's have'} no matching billing row and ${sum.unknown === 1 ? 'is' : 'are'} not in the total.` : base;
}

export function thresholdsHint(t: RightsizingResponse['thresholds']): string {
  return `VMs under ${t.cpu_pct} % mean CPU for ${t.weeks}+ weeks; snapshots older than ${t.stale_snapshot_days} days; Cloud Run idle spend of at least ${t.idle_run_min_net} per week.`;
}

export function footnoteText(monthlyFactor: number): string {
  return `Savings are estimates. VMs: 50 % of last week's core+RAM cost (one machine size down). Disks, IPs and snapshots: their full last-week cost. Cloud Run: last week's idle-instance cost (if cold starts are acceptable). Weekly figures are scaled to a month with × ${monthlyFactor.toFixed(3)} (365.25 ÷ 84).`;
}

// ---- sparkline ---------------------------------------------------------

export interface SparkBar {
  week: WeekID;
  value: number | null;
  x: number;
  y: number;
  width: number;
  height: number;
  /** True when a threshold exists and the value reaches it. */
  over: boolean;
}

export interface SparkGeometry {
  width: number;
  height: number;
  bars: SparkBar[];
  /** Y of the threshold line, or null when there is none. */
  thresholdY: number | null;
  max: number;
}

/**
 * Bars scaled to a shared max (the larger of the values and the threshold, so
 * the line is always inside the box). Missing values (null) have no bar.
 */
export function sparkGeometry(points: RightsizingCandidate['evidence']['points'], threshold: number | null, width = 96, height = 28): SparkGeometry {
  const pad = 2;
  const values = points.map((p) => p.value).filter((v): v is number => v !== null && Number.isFinite(v));
  const max = Math.max(0, ...values, threshold ?? 0);
  const n = points.length;
  const slot = n > 0 ? width / n : width;
  const barW = Math.max(2, slot - 4);
  const inner = height - pad * 2;
  const scale = (v: number) => (max > 0 ? (Math.max(0, v) / max) * inner : 0);
  const bars: SparkBar[] = points.map((p, i) => {
    const h = p.value === null ? 0 : scale(p.value);
    return {
      week: p.week,
      value: p.value,
      x: Math.round((i * slot + (slot - barW) / 2) * 100) / 100,
      y: Math.round((height - pad - h) * 100) / 100,
      width: Math.round(barW * 100) / 100,
      height: Math.round(h * 100) / 100,
      over: threshold !== null && p.value !== null && p.value >= threshold,
    };
  });
  return { width, height, bars, thresholdY: threshold === null ? null : Math.round((height - pad - scale(threshold)) * 100) / 100, max };
}

function fmtValue(v: number | null, unit: string): string {
  if (v === null) return 'no data';
  const n = Math.round(v * 10) / 10;
  return unit === '%' ? `${n} %` : `${n.toLocaleString('en-IN')} ${unit}`.trim();
}

export function pointText(p: { week: string; value: number | null; unit: string }): string {
  return `${p.week}: ${fmtValue(p.value, p.unit)}`;
}

/** Threshold drawn on the spark: only for CPU series. */
export function sparkThreshold(c: RightsizingCandidate, thresholds: RightsizingResponse['thresholds']): number | null {
  return c.kind === 'low_cpu_vm' ? thresholds.cpu_pct : null;
}

/** Screen-reader / title text for the spark. */
export function sparkText(c: RightsizingCandidate, threshold: number | null): string {
  const pts = c.evidence.points;
  if (pts.length === 0) return 'No weekly series for this kind of hint.';
  const what = c.kind === 'low_cpu_vm' ? 'Mean CPU per week' : c.kind === 'cloud_run_min_instances' ? 'Idle cost per week' : 'Value per week';
  const list = pts.map(pointText).join('; ');
  return `${what}: ${list}${threshold !== null ? `. Threshold ${threshold} %.` : '.'}`;
}

// ---- evidence ------------------------------------------------------------

export function windowText(c: RightsizingCandidate): string {
  const { window_start: s, window_end: e } = c.evidence;
  return s === e ? `Observed ${s}` : `Evidence window ${s} to ${e}`;
}

export interface EvidenceLine {
  label: string;
  value: string;
}

export function evidenceLines(c: RightsizingCandidate, currency: string): EvidenceLine[] {
  const lines: EvidenceLine[] = c.evidence.points.map((p) => ({ label: p.week, value: fmtValue(p.value, p.unit) }));
  lines.push({ label: 'Net cost last week', value: c.net_last_week === null ? 'no matching billing row' : formatMoney(c.net_last_week, currency) });
  lines.push({ label: 'Saving basis', value: c.saving_basis });
  return lines;
}

// ---- sources / empty state ---------------------------------------------------

export function problemSources(sources: readonly RightsizingSource[]): RightsizingSource[] {
  return sources.filter((s) => s.status !== 'ok');
}

/** The ISO weeks ending at `week` that the VM CPU rule needs stored. */
export function windowWeeks(week: WeekID, n: number): WeekID[] {
  const end = weekStartDate(week);
  const out: WeekID[] = [];
  for (let i = n - 1; i >= 0; i--) out.push(weekIdOf(new Date(end.getTime() - i * 7 * 86_400_000)));
  return out;
}

export function emptyHint(week: WeekID, thresholds: RightsizingResponse['thresholds'], hasSnapshot: boolean): string {
  const weeks = windowWeeks(week, thresholds.weeks);
  const span = weeks.length > 1 ? `${weeks[0]} to ${weeks[weeks.length - 1]}` : weeks[0]!;
  if (!hasSnapshot) return `There is no cost snapshot for ${week}. Refresh ${week} first.`;
  return `VM hints need a stored Metrics refresh for each of ${span}; disk, IP and snapshot hints need the Compute inventory source. Refresh the weeks that are missing, then reload this tab.`;
}

// ---- CSV -------------------------------------------------------------------------

export function buildRightsizingCsv(list: readonly RightsizingCandidate[]): string {
  const head = ['kind', 'project', 'resource', 'location', 'confidence', 'net_last_week', 'estimated_monthly_saving', 'window_start', 'window_end', 'recommendation', 'saving_basis'];
  const rows = list.map((c) =>
    [
      c.kind,
      c.project_id,
      c.resource_name,
      c.location ?? '',
      c.confidence,
      c.net_last_week === null ? '' : csvNumber(c.net_last_week),
      c.estimated_monthly_saving === null ? '' : csvNumber(c.estimated_monthly_saving),
      c.evidence.window_start,
      c.evidence.window_end,
      c.action,
      c.saving_basis,
    ]
      .map(csvField)
      .join(','),
  );
  return [head.join(','), ...rows].join('\r\n') + '\r\n';
}

export function rightsizingCsvFilename(scope: Scope, week: WeekID, currency: string): string {
  return `cloud0-rightsizing-${scope}-${week}-${currency}.csv`;
}
