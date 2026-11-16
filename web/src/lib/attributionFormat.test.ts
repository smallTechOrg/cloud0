import { describe, expect, it } from 'vitest';
import { attributionRulesFixture, labelSuggestionsFixture, seedPreviewFixture, unattributedFixture } from './fixtures4';
import {
  MAX_REGEX_LEN,
  aliasesToRows,
  buildLabelSuggestionsCsv,
  buildUnattributedCsv,
  coverageSegments,
  coverageShift,
  duplicateRule,
  groupSeedByTier,
  labelProgress,
  moveRule,
  newRule,
  nextPriority,
  parseInvalidRuleIndex,
  parseSeedNote,
  rowsToAliases,
  ruleFromUnattributed,
  rulesEqual,
  sortByPriority,
  tierOf,
  toWireRule,
  validateRule,
} from './attributionFormat';
import type { AttributionRule } from './attributionTypes';

const rule = (over: Partial<AttributionRule> = {}): AttributionRule => ({ ...newRule(400), match: { field: 'resource_name', op: 'contains', value: 'x' }, action: { product: 'p' }, ...over });

describe('validateRule', () => {
  it('accepts fixture rules', () => {
    for (const r of attributionRulesFixture.rules) expect(validateRule(r)).toEqual([]);
  });
  it('requires value, action, label key', () => {
    expect(validateRule(newRule())).toHaveLength(2); // empty value + empty action
    expect(validateRule(rule({ match: { field: 'label', op: 'equals', value: 'v' } }))[0]).toMatch(/label key/);
    expect(validateRule(rule({ match: { field: 'project', op: 'equals', key: 'k', value: 'v' } }))[0]).toMatch(/Only label/);
  });
  it('rejects shared with product and bad names', () => {
    expect(validateRule(rule({ action: { shared: true, product: 'p' } }))[0]).toMatch(/Shared/);
    expect(validateRule(rule({ action: { product: 'Bad Name!' } }))[0]).toMatch(/Product/);
    expect(validateRule(rule({ action: { product: 'Fine-Name' } }))).toEqual([]);
  });
  it('bounds and compiles regex', () => {
    expect(validateRule(rule({ match: { field: 'sku', op: 'regex', value: 'a'.repeat(MAX_REGEX_LEN + 1) } }))[0]).toMatch(/too long/);
    expect(validateRule(rule({ match: { field: 'sku', op: 'regex', value: '(' } }))[0]).toMatch(/compile/);
    expect(validateRule(rule({ match: { field: 'sku', op: 'regex', value: '(^|-)prod($|-)' } }))).toEqual([]);
  });
  it('rejects negative or fractional priority', () => {
    expect(validateRule(rule({ priority: -1 }))[0]).toMatch(/Priority/);
    expect(validateRule(rule({ priority: 1.5 }))[0]).toMatch(/Priority/);
  });
});

describe('list operations', () => {
  const a = rule({ priority: 100 });
  const b = rule({ priority: 200 });
  const c = rule({ priority: 200 });
  it('moveRule swaps order and priorities', () => {
    const bn = { ...b, note: 'moved' };
    const out = moveRule([a, bn], 1, -1);
    // The moved rule takes the earlier slot's priority; list stays ascending.
    expect(out[0]!.note).toBe('moved');
    expect(out[0]!.priority).toBe(100);
    expect(out[1]!.priority).toBe(200);
  });
  it('moveRule separates tied priorities and ignores edges', () => {
    const out = moveRule([b, c], 1, -1);
    expect(out[0]!.priority).toBeLessThan(out[1]!.priority);
    expect(moveRule([a, b], 0, -1)).toEqual([a, b]);
  });
  it('sortByPriority is stable', () => {
    expect(sortByPriority([b, a, c]).map((r) => r.priority)).toEqual([100, 200, 200]);
  });
  it('duplicateRule drops id and becomes a user rule', () => {
    const d = duplicateRule({ ...attributionRulesFixture.rules[0]!, id: 5, source: 'seed' });
    expect(d.id).toBeNull();
    expect(d.source).toBe('user');
    expect(d.priority).toBe(101);
  });
  it('nextPriority stays in range', () => {
    expect(nextPriority([])).toBe(400);
    expect(nextPriority([rule({ priority: 995 })])).toBe(999);
  });
  it('toWireRule normalises and rulesEqual ignores noise', () => {
    const w = toWireRule(rule({ id: null, match: { field: 'project', op: 'equals', key: '', value: 'v' }, action: { product: ' Foo ', environment: '' } }));
    expect(w.id).toBe(0);
    expect(w.match.key).toBeUndefined();
    expect(w.action).toEqual({ product: 'foo' });
    expect(rulesEqual(attributionRulesFixture.rules, attributionRulesFixture.rules.map((r) => ({ ...r })))).toBe(true);
    expect(rulesEqual([a], [b])).toBe(false);
  });
});

