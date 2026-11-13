// Pure helpers for the product/environment breakdown and the Targets tab
// (spec/api.md → "Attribution additions"): ordering, allocation copy,
// reconciliation text, target input validation, CSV builders.

import type {
  AllocateMode,
  BreakdownGroup,
  BreakdownResponse,
  TargetRow,
  TargetStatus,
  TargetsResponse,
  TargetsUpdate,
} from './attributionTypes';
import { csvField, csvNumber } from './csv';
import { formatMoney } from './format';
import type { Scope } from './types';

const CRLF = '\r\n';

// ---- breakdown ----------------------------------------------------------------

export const ALLOCATE_OPTIONS: { mode: AllocateMode; label: string; explain: string }[] = [
  { mode: 'none', label: 'Keep shared separate', explain: 'Shared cost stays in its own (shared) bucket; nothing is spread.' },
  { mode: 'even', label: 'Spread shared evenly', explain: 'Shared cost is split equally across every group that has direct cost.' },
  { mode: 'proportional', label: 'Spread proportionally', explain: 'Shared cost is split by each group’s share of the direct cost.' },
];

export function isAllocateMode(s: string): s is AllocateMode {
  return s === 'none' || s === 'even' || s === 'proportional';
}

function kindRank(g: BreakdownGroup): number {
  return g.kind === 'shared' ? 1 : g.kind === 'unattributed' ? 2 : 0;
}

/** Product/environment groups by net desc then key; shared next; unattributed last. */
export function orderGroups(groups: readonly BreakdownGroup[]): BreakdownGroup[] {
  return [...groups].sort((a, b) => {
    const k = kindRank(a) - kindRank(b);
    if (k !== 0) return k;
    if (a.kind !== 'product' && a.kind !== 'environment') return 0;
    if (b.net !== a.net) return b.net - a.net;
    return a.key < b.key ? -1 : a.key > b.key ? 1 : 0;
  });
}

export function isSpecialGroup(g: BreakdownGroup): boolean {
  return g.kind === 'shared' || g.kind === 'unattributed';
}

/** Service columns only (the expandable per-service rows), in response order. */
export function serviceColumns(data: BreakdownResponse) {
  return data.columns.filter((c) => c.kind === 'service');
}

/** Per-service rows of a group: non-zero service cells in column order. */
export function serviceRows(data: BreakdownResponse, group: BreakdownGroup): { key: string; label: string; net: number }[] {
  const rows: { key: string; label: string; net: number }[] = [];
  for (const c of serviceColumns(data)) {
    const cell = group.cells[c.key];
    if (cell && cell.net !== 0) rows.push({ key: c.key, label: c.label, net: cell.net });
  }
  return rows;
}

export function dimensionTitle(d: 'product' | 'environment'): string {
  return d === 'product' ? 'Cost by product' : 'Cost by environment';
}

export function unattributedLabel(d: 'product' | 'environment'): string {
  return d === 'product' ? '(unattributed)' : '(no environment)';
}

/** Short explanation of what a special group is, shown under its name. */
export function groupHint(g: BreakdownGroup, allocate: AllocateMode): string | null {
  if (g.kind === 'shared') {
    return allocate === 'none'
      ? 'Shared infrastructure not assigned to one product; kept in its own bucket.'
      : 'Shared cost has been spread to the groups above; this bucket now nets to zero.';
  }
  if (g.kind === 'unattributed') return 'No rule decided this cost; never spread. Add rules in the Attribution tab.';
  return null;
}

export interface ReconText {
  ok: boolean;
  label: string;
  detail: string;
}

export function reconciliationText(r: BreakdownResponse['reconciliation'], currency: string, filtered = false): ReconText {
  if (r.ok) {
    return {
      ok: true,
      label: filtered ? 'adds up to the filtered Cost table total' : 'adds up to the Cost table total',
      detail: `breakdown ${formatMoney(r.breakdown_total, currency)} = Cost table ${formatMoney(r.cost_table_total, currency)}`,
    };
  }
  const sign = r.diff > 0 ? '+' : '−';
  return {
    ok: false,
    label: `does not add up: differs by ${sign}${formatMoney(Math.abs(r.diff), currency)}`,
    detail: `breakdown ${formatMoney(r.breakdown_total, currency)} vs Cost table ${formatMoney(r.cost_table_total, currency)}`,
  };
}

export function isFilteredView(data: BreakdownResponse): boolean {
  return data.notes.some((n) => n.includes('Filtered view'));
}

export function buildBreakdownCsv(data: BreakdownResponse): string {
  const svc = serviceColumns(data);
  const header = [data.dimension === 'product' ? 'Product' : 'Environment', 'Kind', 'Direct', 'Allocated', 'Net', 'Share %', ...svc.map((c) => c.label)];
  const lines = [header.map(csvField).join(',')];
  for (const g of orderGroups(data.groups)) {
    lines.push(
      [
        g.label,
        g.kind,
        csvNumber(g.direct_net),
        csvNumber(g.allocated_net),
        csvNumber(g.net),
        csvNumber(g.share_pct),
        ...svc.map((c) => csvNumber(g.cells[c.key]?.net ?? 0)),
      ]
        .map(csvField)
        .join(','),
    );
  }
  const totalNet = data.total.cells.total?.net ?? 0;
  lines.push(
    ['Total', 'total', '', '', csvNumber(totalNet), '', ...svc.map((c) => csvNumber(data.total.cells[c.key]?.net ?? 0))].map(csvField).join(','),
  );
  return lines.join(CRLF) + CRLF;
}

