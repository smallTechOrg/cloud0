import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { RightsizingCandidate, RightsizingResponse } from '@/lib/attributionTypes';
import { rightsizingFixture } from '@/lib/fixtures4';
import { CandidateCard } from './CandidateCard';
import { RightsizingView } from './RightsizingPanel';

const noop = () => undefined;
const [vm, run] = rightsizingFixture.candidates as [RightsizingCandidate, RightsizingCandidate];
const th = rightsizingFixture.thresholds;
const flat = (c: RightsizingCandidate) => ({ ...c, evidence: { ...c.evidence, points: [], notes: ['note'] } });

const base = (kind: RightsizingCandidate['kind'], over: Partial<RightsizingCandidate> = {}): RightsizingCandidate => ({
  ...flat(vm),
  id: `${kind}:proj-a:example-res`,
  kind,
  resource_name: 'example-res',
  ...over,
});

function card(c: RightsizingCandidate): string {
  return renderToStaticMarkup(createElement(CandidateCard, { candidate: c, thresholds: th, currency: 'INR' }));
}

function view(data: RightsizingResponse, kind = '', project = ''): string {
  return renderToStaticMarkup(createElement(RightsizingView, { data, currency: 'INR', kind, project, onKind: noop, onProject: noop, onExport: noop }));
}

describe('CandidateCard', () => {
  it('low-CPU VM: badge, saving, spark with threshold line, window, evidence numbers', () => {
    const html = card(vm);
    expect(html).toContain('data-kind="low_cpu_vm"');
    expect(html).toContain('Low-CPU VM');
    expect(html).toContain('Medium confidence');
    expect(html).toContain('₹891.50');
    expect(html).toContain('data-testid="evidence-spark"');
    expect(html).toContain('<line');
    expect(html).toContain('role="img"');
    expect(html).toContain('Mean CPU per week: 2026-W41: 6.2 %');
    expect(html).toContain('Evidence window 2026-10-05 to 2026-10-25');
    expect(html).toContain('data-testid="candidate-evidence"');
    expect(html).toContain('<dt>2026-W42</dt><dd>7.9 %</dd>');
    expect(html).toContain('50 % of last week');
  });
  it('Cloud Run idle: spark without a threshold line', () => {
    const html = card(run);
    expect(html).toContain('Cloud Run idle minimum instance');
    expect(html).toContain('data-testid="evidence-spark"');
    expect(html).not.toContain('<line');
    expect(html).toContain('Idle cost per week');
  });
  it.each([
    ['idle_disk', 'Idle disk'],
    ['unused_ip', 'Unused static IP'],
    ['stale_snapshot', 'Stale snapshot'],
  ] as const)('%s: badge, no spark, only a text alternative', (kind, label) => {
    const html = card(base(kind, { confidence: 'high', estimated_monthly_saving: 95.7 }));
    expect(html).toContain(`data-kind="${kind}"`);
    expect(html).toContain(label);
    expect(html).toContain('High confidence');
    expect(html).toContain('data-testid="evidence-spark-none"');
    expect(html).not.toContain('<svg');
  });
  it('unknown saving is stated, not zero', () => {
    const html = card(base('idle_disk', { estimated_monthly_saving: null, net_last_week: null }));
    expect(html).toContain('saving unknown');
    expect(html).toContain('no matching billing row');
    expect(html).not.toContain('₹0.00');
  });
  it('a missing week is drawn as a gap with "no data" text', () => {
    const c = { ...vm, evidence: { ...vm.evidence, points: [{ week: '2026-W41', value: null, unit: '%' }, vm.evidence.points[1]!] } };
    expect(card(c)).toContain('2026-W41: no data');
  });
});

describe('RightsizingView', () => {
  it('shows the headline, source failure with hint beside the loaded candidates, thresholds and footnote', () => {
    const html = view(rightsizingFixture);
    expect(html).toContain('potential saving ≈ ₹998.00 / month');
    expect(html).toContain('data-testid="rightsizing-caveat"');
    expect(html).toContain('data-testid="rightsizing-source"');
    expect(html).toContain('data-status="failed"');
    expect(html).toContain('permission_denied');
    expect(html).toContain('Grant roles/compute.viewer');
    expect((html.match(/data-testid="rightsizing-card"/g) ?? []).length).toBe(2);
    expect(html).toContain('VMs under 15 % mean CPU for 3+ weeks');
    expect(html).toContain('are settings');
    expect(html).toContain('× 4.348');
    expect(html).toContain('data-testid="rightsizing-csv"');
    expect(html).not.toMatch(/data-testid="rightsizing-csv"[^>]*disabled/);
  });
  it('kind filter narrows the list and the headline', () => {
    const html = view(rightsizingFixture, 'cloud_run_min_instances');
    expect((html.match(/data-testid="rightsizing-card"/g) ?? []).length).toBe(1);
    expect(html).toContain('₹106.50');
    expect(html).not.toContain('example-vm');
    expect(html).toContain('Filtered view: 1 of 2');
  });
  it('filter with no match shows a filter-specific empty state', () => {
    const html = view(rightsizingFixture, 'idle_disk');
    expect(html).toContain('data-testid="rightsizing-filter-empty"');
    expect(html).not.toContain('data-testid="rightsizing-empty"');
  });
  it('empty state names the weeks to refresh, still shows source status, CSV disabled', () => {
    const html = view({ ...rightsizingFixture, candidates: [], totals: { count: 0, estimated_monthly_saving: 0 } });
    expect(html).toContain('Nothing to right-size with the data we have');
    expect(html).toContain('2026-W41 to 2026-W43');
    expect(html).toContain('No savings candidates');
    expect(html).toContain('data-testid="rightsizing-source"');
    expect(html).toMatch(/data-testid="rightsizing-csv"[^>]*disabled|disabled[^>]*data-testid="rightsizing-csv"/);
  });
  it('all sources ok: no strip', () => {
    const html = view({ ...rightsizingFixture, sources: rightsizingFixture.sources.map((s) => ({ ...s, status: 'ok' as const })) });
    expect(html).not.toContain('data-testid="rightsizing-sources"');
  });
});