describe('parseInvalidRuleIndex', () => {
  it('finds the index in common phrasings', () => {
    expect(parseInvalidRuleIndex('rules[3]: empty value')).toBe(3);
    expect(parseInvalidRuleIndex('rule 12 is invalid')).toBe(12);
    expect(parseInvalidRuleIndex('invalid rule at index 0: bad op')).toBe(0);
    expect(parseInvalidRuleIndex('something else')).toBeNull();
  });
});

describe('aliases', () => {
  it('round-trips and reports errors', () => {
    expect(aliasesToRows({ b: 'x', a: 'y' }).map((r) => r.key)).toEqual(['a', 'b']);
    expect(rowsToAliases([{ key: ' App ', product: 'Prod-A' }, { key: '', product: '' }]).aliases).toEqual({ app: 'prod-a' });
    expect(rowsToAliases([{ key: '', product: 'x' }]).errors).toHaveLength(1);
    expect(rowsToAliases([{ key: 'k', product: 'Bad Name' }]).errors).toHaveLength(1);
  });
});

describe('seed helpers', () => {
  it('tierOf boundaries', () => {
    expect([99, 100, 199, 200, 300, 599, 600, 899, 900, 999, 1000].map(tierOf)).toEqual(['other', 'label', 'label', 'project', 'name', 'name', 'sku', 'sku', 'shared', 'shared', 'other']);
  });
  it('groups by tier with original indexes', () => {
    const g = groupSeedByTier(seedPreviewFixture.proposed);
    expect(g).toHaveLength(1);
    expect(g[0]!.tier.id).toBe('name');
    expect(g[0]!.rules[0]!.index).toBe(0);
  });
  it('parses notes', () => {
    expect(parseSeedNote('7 resources, 412.30 net')).toEqual({ resources: 7, net: 412.3 });
    expect(parseSeedNote(null)).toBeNull();
    expect(parseSeedNote('hello')).toBeNull();
  });
});

describe('coverage', () => {
  it('segments never exceed 100', () => {
    const s = coverageSegments({ ...seedPreviewFixture.coverage, attributed_pct: 99, shared_pct: 5, unattributed_pct: 5 });
    expect(s.attributed + s.shared + s.unattributed).toBeLessThanOrEqual(100);
  });
  it('shift text', () => {
    expect(coverageShift(seedPreviewFixture.coverage_before, seedPreviewFixture.coverage)).toBe('59.6 % → 91.9 %');
  });
});

describe('unattributed and labels', () => {
  it('builds rules from items', () => {
    const [res, sku] = unattributedFixture.items;
    const r = ruleFromUnattributed(res!, 400);
    expect(r.match).toMatchObject({ field: 'resource_name', op: 'equals', value: 'example-orphan-vm' });
    expect(r.action.product).toBe('example-orphan');
    const s = ruleFromUnattributed(sku!, 400);
    expect(s.match.field).toBe('sku');
    expect(s.priority).toBeGreaterThanOrEqual(600);
    expect(s.action.product).toBe('');
  });
  it('progress text', () => {
    expect(labelProgress(12, 28)).toEqual({ text: '12 of 40 resources labelled', pct: 30 });
    expect(labelProgress(0, 0).pct).toBe(100);
  });
  it('CSV exports quote fields and use CRLF', () => {
    const u = buildUnattributedCsv(unattributedFixture.items);
    expect(u.split('\r\n')).toHaveLength(4);
    expect(u).toContain('example-orphan-vm');
    const l = buildLabelSuggestionsCsv(labelSuggestionsFixture.items);
    expect(l).toContain('"gcloud compute instances add-labels example-vm --project=proj-a --zone=zone-a --labels=product=example-product,env=prod"');
    expect(l.startsWith('project,resource,type')).toBe(true);
  });
});
