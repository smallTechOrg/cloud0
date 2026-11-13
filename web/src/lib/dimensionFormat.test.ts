import { describe, expect, it } from 'vitest';
import { costByEnvironmentFixture, costByProductFixture, targetsFixture } from './fixtures4';
import {
  ALLOCATE_OPTIONS,
  breakdownCsvFilename,
  buildBreakdownCsv,
  buildTargetsCsv,
  buildTargetsUpdate,
  isAllocateMode,
  orderGroups,
  parseTargetInput,
  progressWidth,
  reconciliationText,
  serviceRows,
  targetLabel,
  targetsCsvFilename,
  targetsSummary,
} from './dimensionFormat';

describe('orderGroups', () => {
  it('sorts by net desc, shared then unattributed last, regardless of input order', () => {
    const shuffled = [...costByProductFixture.groups].reverse();
    expect(orderGroups(shuffled).map((g) => g.key)).toEqual(['example-product', 'example-other', '(shared)', '(unattributed)']);
  });
  it('breaks ties by key and keeps special groups last even with a larger net', () => {
    const base = costByProductFixture.groups[0]!;
    const mk = (key: string, net: number, kind: typeof base.kind) => ({ ...base, key, label: key, net, kind });
    const out = orderGroups([mk('(unattributed)', 999, 'unattributed'), mk('b', 5, 'product'), mk('a', 5, 'product'), mk('(shared)', 800, 'shared')]);
    expect(out.map((g) => g.key)).toEqual(['a', 'b', '(shared)', '(unattributed)']);
  });
  it('does not mutate its input', () => {
    const copy = [...costByEnvironmentFixture.groups].reverse();
    const before = copy.map((g) => g.key);
    orderGroups(copy);
    expect(copy.map((g) => g.key)).toEqual(before);
  });
});

describe('serviceRows', () => {
  it('lists non-zero service cells in column order, excluding total', () => {
    expect(serviceRows(costByProductFixture, costByProductFixture.groups[0]!)).toEqual([
      { key: 'compute_engine', label: 'Compute Engine', net: 900 },
      { key: 'cloud_run', label: 'Cloud Run', net: 512.5 },
    ]);
  });
  it('is empty for groups that only have a total cell', () => {
    expect(serviceRows(costByProductFixture, costByProductFixture.groups[2]!)).toEqual([]);
  });
});

describe('reconciliationText', () => {
  it('says it adds up when ok', () => {
    const r = reconciliationText(costByProductFixture.reconciliation, 'INR');
    expect(r.ok).toBe(true);
    expect(r.label).toBe('adds up to the Cost table total');
    expect(r.detail).toContain('₹2,012.50');
  });
  it('reports the signed difference when not ok', () => {
    const r = reconciliationText({ cost_table_total: 100, breakdown_total: 97.5, diff: -2.5, ok: false }, 'INR');
    expect(r.ok).toBe(false);
    expect(r.label).toContain('differs by −₹2.50');
    const pos = reconciliationText({ cost_table_total: 100, breakdown_total: 103, diff: 3, ok: false }, 'INR');
    expect(pos.label).toContain('+₹3.00');
  });
  it('marks filtered views', () => {
    expect(reconciliationText(costByProductFixture.reconciliation, 'INR', true).label).toContain('filtered');
  });
});

describe('allocate options', () => {
  it('has the three modes, each with an explanation', () => {
    expect(ALLOCATE_OPTIONS.map((o) => o.mode)).toEqual(['none', 'even', 'proportional']);
    expect(ALLOCATE_OPTIONS.every((o) => o.explain.length > 10)).toBe(true);
    expect(isAllocateMode('even')).toBe(true);
    expect(isAllocateMode('weird')).toBe(false);
  });
});

describe('breakdown CSV', () => {
  const csv = buildBreakdownCsv(costByProductFixture);
  const lines = csv.trimEnd().split('\r\n');
  it('has a header, ordered group rows and a total row', () => {
    expect(lines[0]).toBe('Product,Kind,Direct,Allocated,Net,Share %,Compute Engine,Cloud Run');
    expect(lines[1]).toBe('example-product,product,1412.50,0.00,1412.50,70.19,900.00,512.50');
    expect(lines[3]!.startsWith('(shared),shared,')).toBe(true);
    expect(lines[4]!.startsWith('(unattributed),unattributed,')).toBe(true);
    expect(lines[5]).toBe('Total,total,,,2012.50,,1237.50,512.50');
    expect(csv.endsWith('\r\n')).toBe(true);
  });
  it('names the file by dimension, scope, week, allocation and currency', () => {
    expect(breakdownCsvFilename(costByProductFixture, 'inr')).toBe('cloud0-by-product-all-2026-W43-none-INR.csv');
  });
});

describe('target input', () => {
  it('parses numbers, commas, and empty as clear', () => {
    expect(parseTargetInput('6,000.50')).toEqual({ ok: true, value: 6000.5 });
    expect(parseTargetInput('  ')).toEqual({ ok: true, value: null });
    expect(parseTargetInput('0')).toEqual({ ok: true, value: 0 });
  });
  it('rejects negatives, text and exponents', () => {
    for (const bad of ['-1', 'abc', '1e3', '12.', '1.2.3', 'NaN']) expect(parseTargetInput(bad).ok).toBe(false);
  });
  it('builds a PUT of changed valid entries only', () => {
    const rows = [targetsFixture.account, ...targetsFixture.products];
    const r = buildTargetsUpdate(rows, { '*': '25000', 'example-product': '7000', 'example-other': '' }, '2026-10');
    expect(r.errors).toEqual({});
    expect(r.update).toEqual({ month_from: '2026-10', targets: [{ product: 'example-product', amount: 7000 }] });
  });
  it('clears an existing target with empty text and reports invalid entries without an update', () => {
    const rows = [targetsFixture.account, ...targetsFixture.products];
    expect(buildTargetsUpdate(rows, { 'example-product': '' }, '2026-10').update?.targets).toEqual([{ product: 'example-product', amount: null }]);
    const bad = buildTargetsUpdate(rows, { 'example-product': 'x', 'example-other': '10' }, '2026-10');
    expect(bad.update).toBeNull();
    expect(Object.keys(bad.errors)).toEqual(['example-product']);
  });
});

describe('targets display', () => {
  it('clamps progress and labels the account', () => {
    expect(progressWidth(212.3)).toBe(100);
    expect(progressWidth(-5)).toBe(0);
    expect(progressWidth(null)).toBe(0);
    expect(progressWidth(42.5)).toBe(42.5);
    expect(targetLabel('*')).toBe('Account total');
  });
  it('summarises and exports with shared/unattributed rows last', () => {
    expect(targetsSummary(targetsFixture, 'INR')).toBe('2026-10 · net ₹18,200.50 · projected ₹22,100.30 · target ₹25,000.00');
    const lines = buildTargetsCsv(targetsFixture).trimEnd().split('\r\n');
    expect(lines[1]!.startsWith('Account total,account,25000.00,18200.50')).toBe(true);
    expect(lines[2]!).toContain('example-product,product,6000.00');
    expect(lines[2]!).toContain('Over');
    expect(lines.at(-2)?.startsWith('(shared)')).toBe(true);
    expect(lines.at(-1)?.startsWith('(unattributed)')).toBe(true);
    expect(targetsCsvFilename(targetsFixture)).toBe('cloud0-targets-2026-10-INR.csv');
  });
});
