import { describe, expect, it } from 'vitest';
import type { AttributionRule, ResourceGroup } from '@/lib/attributionTypes';
import { ASSIGN_PRIORITY, applyDecision, labelItems, labelValue } from './AssignProducts';

function group(over: Partial<ResourceGroup> = {}): ResourceGroup {
  return {
    id: 'g1',
    name: 'web-vm',
    kind: 'resource',
    project_id: 'proj',
    services: ['Compute Engine'],
    net: 100,
    share_pct: 10,
    labels: {},
    product: '',
    environment: '',
    shared: false,
    source: 'none',
    rule_id: null,
    match: { field: 'resource_name', op: 'equals', value: 'web-vm' },
    members: [],
    targets: [
      { project: 'proj', type: 'compute_instance', name: 'web-vm', location: 'zone-a', resource: 'x', writable: true },
      { project: 'proj', type: 'compute_disk', name: 'web-vm', location: 'zone-a', resource: 'web-vm', writable: true },
      { project: 'proj', type: 'artifact_repository', name: 'repo', resource: 'repo', writable: false, note: 'n' },
    ],
    suggested: null,
    ...over,
  };
}

describe('labelValue', () => {
  it('normalises to the label character set', () => {
    expect(labelValue('  My Product! ')).toBe('my-product');
    expect(labelValue('a_b-9')).toBe('a_b-9');
    expect(labelValue('')).toBe('');
  });
});

describe('applyDecision', () => {
  it('adds a user rule in the assignment tier matching the group', () => {
    const out = applyDecision([], [group()], { product: 'web', environment: 'prod', shared: false });
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ priority: ASSIGN_PRIORITY, source: 'user', enabled: true, action: { product: 'web', environment: 'prod' }, match: { field: 'resource_name', op: 'equals', value: 'web-vm' } });
  });

  it('replaces an earlier assignment for the same match instead of duplicating it', () => {
    const first = applyDecision([], [group()], { product: 'web', environment: '', shared: false });
    first[0]!.id = 7;
    const second = applyDecision(first, [group()], { product: 'api', environment: 'staging', shared: false });
    expect(second).toHaveLength(1);
    expect(second[0]!.id).toBe(7);
    expect(second[0]!.action).toEqual({ product: 'api', environment: 'staging' });
  });

  it('keeps unrelated rules and marks shared without a product', () => {
    const other: AttributionRule = { id: 1, priority: 400, match: { field: 'service', op: 'equals', value: 'X' }, action: { shared: true }, enabled: true, source: 'seed', note: null };
    const out = applyDecision([other], [group()], { product: 'ignored', environment: '', shared: true });
    expect(out[0]).toBe(other);
    expect(out[1]!.action).toEqual({ shared: true });
  });
});

describe('labelItems', () => {
  it('targets every writable GCP resource of the group once, with product and env labels', () => {
    const items = labelItems([group(), group()], { product: 'web', environment: 'prod', shared: false });
    expect(items).toHaveLength(2);
    expect(items.map((i) => i.target.type).sort()).toEqual(['compute_disk', 'compute_instance']);
    expect(items[0]!.labels).toEqual({ product: 'web', env: 'prod' });
  });

  it('labels shared as product=shared and returns nothing without a decision', () => {
    expect(labelItems([group()], { product: '', environment: '', shared: true })[0]!.labels).toEqual({ product: 'shared' });
    expect(labelItems([group()], { product: '', environment: '', shared: false })).toEqual([]);
  });
});
