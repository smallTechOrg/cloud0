// Phase 3 attribution + insights types (spec/api.md → "Attribution additions").
// Mirrors internal/attribution/types.go. Money values are numbers in
// config.currency. `null` fields are typed `| null`, never optional.

import type { Scope, WeekID, WeekStatus } from './types';

export type MatchField = 'label' | 'project' | 'resource_name' | 'sku' | 'service';
export type MatchOp = 'equals' | 'contains' | 'prefix' | 'regex';
export type RuleSource = 'seed' | 'user' | 'proposed';
export type AllocateMode = 'none' | 'even' | 'proportional';
export type Confidence = 'high' | 'medium' | 'low';

export interface RuleMatch {
  field: MatchField;
  op: MatchOp;
  /** Required for (and only for) field `label`. */
  key?: string;
  value: string;
}

export interface RuleAction {
  product?: string;
  environment?: string;
  /** Mutually exclusive with `product`. */
  shared?: boolean;
}

export interface AttributionRule {
  /** null for rules not yet stored (seed previews, proposals, new rules in a PUT). */
  id: number | null;
  priority: number;
  match: RuleMatch;
  action: RuleAction;
  enabled: boolean;
  source: RuleSource;
  note: string | null;
}

export interface RulesResponse {
  rules: AttributionRule[];
  allocate: AllocateMode;
  aliases: Record<string, string>;
  proposer: { enabled: boolean; model: string | null };
  updated_at: string | null;
}

export interface RulesUpdate {
  rules: AttributionRule[];
  allocate?: AllocateMode;
  aliases?: Record<string, string>;
}

export interface AttributionSnapshotRef {
  id: number;
  fetched_at: string;
  status_at_fetch: WeekStatus;
  run_id: number;
  row_count: number;
  stale: boolean;
}

export interface Coverage {
  total_net: number;
  attributed_net: number;
  shared_net: number;
  unattributed_net: number;
  attributed_pct: number;
  shared_pct: number;
  unattributed_pct: number;
}

export interface SeedRequest {
  week?: WeekID;
  scope?: Scope;
  aliases?: Record<string, string>;
  shared_tokens?: string[];
}

export interface SeedCounts {
  rules: number;
  label: number;
  project: number;
  resource_name: number;
  sku_service: number;
  products: number;
  environments: number;
  kept_user_rules: number;
  replaced_seed_rules: number;
}

export interface SeedPreview {
  week: WeekID;
  snapshot: AttributionSnapshotRef | null;
  labels_captured: boolean;
  proposed: AttributionRule[];
  counts: SeedCounts;
  coverage: Coverage;
  coverage_before: Coverage;
  products: { product: string; net: number; resources: number }[];
  warnings: string[];
}

export interface SeedApplied {
  applied: boolean;
  rules: AttributionRule[];
  counts: SeedCounts;
  coverage: Coverage;
  warnings: string[];
}

export type UnattributedReason = 'no_resource_name' | 'no_matching_rule' | 'environment_only';

export interface SuggestedAttribution {
  product: string;
  environment: string | null;
  confidence: Confidence;
  basis: string;
}

export interface UnattributedItem {
  kind: 'resource' | 'sku';
  key: string;
  project_id: string;
  service: string;
  resource_name: string | null;
  resource_global_name: string | null;
  sku: string | null;
  net: number;
  share_pct: number;
  line_items: number;
  reason: UnattributedReason;
  suggested: SuggestedAttribution | null;
  evidence: string[];
}

export interface UnattributedResponse {
  week: WeekID;
  scope: Scope;
  snapshot: AttributionSnapshotRef | null;
  labels_captured: boolean;
  total_net: number;
  unattributed_net: number;
  unattributed_pct: number;
  item_count: number;
  truncated: boolean;
  items: UnattributedItem[];
}

export interface ExplainRequest {
  resource: string;
  week?: WeekID;
  scope?: Scope;
}

export interface Evaluation {
  product: string;
  environment: string;
  shared: boolean;
  /** 0 = built-in label rule, null = no decision. */
  rule_id: number | null;
  environment_rule_id: number | null;
  evidence: string[];
}

export interface TrailStep {
  rule_id: number;
  builtin: boolean;
  priority?: number;
  description: string;
  matched: boolean;
  applied: 'product' | 'environment' | 'shared' | null;
  reason: string;
}

export interface ExplainResult {
  project_id: string;
  service: string;
  resource_name: string | null;
  resource_global_name: string | null;
  labels: Record<string, string>;
  net: number;
  evaluation: Evaluation;
  trail: TrailStep[];
}

export interface ExplainResponse {
  week: WeekID;
  resource: string;
  matches: number;
  results: ExplainResult[];
}

export type GroupKind = 'product' | 'environment' | 'shared' | 'unattributed';

export interface BreakdownColumn {
  key: string;
  label: string;
  kind: 'service' | 'total';
  services?: string[];
  extra?: boolean;
}

