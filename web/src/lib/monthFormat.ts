// Pure helpers for the Month tab: month ids, the summary line, CSV export.
// Kept free of React/DOM so they are unit-testable (spec/capabilities/month-view.md).

import { csvField, csvNumber } from './csv';
import { DEFAULT_CURRENCY, formatMoney } from './format';
import type { MonthColumn, MonthID, MonthRow, MonthSummary, RefreshResponse, Scope } from './types';

const CRLF = '\r\n';

export const MONTH_ID_PATTERN = /^(\d{4})-(0[1-9]|1[0-2])$/;

export function isMonthId(s: string): boolean {
  return MONTH_ID_PATTERN.test(s);
}

/** `YYYY-MM` of the UTC calendar month containing `now`. */
export function currentMonthId(now: Date): MonthID {
  return now.toISOString().slice(0, 7);
}

/** Shift a month id by `delta` months (negative = earlier). */
export function addMonths(month: MonthID, delta: number): MonthID {
  const m = MONTH_ID_PATTERN.exec(month);
  if (!m) throw new Error(`invalid month id: ${month}`);
  const index = Number(m[1]) * 12 + (Number(m[2]) - 1) + delta;
  const year = Math.floor(index / 12);
  const mon = (index % 12) + 1;
  return `${String(year).padStart(4, '0')}-${String(mon).padStart(2, '0')}`;
}

/** The table window for a picked month: that month and the five before it. */
export function monthWindow(picked: MonthID, span = 6): { from: MonthID; to: MonthID } {
  return { from: addMonths(picked, -(span - 1)), to: picked };
}

/** `Current month net ₹812.40 · target ₹2,500.00 · projected ₹2,210.90`; target `—` when unset. */
export function formatSummaryLine(s: MonthSummary, currency: string = DEFAULT_CURRENCY): string {
  const net = s.has_data ? formatMoney(s.net, currency) : '—';
  const projected = s.has_data ? formatMoney(s.projected, currency) : '—';
  const target = s.target === null ? '—' : formatMoney(s.target, currency);
  return `Current month net ${net} · target ${target} · projected ${projected}`;
}

function cellValue(column: MonthColumn, row: MonthRow): string {
  switch (column.kind) {
    case 'month':
      return row.month;
    case 'service':
    case 'total': {
      const cell = row.cells?.[column.key];
      return cell ? csvNumber(cell.net) : '';
    }
    case 'pct_change':
      return row.pct_change === null ? '' : csvNumber(row.pct_change);
    default:
      return '';
  }
}

/** Same conventions as the Cost-table CSV: label header, CRLF, no BOM. */
export function buildMonthCsv(columns: readonly MonthColumn[], rows: readonly MonthRow[]): string {
  const lines: string[] = [columns.map((c) => csvField(c.label)).join(',')];
  for (const row of rows) {
    lines.push(columns.map((c) => csvField(cellValue(c, row))).join(','));
  }
  return lines.join(CRLF) + CRLF;
}

/** `cloud0-month-<scope>-<from>-<to>-<CURRENCY>.csv`; cells are plain numbers in that currency. */
export function monthCsvFilename(scope: Scope, from: MonthID, to: MonthID, currency: string): string {
  const safeScope = scope.replace(/[^A-Za-z0-9._-]/g, '_');
  return `cloud0-month-${safeScope}-${from}-${to}-${currency.toUpperCase()}.csv`;
}

export interface RefreshOutcome {
  ok: boolean;
  /** First failed source of the run, when any. */
  failure: { source: string; code: string; message: string; hint: string | null } | null;
}

/** Interprets a refresh response: a failed source is a 200, so inspect the run. */
export function refreshOutcome(res: RefreshResponse): RefreshOutcome {
  const bad = res.run.sources.find((s) => s.status === 'failed');
  if (!bad) return { ok: res.run.status !== 'failed', failure: null };
  return {
    ok: false,
    failure: {
      source: bad.source,
      code: bad.error_code ?? 'failed',
      message: bad.error_message ?? `${bad.source} failed`,
      hint: bad.fix_hint,
    },
  };
}
