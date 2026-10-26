import { describe, expect, it } from 'vitest';
import type { RightsizingCandidate } from './attributionTypes';
import { rightsizingFixture } from './fixtures4';
import {
  buildRightsizingCsv,
  emptyHint,
  evidenceLines,
  filterCandidates,
  headlineCaveat,
  headlineText,
  kindCounts,
  kindLabel,
  problemSources,
  projectsOf,
  sortCandidates,
  sparkGeometry,
  sparkText,
  thresholdsHint,
  totalSaving,
  windowText,
  windowWeeks,
} from './rightsizingFormat';

const [vm, run] = rightsizingFixture.candidates as [RightsizingCandidate, RightsizingCandidate];
const mk = (over: Partial<RightsizingCandidate>): RightsizingCandidate => ({ ...vm, ...over });
const disk = mk({ id: 'idle_disk:proj-b:example-disk', kind: 'idle_disk', project_id: 'proj-b', resource_name: 'example-disk', estimated_monthly_saving: 95.7, evidence: { window_start: '2026-10-25', window_end: '2026-10-25', points: [], notes: [] } });
const unknown = mk({ id: 'unused_ip:proj-b:example-ip', kind: 'unused_ip', project_id: 'proj-b', estimated_monthly_saving: null, net_last_week: null });

describe('totals and sorting', () => {
  it('sums known savings, counts unknown separately', () => {
    const t = totalSaving([vm, run, unknown]);
    expect(t).toEqual({ total: 998, count: 3, unknown: 1 });
    expect(totalSaving([])).toEqual({ total: 0, count: 0, unknown: 0 });
  });
  it('sorts by saving desc, null last, then id', () => {
    const ids = sortCandidates([unknown, run, disk, vm]).map((c) => c.id);
    expect(ids).toEqual([vm.id, run.id, disk.id, unknown.id]);
  });
  it('filters by kind and project and re-sorts', () => {
    const all = [run, disk, vm, unknown];
    expect(filterCandidates(all, 'idle_disk', '').map((c) => c.id)).toEqual([disk.id]);
    expect(filterCandidates(all, '', 'proj-b').map((c) => c.id)).toEqual([disk.id, unknown.id]);
    expect(filterCandidates(all, 'low_cpu_vm', 'proj-b')).toEqual([]);
    expect(filterCandidates(all, '', '')).toHaveLength(4);
  });
  it('lists projects and kind counts in display order', () => {
    expect(projectsOf([run, disk, vm])).toEqual(['proj-a', 'proj-b']);
    expect(kindCounts([run, disk, vm, vm]).map((k) => `${k.kind}:${k.count}`)).toEqual(['low_cpu_vm:2', 'idle_disk:1', 'cloud_run_min_instances:1']);
  });
});

describe('text', () => {
  it('headline uses the currency and the caveat mentions unknown candidates', () => {
    expect(headlineText(totalSaving([vm, run]), 'INR')).toBe('potential saving ≈ ₹998.00 / month');
    expect(headlineText(totalSaving([]), 'INR')).toBe('No savings candidates');
    expect(headlineCaveat(totalSaving([vm]))).toContain('estimate');
    expect(headlineCaveat(totalSaving([vm, unknown]))).toContain('1 candidate has no matching billing row');
  });
  it('states thresholds and labels kinds', () => {
    expect(thresholdsHint(rightsizingFixture.thresholds)).toContain('VMs under 15 % mean CPU for 3+ weeks');
    expect(kindLabel('unused_ip')).toBe('Unused static IP');
    expect(kindLabel('future_kind')).toBe('future_kind');
  });
  it('window and evidence lines', () => {
    expect(windowText(vm)).toBe('Evidence window 2026-10-05 to 2026-10-25');
    expect(windowText(disk)).toBe('Observed 2026-10-25');
    const lines = evidenceLines(vm, 'INR');
    expect(lines.slice(0, 3)).toEqual([
      { label: '2026-W41', value: '6.2 %' },
      { label: '2026-W42', value: '7.9 %' },
      { label: '2026-W43', value: '5.1 %' },
    ]);
    expect(lines.find((l) => l.label === 'Net cost last week')?.value).toBe('₹410.00');
    expect(evidenceLines(unknown, 'INR').find((l) => l.label === 'Net cost last week')?.value).toBe('no matching billing row');
  });
  it('spark text is a full alternative', () => {
    expect(sparkText(vm, 15)).toBe('Mean CPU per week: 2026-W41: 6.2 %; 2026-W42: 7.9 %; 2026-W43: 5.1 %. Threshold 15 %.');
    expect(sparkText(run, null)).toContain('Idle cost per week: 2026-W43: 24.5 INR idle');
    expect(sparkText(disk, null)).toBe('No weekly series for this kind of hint.');
  });
});

