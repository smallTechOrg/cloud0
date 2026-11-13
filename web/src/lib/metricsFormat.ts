// Pure helpers for the Metrics and Errors panels: group order/headings,
// value formatting, limit classes, provenance text and CSV builders.

import { csvField, csvNumber } from './csv';
import type {
  ErrorTableRow,
  ErrorTotals,
  ErrorsTableResponse,
  LimitLevel,
  MetricCell,
  MetricGroup,
  MetricGroupId,
  MetricsTableResponse,
  Provenance,
  Scope,
  WeekID,
} from './types';

const CRLF = '\r\n';

/** The sheet's Metrics block order. */
export const GROUP_ORDER: readonly MetricGroupId[] = ['vm', 'cloudsql', 'networking', 'cloudrun', 'gcs', 'logging', 'errors'];

export const GROUP_HEADINGS: Record<MetricGroupId, string> = {
  vm: 'VMs',
  cloudsql: 'Cloud SQL',
  networking: 'Networking',
  cloudrun: 'Cloud Run',
  gcs: 'Cloud Storage',
  logging: 'Cloud Logging',
  errors: 'Errors',
};

export function groupHeading(group: MetricGroupId, fallback = ''): string {
  return GROUP_HEADINGS[group] ?? fallback;
}

/** Groups sorted into sheet order; unknown groups keep their relative order at the end. */
export function orderGroups(groups: readonly MetricGroup[]): MetricGroup[] {
  const rank = (g: MetricGroup) => {
    const i = GROUP_ORDER.indexOf(g.group);
    return i === -1 ? GROUP_ORDER.length : i;
  };
  return groups
    .map((g, i) => ({ g, i }))
    .sort((a, b) => rank(a.g) - rank(b.g) || a.i - b.i)
    .map((x) => x.g);
}

/** Dash placeholder for absent numbers. */
export const NO_VALUE = '—';

