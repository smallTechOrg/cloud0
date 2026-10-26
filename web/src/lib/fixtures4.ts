// One realistic example response per Phase 3 endpoint (spec/api.md →
// "Attribution additions"), placeholder names only, so frontend work can render
// before the API exists. Not used by production code paths.

import type {
  BreakdownResponse,
  ExplainResponse,
  LabelSuggestionsResponse,
  ProposeResponse,
  RightsizingResponse,
  RulesResponse,
  SeedApplied,
  SeedPreview,
  TargetsResponse,
  UnattributedResponse,
} from './attributionTypes';

const snapshot = { id: 13, fetched_at: '2026-10-26T09:05:00Z', status_at_fetch: 'settling' as const, run_id: 8, row_count: 2345, stale: false };

const coverage = {
  total_net: 2012.5,
  attributed_net: 1850,
  shared_net: 90,
  unattributed_net: 72.5,
  attributed_pct: 91.9,
  shared_pct: 4.5,
  unattributed_pct: 3.6,
};

export const attributionRulesFixture: RulesResponse = {
  rules: [
    { id: 1, priority: 100, match: { field: 'label', op: 'equals', key: 'app', value: 'example-alias' }, action: { product: 'example-product' }, enabled: true, source: 'seed', note: null },
    { id: 2, priority: 310, match: { field: 'resource_name', op: 'contains', value: 'example-product' }, action: { product: 'example-product' }, enabled: true, source: 'seed', note: null },
    { id: 3, priority: 320, match: { field: 'resource_name', op: 'regex', value: '(^|[-_/])prod($|[-_/])' }, action: { environment: 'prod' }, enabled: true, source: 'seed', note: null },
    { id: 4, priority: 900, match: { field: 'service', op: 'equals', value: 'VM Manager' }, action: { shared: true }, enabled: true, source: 'user', note: 'OS patch management, not per product' },
  ],
  allocate: 'none',
  aliases: { 'example-alias': 'example-product' },
  proposer: { enabled: false, model: null },
  updated_at: '2026-10-27T09:00:00Z',
};

export const seedPreviewFixture: SeedPreview = {
  week: '2026-W43',
  snapshot,
  labels_captured: true,
  proposed: [
    { id: null, priority: 310, match: { field: 'resource_name', op: 'contains', value: 'example-product' }, action: { product: 'example-product' }, enabled: true, source: 'seed', note: '7 resources, 412.30 net' },
  ],
  counts: { rules: 14, label: 3, project: 0, resource_name: 9, sku_service: 2, products: 6, environments: 3, kept_user_rules: 1, replaced_seed_rules: 12 },
  coverage,
  coverage_before: { total_net: 2012.5, attributed_net: 1200, shared_net: 0, unattributed_net: 812.5, attributed_pct: 59.6, shared_pct: 0, unattributed_pct: 40.4 },
  products: [{ product: 'example-product', net: 412.3, resources: 7 }],
  warnings: [],
};

export const seedAppliedFixture: SeedApplied = {
  applied: true,
  rules: attributionRulesFixture.rules,
  counts: seedPreviewFixture.counts,
  coverage,
  warnings: [],
};

export const unattributedFixture: UnattributedResponse = {
  week: '2026-W43',
  scope: 'all',
  snapshot,
  labels_captured: true,
  total_net: 2012.5,
  unattributed_net: 43.3,
  unattributed_pct: 2.15,
  item_count: 2,
  truncated: false,
  items: [
    {
      kind: 'resource',
      key: 'example-orphan-vm',
      project_id: 'proj-a',
      service: 'Compute Engine',
      resource_name: 'example-orphan-vm',
      resource_global_name: '//compute.googleapis.com/projects/proj-a/zones/zone-a/instances/example-orphan-vm',
      sku: null,
      net: 31.2,
      share_pct: 1.55,
      line_items: 168,
      reason: 'no_matching_rule',
      suggested: { product: 'example-orphan', environment: null, confidence: 'low', basis: "name token 'example-orphan'" },
      evidence: ['resource has no label product/env', 'no enabled rule matched'],
    },
    {
      kind: 'sku',
      key: 'Network Inter Region Egress',
      project_id: 'proj-a',
      service: 'Networking',
      resource_name: null,
      resource_global_name: null,
      sku: 'Network Inter Region Egress',
      net: 12.1,
      share_pct: 0.6,
      line_items: 40,
      reason: 'no_resource_name',
      suggested: null,
      evidence: ['billing line has no resource name; add a service/SKU rule or mark it shared'],
    },
  ],
};

