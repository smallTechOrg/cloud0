import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from './api';
import {
  applySeed,
  explainResource,
  getCostByEnvironment,
  getCostByProduct,
  getLabelSuggestions,
  getRightsizing,
  getTargets,
  getUnattributed,
  proposeRules,
  putAttributionRules,
  putTargets,
} from './attributionApi';
import {
  attributionRulesFixture,
  costByEnvironmentFixture,
  costByProductFixture,
  explainFixture,
  proposeFixture,
  rightsizingFixture,
  targetsFixture,
} from './fixtures4';

function mockFetch(status: number, body: unknown) {
  const fn = vi.fn(async (_url: string, _init?: RequestInit) => new Response(JSON.stringify(body), { status, headers: { 'X-Request-Id': 'rid-1' } }));
  vi.stubGlobal('fetch', fn);
  return fn;
}

const calls = (fn: ReturnType<typeof mockFetch>) => fn.mock.calls as unknown as [string, RequestInit | undefined][];

afterEach(() => vi.unstubAllGlobals());

describe('attribution client URLs and methods', () => {
  it('builds query strings and skips undefined params', async () => {
    const fn = mockFetch(200, costByProductFixture);
    await getCostByProduct({ week: '2026-W43', allocate: 'proportional', env: '(no environment)' });
    expect(calls(fn)[0]![0]).toBe('/api/v1/cost/by-product?week=2026-W43&allocate=proportional&env=%28no+environment%29');
    await getCostByEnvironment({ product: '(shared)' });
    expect(calls(fn)[1]![0]).toBe('/api/v1/cost/by-environment?product=%28shared%29');
    await getUnattributed();
    expect(calls(fn)[2]![0]).toBe('/api/v1/attribution/unattributed');
    await getLabelSuggestions({ week: '2026-W43', limit: 50 });
    expect(calls(fn)[3]![0]).toBe('/api/v1/attribution/label-suggestions?week=2026-W43&limit=50');
    await getTargets({ month: '2026-10' });
    expect(calls(fn)[4]![0]).toBe('/api/v1/targets?month=2026-10');
    await getRightsizing({ week: '2026-W43' });
    expect(calls(fn)[5]![0]).toBe('/api/v1/rightsizing?week=2026-W43');
  });

  it('sends JSON bodies with PUT/POST', async () => {
    const fn = mockFetch(200, attributionRulesFixture);
    await putAttributionRules({ rules: attributionRulesFixture.rules, allocate: 'even' });
    let init = calls(fn)[0]![1]!;
    expect(calls(fn)[0]![0]).toBe('/api/v1/attribution/rules');
    expect(init.method).toBe('PUT');
    expect(JSON.parse(init.body as string).allocate).toBe('even');

    fn.mockImplementationOnce(async () => new Response(JSON.stringify(explainFixture), { status: 200 }));
    await explainResource({ resource: 'example-web' });
    init = calls(fn)[1]![1]!;
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ resource: 'example-web' });

    fn.mockImplementationOnce(async () => new Response(JSON.stringify(targetsFixture), { status: 200 }));
    await putTargets({ month_from: '2026-11', targets: [{ product: '*', amount: null }] });
    expect(calls(fn)[2]![0]).toBe('/api/v1/targets');
    expect(JSON.parse(calls(fn)[2]![1]!.body as string).targets[0].amount).toBeNull();

    fn.mockImplementationOnce(async () => new Response(JSON.stringify({ applied: true }), { status: 200 }));
    await applySeed({ aliases: { a: 'b' } });
    expect(calls(fn)[3]![0]).toBe('/api/v1/attribution/seed/apply');
  });

  it('surfaces proposer_not_configured as an ApiError with status 503', async () => {
    mockFetch(503, { error: { code: 'proposer_not_configured', message: 'Rule proposer is not configured.', hint: 'Set CLOUD0_OPENROUTER_API_KEY in .env and restart to enable LLM rule proposals.' } });
    const err = await proposeRules().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(503);
    expect((err as ApiError).code).toBe('proposer_not_configured');
    expect((err as ApiError).requestId).toBe('rid-1');
  });

  it('surfaces invalid_rule as a 400 ApiError', async () => {
    mockFetch(400, { error: { code: 'invalid_rule', message: 'rule 2: regex does not compile', hint: null } });
    const err = (await putAttributionRules({ rules: [] }).catch((e: unknown) => e)) as ApiError;
    expect(err.code).toBe('invalid_rule');
    expect(err.status).toBe(400);
  });
});

describe('fixtures follow the spec invariants', () => {
  it('breakdown groups reconcile to the total and the cost table', () => {
    for (const f of [costByProductFixture, costByEnvironmentFixture]) {
      const sum = f.groups.reduce((s, g) => s + g.net, 0);
      expect(Math.abs(sum - f.reconciliation.cost_table_total)).toBeLessThanOrEqual(0.005);
      expect(f.reconciliation.breakdown_total).toBeCloseTo(sum, 6);
      expect(f.total.cells.total?.net).toBeCloseTo(sum, 6);
      for (const g of f.groups) expect(g.net).toBeCloseTo(g.direct_net + g.allocated_net, 6);
      // shared and unattributed buckets sort last
      expect(f.groups.slice(-2).map((g) => g.kind)).toEqual(['shared', 'unattributed']);
    }
  });

  it('coverage percentages sum to 100', () => {
    const c = costByProductFixture.coverage;
    expect(c.attributed_pct + c.shared_pct + c.unattributed_pct).toBeCloseTo(100, 1);
    expect(c.attributed_net + c.shared_net + c.unattributed_net).toBeCloseTo(c.total_net, 6);
  });

  it('rightsizing totals and low-cpu evidence follow the thresholds', () => {
    const r = rightsizingFixture;
    expect(r.totals.estimated_monthly_saving).toBeCloseTo(r.candidates.reduce((s, c) => s + (c.estimated_monthly_saving ?? 0), 0), 6);
    const vm = r.candidates.find((c) => c.kind === 'low_cpu_vm')!;
    expect(vm.evidence.points).toHaveLength(r.thresholds.weeks);
    expect(vm.evidence.points.every((p) => p.value !== null && p.value < r.thresholds.cpu_pct)).toBe(true);
    expect(r.monthly_factor).toBeCloseTo(365.25 / 84, 5);
  });

  it('target product shares plus buckets add up to the account net', () => {
    const t = targetsFixture;
    const sum = t.products.reduce((s, p) => s + p.net, 0) + t.shared.net + t.unattributed.net;
    expect(Math.abs(sum - t.account.net)).toBeLessThanOrEqual(0.005);
  });

  it('propose fixture never carries an id (proposals are not stored)', () => {
    expect(proposeFixture.proposals.every((p) => p.rule.id === null && p.rule.source === 'proposed')).toBe(true);
  });
});
