import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { BreakdownResponse, TargetsResponse } from '@/lib/attributionTypes';
import { costByEnvironmentFixture, costByProductFixture, targetsFixture } from '@/lib/fixtures4';
import { AllocateSelect } from './AllocateSelect';
import { CostByDimensionView, type CostByDimensionViewProps } from './CostByDimension';
import { TargetsView, type TargetsViewProps } from './TargetsPanel';

const noop = () => undefined;

function dim(data: BreakdownResponse = costByProductFixture, o: Partial<CostByDimensionViewProps> = {}) {
  return renderToStaticMarkup(
    createElement(CostByDimensionView, {
      data, allocate: data.allocate, onAllocate: noop, expanded: new Set<string>(), onToggle: noop, onExport: noop,
      provenanceOpen: false, onProvenance: noop, run: null, runError: null, ...o,
    }),
  );
}

function targets(data: TargetsResponse = targetsFixture, o: Partial<TargetsViewProps> = {}) {
  return renderToStaticMarkup(
    createElement(TargetsView, { data, drafts: {}, errors: {}, saving: false, dirty: false, saveError: null, notice: null, onDraft: noop, onSave: noop, onDiscard: noop, ...o }),
  );
}

describe('AllocateSelect', () => {
  it('renders three explained options with the current one checked', () => {
    const html = renderToStaticMarkup(createElement(AllocateSelect, { value: 'even', onChange: noop }));
    expect(html).toContain('allocate-option-none');
    expect(html).toContain('Spread shared evenly');
    expect(html).toContain('Spread proportionally');
    expect(html).toMatch(/checked=""[^>]*value="even"/);
  });
});

describe('CostByDimensionView', () => {
  it('renders groups in order with shared and unattributed last and the green badge', () => {
    const html = dim();
    const idx = ['example-product', 'example-other', '(shared)', '(unattributed)'].map((k) => html.indexOf(`dimension-row-${k.replace(/[()]/g, '')}`));
    expect(idx.every((i) => i > 0)).toBe(true);
    expect([...idx].sort((a, b) => a - b)).toEqual(idx);
    expect(html).toContain('adds up to the Cost table total');
    expect(html).toContain('data-ok="true"');
    expect(html).toContain('data-kind="shared"');
    expect(html).toContain('Shared bucket: ₹200.00');
    expect(html).toContain('₹2,012.50');
    expect(html).toContain('settling');
  });
  it('shows the red badge with the difference when it does not reconcile', () => {
    const html = dim({ ...costByProductFixture, reconciliation: { cost_table_total: 2012.5, breakdown_total: 2000, diff: -12.5, ok: false } });
    expect(html).toContain('data-ok="false"');
    expect(html).toContain('differs by −₹12.50');
  });
  it('expands to per-service rows', () => {
    const html = dim(costByProductFixture, { expanded: new Set(['example-product']) });
    expect(html).toContain('dimension-service-example-product-compute_engine');
    expect(html).toContain('₹900.00');
    expect(dim()).not.toContain('dimension-service-');
  });
  it('shows allocated amounts and no shared note when allocation is on', () => {
    const data: BreakdownResponse = {
      ...costByProductFixture,
      allocate: 'even',
      groups: costByProductFixture.groups.map((g) => (g.kind === 'shared' ? { ...g, allocated_net: -200, net: 0 } : g.kind === 'product' ? { ...g, allocated_net: 100, net: g.direct_net + 100 } : g)),
    };
    const html = dim(data);
    expect(html).not.toContain('dimension-shared-note');
    expect(html).toContain('−₹200.00');
    expect(html).toContain('now nets to zero');
  });
  it('renders the environment dimension with its own unattributed label', () => {
    const html = dim(costByEnvironmentFixture);
    expect(html).toContain('data-dimension="environment"');
    expect(html).toContain('(no environment)');
    expect(html).toContain('Cost by environment');
  });
  it('shows an empty state without a snapshot and a no-groups state without rules', () => {
    const none = dim({ ...costByProductFixture, snapshot: null, groups: [] });
    expect(none).toContain('dimension-empty');
    expect(none).not.toContain('dimension-table');
    const noRules = dim({ ...costByProductFixture, groups: costByProductFixture.groups.filter((g) => g.kind !== 'product') });
    expect(noRules).toContain('dimension-no-groups');
    expect(noRules).toContain('Attribution tab');
  });
  it('warns when labels were not captured and shows the stale hint', () => {
    const html = dim({ ...costByProductFixture, labels_captured: false, snapshot: { ...costByProductFixture.snapshot!, stale: true } });
    expect(html).toContain('dimension-no-labels');
    expect(html).toContain('Refresh for final');
  });
  it('shows provenance details when opened', () => {
    const html = dim(costByProductFixture, { provenanceOpen: true });
    expect(html).toContain('dimension-provenance');
    expect(html).toContain('#13');
    expect(html).toContain('Loading query');
  });
});

describe('TargetsView', () => {
  it('renders account, products, status chips with text, shared and unattributed rows, and the note', () => {
    const html = targets();
    expect(html).toContain('target-row-account');
    expect(html).toContain('target-row-example-product');
    expect(html).toContain('>On track<');
    expect(html).toContain('>Over<');
    expect(html).toContain('>No target<');
    expect(html.indexOf('target-row-shared')).toBeGreaterThan(html.indexOf('target-row-example-other'));
    expect(html.indexOf('target-row-unattributed')).toBeGreaterThan(html.indexOf('target-row-shared'));
    expect(html).toContain('products are estimates'.replace('products', 'Product figures'));
    expect(html).toContain('Save targets');
    expect(html).toMatch(/data-testid="targets-save"[^>]*disabled/);
    expect(html).toContain('role="progressbar"');
    expect(html).toContain('width:100%');
  });
  it('shows inline errors and enables save when dirty', () => {
    const html = targets(targetsFixture, { drafts: { 'example-product': 'x' }, errors: { 'example-product': 'Enter a number ≥ 0, or leave empty to clear.' }, dirty: true, saveError: 'unknown product "x"' });
    expect(html).toContain('target-error-example-product');
    expect(html).toContain('aria-invalid="true"');
    expect(html).toContain('targets-save-error');
    expect(html).not.toMatch(/data-testid="targets-save"[^>]*disabled/);
  });
  it('shows the add-rules empty state when there are no products', () => {
    const html = targets({ ...targetsFixture, products: [], share_weeks: [], covered_until: null });
    expect(html).toContain('targets-empty');
    expect(html).toContain('Add rules in the Attribution tab first');
  });
  it('shows a no-data state for an unfetched month', () => {
    const html = targets({ ...targetsFixture, account: { ...targetsFixture.account, net: 0, projected: 0, pct: null, remaining: null, status: 'no_data' }, products: [], share_weeks: [], covered_until: null });
    expect(html).toContain('targets-no-data');
    expect(html).toContain('>No data<');
  });
});