export const explainFixture: ExplainResponse = {
  week: '2026-W43',
  resource: 'example-web',
  matches: 1,
  results: [
    {
      project_id: 'proj-a',
      service: 'Cloud Run',
      resource_name: 'example-web',
      resource_global_name: '//run.googleapis.com/projects/proj-a/locations/region-a/services/example-web',
      labels: { env: 'staging' },
      net: 18.4,
      evaluation: { product: 'example-product', environment: 'staging', shared: false, rule_id: 2, environment_rule_id: 0, evidence: ['resource_name contains "example-product"', 'label env=staging'] },
      trail: [
        { rule_id: 0, builtin: true, description: 'label product', matched: false, applied: null, reason: "no label 'product'" },
        { rule_id: 0, builtin: true, description: 'label env|environment', matched: true, applied: 'environment', reason: 'label env=staging' },
        { rule_id: 1, builtin: false, priority: 100, description: 'label app equals "example-alias" → product example-product', matched: false, applied: null, reason: "no label 'app'" },
        { rule_id: 2, builtin: false, priority: 310, description: 'resource_name contains "example-product" → product example-product', matched: true, applied: 'product', reason: 'resource_global_name contains it' },
      ],
    },
  ],
};

const cell = (net: number, gross = net, credits = 0) => ({ net, gross, credits });

export const costByProductFixture: BreakdownResponse = {
  dimension: 'product',
  week: '2026-W43',
  week_start: '2026-10-19',
  week_end: '2026-10-25',
  week_status: 'settling',
  scope: 'all',
  allocate: 'none',
  filter: { env: null, product: null },
  generated_at: '2026-10-26T09:00:00Z',
  snapshot,
  labels_captured: true,
  columns: [
    { key: 'compute_engine', label: 'Compute Engine', kind: 'service', services: ['Compute Engine'] },
    { key: 'cloud_run', label: 'Cloud Run', kind: 'service', services: ['Cloud Run'] },
    { key: 'total', label: 'Total GCP', kind: 'total' },
  ],
  groups: [
    { key: 'example-product', label: 'example-product', kind: 'product', cells: { compute_engine: cell(900), cloud_run: cell(512.5, 520, -7.5), total: cell(1412.5, 1420, -7.5) }, direct_net: 1412.5, allocated_net: 0, net: 1412.5, share_pct: 70.19, rule_ids: [2, 1] },
    { key: 'example-other', label: 'example-other', kind: 'product', cells: { compute_engine: cell(337.5), total: cell(337.5) }, direct_net: 337.5, allocated_net: 0, net: 337.5, share_pct: 16.77, rule_ids: [5] },
    { key: '(shared)', label: '(shared)', kind: 'shared', cells: { total: cell(200) }, direct_net: 200, allocated_net: 0, net: 200, share_pct: 9.94, rule_ids: [4] },
    { key: '(unattributed)', label: '(unattributed)', kind: 'unattributed', cells: { total: cell(62.5) }, direct_net: 62.5, allocated_net: 0, net: 62.5, share_pct: 3.11, rule_ids: [] },
  ],
  total: { cells: { compute_engine: cell(1237.5), cloud_run: cell(512.5, 520, -7.5), total: cell(2012.5, 2020, -7.5) } },
  coverage: { total_net: 2012.5, attributed_net: 1750, shared_net: 200, unattributed_net: 62.5, attributed_pct: 86.96, shared_pct: 9.94, unattributed_pct: 3.11 },
  reconciliation: { cost_table_total: 2012.5, breakdown_total: 2012.5, diff: 0, ok: true },
  notes: [],
};

export const costByEnvironmentFixture: BreakdownResponse = {
  ...costByProductFixture,
  dimension: 'environment',
  groups: [
    { key: 'prod', label: 'prod', kind: 'environment', cells: { total: cell(1300) }, direct_net: 1300, allocated_net: 0, net: 1300, share_pct: 64.6, rule_ids: [3] },
    { key: 'staging', label: 'staging', kind: 'environment', cells: { total: cell(450) }, direct_net: 450, allocated_net: 0, net: 450, share_pct: 22.36, rule_ids: [0] },
    { key: '(shared)', label: '(shared)', kind: 'shared', cells: { total: cell(200) }, direct_net: 200, allocated_net: 0, net: 200, share_pct: 9.94, rule_ids: [4] },
    { key: '(no environment)', label: '(no environment)', kind: 'unattributed', cells: { total: cell(62.5) }, direct_net: 62.5, allocated_net: 0, net: 62.5, share_pct: 3.11, rule_ids: [] },
  ],
};

export const labelSuggestionsFixture: LabelSuggestionsResponse = {
  week: '2026-W43',
  scope: 'all',
  snapshot,
  labels_captured: true,
  count: 2,
  done_count: 5,
  items: [
    {
      project_id: 'proj-a',
      resource_name: 'example-vm',
      resource_global_name: '//compute.googleapis.com/projects/proj-a/zones/zone-a/instances/example-vm',
      resource_type: 'compute_instance',
      region_or_zone: 'zone-a',
      net: 120.4,
      labels: { product: 'example-product', env: 'prod' },
      missing: ['product', 'env'],
      basis: { product_rule_id: 2, environment_rule_id: 3 },
      command: 'gcloud compute instances add-labels example-vm --project=proj-a --zone=zone-a --labels=product=example-product,env=prod',
      risk: 'Metadata-only change; no restart. Label values are visible in billing exports.',
    },
    {
      project_id: 'proj-a',
      resource_name: 'example-web',
      resource_global_name: '//run.googleapis.com/projects/proj-a/locations/region-a/services/example-web',
      resource_type: 'cloud_run_service',
      region_or_zone: 'region-a',
      net: 18.4,
      labels: { product: 'example-product' },
      missing: ['product'],
      basis: { product_rule_id: 2, environment_rule_id: null },
      command: 'gcloud run services update example-web --project=proj-a --region=region-a --update-labels=product=example-product',
      risk: 'Creates a new Cloud Run revision; do it in a quiet window.',
    },
  ],
};