/** 2 dp with thousands grouping, e.g. 15300 -> "15,300.00". */
export function formatMetricNumber(n: number): string {
  const rounded = (Math.sign(n) * Math.round(Math.abs(n) * 100)) / 100;
  return (rounded === 0 ? 0 : rounded).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

/** Cell text on screen: the 2 dp value, or the gap text — never 0 for a gap. */
export function cellText(cell: MetricCell | undefined): string {
  if (!cell) return NO_VALUE;
  if (cell.gap !== null && cell.gap !== undefined) return cell.gap;
  if (cell.value === null || cell.value === undefined) return NO_VALUE;
  return formatMetricNumber(cell.value);
}

export function isGap(cell: MetricCell | undefined): boolean {
  return !!cell && cell.gap !== null && cell.gap !== undefined;
}

/** Unit line under a column heading; omitted when the label already carries it. */
export function headerUnit(label: string, unit: string): string | null {
  if (unit === '' || label.includes(unit)) return null;
  return unit;
}

export type LimitClassKey = 'limitNear' | 'limitOver';

/** `ok` and null carry no colour. */
export function limitClassKey(level: LimitLevel | null): LimitClassKey | null {
  if (level === 'near') return 'limitNear';
  if (level === 'over') return 'limitOver';
  return null;
}

/** Text companion so colour is never the only signal. */
export function limitLabel(level: LimitLevel | null): string | null {
  if (level === 'near') return 'near limit';
  if (level === 'over') return 'over limit';
  return null;
}

/** Aggregation is a JSON string in the API; pretty-print it when parseable. */
export function prettyAggregation(aggregation: string): string {
  try {
    return JSON.stringify(JSON.parse(aggregation), null, 2);
  } catch {
    return aggregation;
  }
}

export function hasProvenance(p: Provenance | undefined | null): p is Provenance {
  return !!p && (p.source !== '' || p.query !== '');
}

/** Tooltip for a cell: gap hint, or the unit. */
export function cellTitle(cell: MetricCell | undefined): string | undefined {
  if (!cell) return undefined;
  if (isGap(cell)) return cell.hint ? `${cell.gap}: ${cell.hint}` : (cell.gap ?? undefined);
  return cell.unit || undefined;
}

/** Row heading: the server-assembled TOTAL row reads "Total"; a bare empty resource keeps the legacy "<project> (total)". */
export function resourceLabel(project: string, resource: string, isTotal = false): { name: string; total: boolean } {
  if (isTotal) return { name: 'Total', total: true };
  return resource === '' ? { name: `${project} (total)`, total: true } : { name: resource, total: false };
}

// ---- CSV ---------------------------------------------------------------------

function csvLine(fields: string[]): string {
  return fields.map(csvField).join(',');
}

function csvCell(cell: MetricCell | undefined): string {
  if (!cell) return '';
  if (isGap(cell)) return cell.gap ?? '';
  return cell.value === null || cell.value === undefined ? '' : csvNumber(cell.value);
}

function columnTitle(label: string, unit: string): string {
  return headerUnit(label, unit) ? `${label} (${unit})` : label;
}

/** Sectioned CSV of the on-screen Metrics block: one section per group, gap text in the cell. */
export function buildMetricsCsv(data: MetricsTableResponse): string {
  const sections: string[] = [];
  for (const g of orderGroups(data.groups)) {
    const lines: string[] = [];
    lines.push(csvLine([groupHeading(g.group, g.label)]));
    lines.push(csvLine(['Project', 'Resource', ...g.columns.map((c) => columnTitle(c.label, c.unit))]));
    for (const r of g.rows) {
      lines.push(csvLine([r.project, r.is_total ? 'Total' : r.resource === '' ? '(total)' : r.resource, ...g.columns.map((c) => csvCell(r.cells[c.key]))]));
    }
    sections.push(lines.join(CRLF));
  }
  return sections.join(CRLF + CRLF) + CRLF;
}

export function metricsCsvFilename(scope: Scope, week: WeekID): string {
  return `cloud0-metrics-${scope.replace(/[^A-Za-z0-9._-]/g, '_')}-${week}.csv`;
}

export function errorsCsvFilename(scope: Scope, week: WeekID): string {
  return `cloud0-errors-${scope.replace(/[^A-Za-z0-9._-]/g, '_')}-${week}.csv`;
}

function count(n: number | null): string {
  return n === null || n === undefined ? '' : String(n);
}

export const ERROR_SOURCE_LABELS: Record<ErrorTableRow['source'], string> = {
  cloudrun: 'Cloud Run',
  error_reporting: 'Error Reporting',
  load_balancer: 'Load balancer',
  nginx: 'nginx (VM)',
};

export function buildErrorsCsv(data: ErrorsTableResponse): string {
  const header = ['Source', 'Project', 'Service', '2xx', '3xx', '4xx', '5xx', 'Groups', 'Occurrences', 'Gap', 'Fix hint'];
  const lines = [csvLine(header)];
  for (const r of data.rows) {
    lines.push(
      csvLine([
        ERROR_SOURCE_LABELS[r.source] ?? r.source,
        r.project,
        r.service,
        count(r.c2xx),
        count(r.c3xx),
        count(r.c4xx),
        count(r.c5xx),
        count(r.groups),
        count(r.occurrences),
        r.gap ?? '',
        r.hint ?? '',
      ]),
    );
  }
  const t: ErrorTotals = data.totals;
  lines.push(csvLine(['Total', '', '', count(t.c2xx), count(t.c3xx), count(t.c4xx), count(t.c5xx), count(t.groups), count(t.occurrences), '', '']));
  return lines.join(CRLF) + CRLF;
}

/** Integer counts with thousands grouping; null -> dash. */
export function formatCount(n: number | null): string {
  return n === null || n === undefined ? NO_VALUE : n.toLocaleString('en-US');
}