export function breakdownCsvFilename(data: BreakdownResponse, currency: string): string {
  const scope = (data.scope as Scope).replace(/[^A-Za-z0-9._-]/g, '_');
  return `cloud0-by-${data.dimension}-${scope}-${data.week}-${data.allocate}-${currency.toUpperCase()}.csv`;
}

// ---- targets --------------------------------------------------------------------

export const TARGETS_NOTE =
  'Product figures are estimates: each product’s share of the month is taken from the weekly snapshots that start in the month, applied to the Month tab total. Products, shared and unattributed add up to the account net.';

export const ACCOUNT_KEY = '*';

export const STATUS_LABEL: Record<TargetStatus, string> = {
  on_track: 'On track',
  at_risk: 'At risk',
  over: 'Over',
  no_target: 'No target',
  no_data: 'No data',
};

export const STATUS_TITLE: Record<TargetStatus, string> = {
  on_track: 'projected month end is within the target',
  at_risk: 'projected month end is up to 10 % over the target',
  over: 'projected month end is more than 10 % over the target',
  no_target: 'no target set for this month',
  no_data: 'no data for this month yet',
};

export function targetLabel(product: string): string {
  return product === ACCOUNT_KEY ? 'Account total' : product;
}

/** Bar fill 0..100 (the label keeps the real figure). */
export function progressWidth(pct: number | null): number {
  if (pct === null || !Number.isFinite(pct)) return 0;
  return Math.min(100, Math.max(0, pct));
}

export type TargetParse = { ok: true; value: number | null } | { ok: false; message: string };

/** Empty = clear the target; otherwise a plain non-negative number (commas and spaces ignored). */
export function parseTargetInput(text: string): TargetParse {
  const t = text.replace(/[,\s]/g, '');
  if (t === '') return { ok: true, value: null };
  if (!/^\d+(\.\d+)?$/.test(t)) return { ok: false, message: 'Enter a number ≥ 0, or leave empty to clear.' };
  const n = Number(t);
  if (!Number.isFinite(n)) return { ok: false, message: 'Enter a number ≥ 0, or leave empty to clear.' };
  return { ok: true, value: n };
}

/** Text shown in an editable target cell for a stored value. */
export function targetInputText(target: number | null): string {
  return target === null ? '' : String(target);
}

export interface TargetsDraftResult {
  update: TargetsUpdate | null;
  errors: Record<string, string>;
}

/** Only changed, valid entries go into the PUT; invalid entries are reported per product. */
export function buildTargetsUpdate(rows: readonly TargetRow[], edits: Record<string, string>, monthFrom: string): TargetsDraftResult {
  const errors: Record<string, string> = {};
  const targets: TargetsUpdate['targets'] = [];
  for (const row of rows) {
    const text = edits[row.product];
    if (text === undefined) continue;
    const parsed = parseTargetInput(text);
    if (!parsed.ok) {
      errors[row.product] = parsed.message;
      continue;
    }
    if (parsed.value === row.target) continue;
    targets.push({ product: row.product, amount: parsed.value });
  }
  if (Object.keys(errors).length > 0 || targets.length === 0) return { update: null, errors };
  return { update: { month_from: monthFrom, targets }, errors };
}

export function targetsEmpty(data: TargetsResponse): boolean {
  return data.products.length === 0;
}

export function buildTargetsCsv(data: TargetsResponse): string {
  const header = ['Name', 'Kind', 'Target', 'Net to date', 'Projected', 'Remaining', 'Used %', 'Status', 'Share %'];
  const lines = [header.join(',')];
  const num = (n: number | null) => (n === null ? '' : csvNumber(n));
  const add = (r: TargetRow, kind: string) =>
    lines.push(
      [targetLabel(r.product), kind, num(r.target), csvNumber(r.net), csvNumber(r.projected), num(r.remaining), num(r.pct), STATUS_LABEL[r.status], num(r.share_pct ?? null)]
        .map(csvField)
        .join(','),
    );
  add(data.account, 'account');
  for (const p of data.products) add(p, 'product');
  lines.push(['(shared)', 'shared', '', csvNumber(data.shared.net), csvNumber(data.shared.projected), '', '', '', csvNumber(data.shared.share_pct)].map(csvField).join(','));
  lines.push(
    ['(unattributed)', 'unattributed', '', csvNumber(data.unattributed.net), csvNumber(data.unattributed.projected), '', '', '', csvNumber(data.unattributed.share_pct)]
      .map(csvField)
      .join(','),
  );
  return lines.join(CRLF) + CRLF;
}

export function targetsCsvFilename(data: TargetsResponse): string {
  return `cloud0-targets-${data.month}-${data.currency.toUpperCase()}.csv`;
}

/** One-line summary under the month picker. */
export function targetsSummary(data: TargetsResponse, currency: string): string {
  const a = data.account;
  const t = a.target === null ? 'no account target' : `target ${formatMoney(a.target, currency)}`;
  return `${data.month} · net ${formatMoney(a.net, currency)} · projected ${formatMoney(a.projected, currency)} · ${t}`;
}