describe('spark geometry', () => {
  it('scales bars to the larger of values and threshold', () => {
    const g = sparkGeometry(vm.evidence.points, 15, 90, 30);
    expect(g.max).toBe(15);
    expect(g.thresholdY).toBe(2); // threshold at the top of the inner box
    expect(g.bars).toHaveLength(3);
    expect(g.bars.every((b) => !b.over)).toBe(true);
    expect(g.bars[1]!.height).toBeGreaterThan(g.bars[0]!.height);
    expect(g.bars[0]!.height).toBeCloseTo((6.2 / 15) * 26, 1);
  });
  it('marks values at or above the threshold and leaves gaps for null', () => {
    const g = sparkGeometry([{ week: '2026-W41', value: 20, unit: '%' }, { week: '2026-W42', value: null, unit: '%' }], 15, 60, 30);
    expect(g.max).toBe(20);
    expect(g.bars[0]!.over).toBe(true);
    expect(g.bars[1]!.height).toBe(0);
    expect(g.bars[1]!.over).toBe(false);
  });
  it('has no threshold line for cost series and survives all-zero data', () => {
    const g = sparkGeometry([{ week: '2026-W43', value: 0, unit: 'x' }], null);
    expect(g.thresholdY).toBeNull();
    expect(g.bars[0]!.height).toBe(0);
    expect(sparkGeometry([], null).bars).toEqual([]);
  });
});

describe('sources, windows, empty hint, CSV', () => {
  it('keeps only non-ok sources', () => {
    expect(problemSources(rightsizingFixture.sources).map((s) => s.source)).toEqual(['compute_inventory']);
  });
  it('computes the window weeks across a year boundary', () => {
    expect(windowWeeks('2026-W43', 3)).toEqual(['2026-W41', '2026-W42', '2026-W43']);
    expect(windowWeeks('2026-W01', 3)).toEqual(['2025-W51', '2025-W52', '2026-W01']);
  });
  it('empty hint names the weeks to refresh', () => {
    expect(emptyHint('2026-W43', rightsizingFixture.thresholds, true)).toContain('2026-W41 to 2026-W43');
    expect(emptyHint('2026-W43', rightsizingFixture.thresholds, false)).toContain('Refresh 2026-W43 first');
  });
  it('CSV has a header, quotes commas and leaves unknown money blank', () => {
    const csv = buildRightsizingCsv([vm, unknown]);
    const lines = csv.trimEnd().split('\r\n');
    expect(lines[0]).toBe('kind,project,resource,location,confidence,net_last_week,estimated_monthly_saving,window_start,window_end,recommendation,saving_basis');
    expect(lines[1]).toContain('low_cpu_vm,proj-a,example-vm,zone-a,medium,410.00,891.50');
    expect(lines[1]).toContain('"Review the workload; consider a smaller machine type or stopping the instance. cloud0 makes no changes."'.replace(/^"|"$/g, ''));
    expect(lines[2]).toContain(',medium,,,');
    expect(lines).toHaveLength(3);
  });
});