export interface BreakdownCell {
  net: number;
  gross: number;
  credits: number;
}

export interface BreakdownGroup {
  key: string;
  label: string;
  kind: GroupKind;
  /** Missing service keys mean 0; `total` is always present. */
  cells: Record<string, BreakdownCell>;
  direct_net: number;
  allocated_net: number;
  net: number;
  share_pct: number;
  rule_ids: number[];
}

export interface BreakdownResponse {
  dimension: 'product' | 'environment';
  week: WeekID;
  week_start: string;
  week_end: string;
  week_status: WeekStatus;
  scope: Scope;
  allocate: AllocateMode;
  filter: { env: string | null; product: string | null };
  generated_at: string;
  snapshot: AttributionSnapshotRef | null;
  labels_captured: boolean;
  columns: BreakdownColumn[];
  groups: BreakdownGroup[];
  total: { cells: Record<string, BreakdownCell> };
  coverage: Coverage;
  reconciliation: { cost_table_total: number; breakdown_total: number; diff: number; ok: boolean };
  notes: string[];
}

export type ResourceType =
  | 'compute_instance'
  | 'compute_disk'
  | 'compute_address'
  | 'cloud_run_service'
  | 'cloud_run_job'
  | 'gcs_bucket'
  | 'cloud_sql_instance'
  | 'artifact_repository'
  | 'unknown';

export interface LabelSuggestion {
  project_id: string;
  resource_name: string;
  resource_global_name: string | null;
  resource_type: ResourceType;
  region_or_zone: string | null;
  net: number;
  /** Only the labels to add. */
  labels: Record<string, string>;
  missing: ('product' | 'env')[];
  basis: { product_rule_id: number | null; environment_rule_id: number | null };
  /** null for unknown types / unparsed location. cloud0 never runs it. */
  command: string | null;
  risk: string;
}

export interface LabelSuggestionsResponse {
  week: WeekID;
  scope: Scope;
  snapshot: AttributionSnapshotRef | null;
  labels_captured: boolean;
  count: number;
  done_count: number;
  items: LabelSuggestion[];
}

export type TargetStatus = 'on_track' | 'at_risk' | 'over' | 'no_target' | 'no_data';

export interface TargetRow {
  /** `*` = the account target. */
  product: string;
  target: number | null;
  month_from: string | null;
  net: number;
  projected: number;
  pct: number | null;
  remaining: number | null;
  status: TargetStatus;
  share_pct?: number;
}

export interface TargetsResponse {
  month: string;
  month_start: string;
  month_end: string;
  currency: string;
  generated_at: string;
  month_status: string;
  covered_until: string | null;
  share_weeks: WeekID[];
  account: TargetRow;
  products: TargetRow[];
  shared: { net: number; projected: number; share_pct: number };
  unattributed: { net: number; projected: number; share_pct: number };
  history: { product: string; month_from: string; amount: number | null; created_at: string }[];
}

export interface TargetsUpdate {
  month_from?: string;
  /** amount null clears the target from month_from. */
  targets: { product: string; amount: number | null }[];
}

export type RightsizingKind = 'low_cpu_vm' | 'idle_disk' | 'unused_ip' | 'stale_snapshot' | 'cloud_run_min_instances';

export interface RightsizingCandidate {
  id: string;
  kind: RightsizingKind;
  project_id: string;
  resource_name: string;
  resource_type: ResourceType;
  location: string | null;
  confidence: Confidence;
  evidence: {
    window_start: string;
    window_end: string;
    points: { week: WeekID; value: number | null; unit: string }[];
    notes: string[];
  };
  net_last_week: number | null;
  estimated_monthly_saving: number | null;
  saving_basis: string;
  action: string;
}

export interface RightsizingSource {
  source: string;
  status: 'ok' | 'failed' | 'skipped';
  error_code: string | null;
  message: string | null;
  fix_hint: string | null;
}

export interface RightsizingResponse {
  week: WeekID;
  scope: Scope;
  currency: string;
  generated_at: string;
  monthly_factor: number;
  thresholds: { cpu_pct: number; weeks: number; stale_snapshot_days: number; idle_run_min_net: number };
  sources: RightsizingSource[];
  candidates: RightsizingCandidate[];
  totals: { count: number; estimated_monthly_saving: number };
}

export interface ProposeRequest {
  week?: WeekID;
  scope?: Scope;
  max_rules?: number;
}

export interface RuleProposal {
  rule: AttributionRule;
  confidence: Confidence;
  rationale: string;
  covers: number;
}

export interface ProposeResponse {
  week: WeekID;
  model: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  proposals: RuleProposal[];
  dropped: number;
  coverage_if_applied: Coverage;
}

/** Drill-down filters accepted by GET /snapshots/{id}/breakdown (Phase 3). */
export interface AttributionDrillFilter {
  product?: string;
  environment?: string;
}