export const targetsFixture: TargetsResponse = {
  month: '2026-10',
  month_start: '2026-10-01',
  month_end: '2026-10-31',
  currency: 'INR',
  generated_at: '2026-10-26T09:00:00Z',
  month_status: 'partial',
  covered_until: '2026-10-25',
  share_weeks: ['2026-W41', '2026-W42', '2026-W43'],
  account: { product: '*', target: 25000, month_from: '2026-09', net: 18200.5, projected: 22100.3, pct: 72.8, remaining: 6799.5, status: 'on_track' },
  products: [
    { product: 'example-product', target: 6000, month_from: '2026-10', net: 12740.35, projected: 15470.2, pct: 212.3, remaining: -6740.35, status: 'over', share_pct: 70 },
    { product: 'example-other', target: null, month_from: null, net: 3640.1, projected: 4420.1, pct: null, remaining: null, status: 'no_target', share_pct: 20 },
  ],
  shared: { net: 1274.0, projected: 1547.0, share_pct: 7 },
  unattributed: { net: 546.05, projected: 663.0, share_pct: 3 },
  history: [{ product: 'example-product', month_from: '2026-10', amount: 6000, created_at: '2026-10-20T08:00:00Z' }],
};

export const rightsizingFixture: RightsizingResponse = {
  week: '2026-W43',
  scope: 'all',
  currency: 'INR',
  generated_at: '2026-10-26T09:00:00Z',
  monthly_factor: 4.348214,
  thresholds: { cpu_pct: 15, weeks: 3, stale_snapshot_days: 30, idle_run_min_net: 1 },
  sources: [
    { source: 'monitoring_vm', status: 'ok', error_code: null, message: null, fix_hint: null },
    { source: 'compute_inventory', status: 'failed', error_code: 'permission_denied', message: 'caller lacks compute.disks.list', fix_hint: 'Grant roles/compute.viewer to the service account and enable compute.googleapis.com.' },
  ],
  candidates: [
    {
      id: 'low_cpu_vm:proj-a:example-vm',
      kind: 'low_cpu_vm',
      project_id: 'proj-a',
      resource_name: 'example-vm',
      resource_type: 'compute_instance',
      location: 'zone-a',
      confidence: 'medium',
      evidence: {
        window_start: '2026-10-05',
        window_end: '2026-10-25',
        points: [
          { week: '2026-W41', value: 6.2, unit: '%' },
          { week: '2026-W42', value: 7.9, unit: '%' },
          { week: '2026-W43', value: 5.1, unit: '%' },
        ],
        notes: ['mean CPU < 15 % in each of the last 3 stored weeks'],
      },
      net_last_week: 410,
      estimated_monthly_saving: 891.5,
      saving_basis: "50 % of last week's instance core+RAM net (205.00) × 4.348 weeks/month — assumes one machine size down",
      action: 'Review the workload; consider a smaller machine type or stopping the instance. cloud0 makes no changes.',
    },
    {
      id: 'cloud_run_min_instances:proj-a:example-web',
      kind: 'cloud_run_min_instances',
      project_id: 'proj-a',
      resource_name: 'example-web',
      resource_type: 'cloud_run_service',
      location: 'region-a',
      confidence: 'medium',
      evidence: { window_start: '2026-10-05', window_end: '2026-10-25', points: [{ week: '2026-W43', value: 24.5, unit: 'INR idle' }], notes: ['idle CPU/memory SKUs billed while no request is served'] },
      net_last_week: 24.5,
      estimated_monthly_saving: 106.5,
      saving_basis: "last week's idle-instance net × 4.348 weeks/month",
      action: 'Lower min instances to 0 if cold starts are acceptable.',
    },
  ],
  totals: { count: 2, estimated_monthly_saving: 998 },
};

export const proposeFixture: ProposeResponse = {
  week: '2026-W43',
  model: 'anthropic/claude-sonnet-4-6',
  latency_ms: 2400,
  input_tokens: 1850,
  output_tokens: 420,
  proposals: [
    {
      rule: { id: null, priority: 330, match: { field: 'resource_name', op: 'contains', value: 'example-orphan' }, action: { product: 'example-orphan' }, enabled: true, source: 'proposed', note: null },
      confidence: 'medium',
      rationale: "Three resources share the token 'example-orphan'.",
      covers: 3,
    },
  ],
  dropped: 0,
  coverage_if_applied: { ...coverage, attributed_net: 1900, unattributed_net: 22.5, attributed_pct: 94.4, unattributed_pct: 1.1 },
};
