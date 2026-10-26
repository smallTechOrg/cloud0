// Client-side CSV export of the rendered Cost table
// (spec/capabilities/cost-table.md → CSV export). Pure functions + one
// browser helper that triggers the download.

import type { Column, CostRow, Scope, WeekID } from './types';

const CRLF = '\r\n';

/** RFC 4180: quote when the field contains a comma, quote, CR or LF; double embedded quotes. */
export function csvField(value: string): string {
  if (/[",\r\n]/.test(value)) {
    return `"${value.replace(/"/g, '""')}"`;
  }
  return value;
}

/** 2 decimals, `-` sign for negatives, never `-0.00`, no thousands separator. */
export function csvNumber(n: number): string {
  // Round half away from zero so negatives mirror positives.
  const rounded = (Math.sign(n) * Math.round(Math.abs(n) * 100)) / 100;
  return (rounded === 0 ? 0 : rounded).toFixed(2);
}

function cellValue(column: Column, row: CostRow): string {
  switch (column.kind) {
    case 'week':
      return row.week;
    case 'service':
    case 'total': {
      const cell = row.cells?.[column.key];
      return cell ? csvNumber(cell.net) : '';
    }
    case 'pct_change':
      return row.pct_change === null ? '' : csvNumber(row.pct_change);
    case 'stub':
      return '';
  }
}

/**
 * Header row = column labels (no "(stub)" suffix), one line per table row,
 * CRLF line endings, UTF-8 without BOM.
 */
export function buildCsv(columns: Column[], rows: CostRow[]): string {
  const lines: string[] = [columns.map((c) => csvField(c.label)).join(',')];
  for (const row of rows) {
    lines.push(columns.map((c) => csvField(cellValue(c, row))).join(','));
  }
  return lines.join(CRLF) + CRLF;
}

/** `cloud0-cost-<scope>-<from>-<to>.csv` with filename-unsafe characters replaced. */
export function csvFilename(scope: Scope, from: WeekID, to: WeekID): string {
  const safeScope = scope.replace(/[^A-Za-z0-9._-]/g, '_');
  return `cloud0-cost-${safeScope}-${from}-${to}.csv`;
}

/** Browser-only: offers `text` as a file download. */
export function downloadTextFile(filename: string, text: string, mime = 'text/csv;charset=utf-8'): void {
  const blob = new Blob([text], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.rel = 'noopener';
  document.body.appendChild(a);
  a.click();
  a.remove();
  // Give the browser a tick to start the download before revoking.
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
