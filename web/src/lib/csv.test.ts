import { describe, expect, it } from 'vitest';
import { buildCsv, csvField, csvFilename } from './csv';
import type { Cell, Column, CostRow } from './types';

const columns: Column[] = [
  { key: 'week', label: 'Week', kind: 'week' },
  { key: 'compute_engine', label: 'Compute Engine', kind: 'service', services: ['Compute Engine'] },
  { key: 'networking', label: 'Networking', kind: 'service', services: ['Networking'] },
  { key: 'svc:Cloud Monitoring', label: 'Cloud Monitoring', kind: 'service', services: ['Cloud Monitoring'], extra: true },
  { key: 'total', label: 'Total GCP', kind: 'total' },
  { key: 'pct_change', label: '% change', kind: 'pct_change' },
  { key: 'stub:groq', label: 'AI Cost (Groq)', kind: 'stub', phase: 4 },
  { key: 'stub:claude', label: 'claude (usage)', kind: 'stub', phase: 4 },
];

function cell(net: number, gross = net, credits = 0): Cell {
  return { net, gross, credits, delta_abs: null, delta_pct: null, flagged: false };
}

const fetched: CostRow = {
  week: '2026-W42',
  week_start: '2026-10-12',
  week_end: '2026-10-18',
  week_status: 'final',
  snapshot: { id: 12, fetched_at: '2026-10-26T09:01:10Z', status_at_fetch: 'final', run_id: 7, row_count: 2345, stale: false },
  cells: {
    compute_engine: cell(1191.69),
    networking: cell(403.41, 596.92, -193.51),
    'svc:Cloud Monitoring': cell(0.5),
    total: cell(2095.11),
  },
  pct_change: 25.36,
  flagged: true,
  annotation: null,
};

const notFetched: CostRow = {
  week: '2026-W43',
  week_start: '2026-10-19',
  week_end: '2026-10-25',
  week_status: 'settling',
  snapshot: null,
  cells: null,
  pct_change: null,
  flagged: false,
  annotation: null,
};

describe('csvField', () => {
  it('leaves plain values alone', () => {
    expect(csvField('Compute Engine')).toBe('Compute Engine');
    expect(csvField('1191.69')).toBe('1191.69');
  });

  it('quotes commas, quotes and newlines, doubling embedded quotes', () => {
    expect(csvField('a,b')).toBe('"a,b"');
    expect(csvField('say "hi"')).toBe('"say ""hi"""');
    expect(csvField('line1\nline2')).toBe('"line1\nline2"');
    expect(csvField('cr\rhere')).toBe('"cr\rhere"');
  });
});

describe('buildCsv', () => {
  const csv = buildCsv(columns, [fetched, notFetched]);
  const lines = csv.split('\r\n');

  it('uses CRLF line endings and ends with one', () => {
    expect(csv.endsWith('\r\n')).toBe(true);
    expect(csv.includes('\n') && !csv.includes('\r\n')).toBe(false);
    // header + 2 rows + trailing empty segment from the final CRLF
    expect(lines).toHaveLength(4);
    expect(lines[3]).toBe('');
  });

  it('first line equals the column labels joined by commas, in order', () => {
    expect(lines[0]).toBe(
      'Week,Compute Engine,Networking,Cloud Monitoring,Total GCP,% change,AI Cost (Groq),claude (usage)',
    );
  });

  it('formats numbers with 2 decimals, pct as a number, stub cells empty', () => {
    expect(lines[1]).toBe('2026-W42,1191.69,403.41,0.50,2095.11,25.36,,');
  });

  it('renders a not-fetched row as the week id followed by empty cells', () => {
    expect(lines[2]).toBe('2026-W43,,,,,,,');
  });

  it('writes negatives with a minus sign and never -0.00', () => {
    const row: CostRow = { ...fetched, cells: { ...fetched.cells, networking: cell(-12.346), total: cell(-0.001) } };
    const out = buildCsv(columns, [row]).split('\r\n')[1];
    expect(out).toBe('2026-W42,1191.69,-12.35,0.50,0.00,25.36,,');
  });

  it('quotes a label containing a comma', () => {
    const cols: Column[] = [columns[0]!, { key: 'svc:Foo, Inc', label: 'Foo, Inc', kind: 'service', services: ['Foo, Inc'] }];
    expect(buildCsv(cols, []).split('\r\n')[0]).toBe('Week,"Foo, Inc"');
  });

  it('writes an empty cell when a fetched row lacks a column key', () => {
    const row: CostRow = { ...fetched, cells: { total: cell(1) } };
    expect(buildCsv(columns, [row]).split('\r\n')[1]).toBe('2026-W42,,,,1.00,25.36,,');
  });

  it('does not start with a BOM', () => {
    expect(csv.charCodeAt(0)).not.toBe(0xfeff);
  });
});

describe('csvFilename', () => {
  it('encodes scope and range', () => {
    expect(csvFilename('all', '2026-W18', '2026-W44', 'INR')).toBe('cloud0-cost-all-2026-W18-2026-W44-INR.csv');
    expect(csvFilename('my-project', '2026-W18', '2026-W44', 'inr')).toBe('cloud0-cost-my-project-2026-W18-2026-W44-INR.csv');
  });

  it('sanitises characters that are unsafe in filenames', () => {
    expect(csvFilename('a/b c', '2026-W18', '2026-W18', 'USD')).toBe('cloud0-cost-a_b_c-2026-W18-2026-W18-USD.csv');
  });
});
