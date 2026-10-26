// Request/response types mirroring spec/api.md field-for-field (snake_case
// preserved, no renaming layer). The backend (internal/httpapi) implements the
// same file literally; change spec/api.md first, then both sides.

/** `YYYY-Www`, validated against the ISO calendar. */
export type WeekID = string;
/** `"all"` or a project ID from `GET /config`. */
export type Scope = string;
export const SCOPE_ALL: Scope = 'all';

export type WeekStatus = 'partial' | 'settling' | 'final';

/** RFC3339 UTC timestamp, e.g. `2026-10-26T09:15:00Z`. */
export type Timestamp = string;
/** `YYYY-MM-DD`. */
export type DateString = string;

// ---- Envelope ---------------------------------------------------------------

export interface ErrorEnvelope {
  error: { code: string; message: string; hint: string | null };
}

// ---- GET /health ------------------------------------------------------------

export interface BigQueryHealth {
  status: 'ok' | 'error';
  checked_at: Timestamp;
  error_code: string | null;
  message: string | null;
  fix_hint: string | null;
}

export interface Health {
  ok: boolean;
  version: string;
  db: string;
  migrations: string;
  ui: 'real' | 'placeholder';
  /** `null` in the scaffold build (before slice S3 adds the billing source). */
  bigquery: BigQueryHealth | null;
}

// ---- GET /config ------------------------------------------------------------

export interface AppConfig {
  projects: string[];
  first_week: WeekID;
  current_week: WeekID;
  default_week: WeekID;
  settle_hours: number;
  /** ISO 4217 code of every Money value (CLOUD0_CURRENCY, default INR). */
  currency: string;
  now: Timestamp;
}

// ---- GET /cost/table --------------------------------------------------------

export type ColumnKind = 'week' | 'service' | 'total' | 'pct_change' | 'stub';

export interface Column {
  key: string;
  label: string;
  kind: ColumnKind;
  /** Raw `service.description` values aggregated into a `service` column. */
  services?: string[];
  /** Service not in the reference sheet; appended automatically. */
  extra?: boolean;
  /** For `stub` columns: the phase in which the column becomes real. */
  phase?: number;
}

export interface Thresholds {
  pct: number;
  abs_usd: number;
}

export interface SnapshotMeta {
  id: number;
  fetched_at: Timestamp;
  status_at_fetch: WeekStatus;
  run_id: number;
  row_count: number;
  stale: boolean;
}

export interface Cell {
  net: number;
  gross: number;
  credits: number;
  delta_abs: number | null;
  delta_pct: number | null;
  flagged: boolean;
}

export interface CostRow {
  week: WeekID;
  week_start: DateString;
  week_end: DateString;
  week_status: WeekStatus;
  snapshot: SnapshotMeta | null;
  /** One entry per `service`/`total` column key; `null` when no snapshot exists. */
  cells: Record<string, Cell> | null;
  pct_change: number | null;
  flagged: boolean;
  annotation: string | null;
}

export interface CostTableResponse {
  scope: Scope;
  from: WeekID;
  to: WeekID;
  generated_at: Timestamp;
  thresholds: Thresholds;
  columns: Column[];
  rows: CostRow[];
}

// ---- POST /refresh ----------------------------------------------------------

export type RefreshRequest = { mode: 'week'; week: WeekID } | { mode: 'month'; month: MonthID } | { mode: 'all' };

export type RunStatus = 'running' | 'ok' | 'failed' | 'partial';
export type SourceStatus = 'running' | 'ok' | 'failed';

export interface RunSource {
  id: number;
  source: string;
  status: SourceStatus;
  duration_ms: number | null;
  job_id: string | null;
  bytes_processed: number | null;
  bytes_billed: number | null;
  cache_hit: boolean | null;
  rows_returned: number | null;
  error_code: string | null;
  error_message: string | null;
  fix_hint: string | null;
  /** Only on `GET /runs/{id}`. */
  sql?: string;
  /** Only on `GET /runs/{id}`. */
  params?: Record<string, string>;
}

export interface Run {
  id: number;
  trigger: string;
  from: WeekID;
  to: WeekID;
  started_at: Timestamp;
  finished_at: Timestamp | null;
  duration_ms: number | null;
  status: RunStatus;
  error: string | null;
  sources: RunSource[];
}

export interface RefreshSnapshot {
  id: number;
  week: WeekID;
  status_at_fetch: WeekStatus;
  row_count: number;
  net_total: number;
}

export interface RefreshResponse {
  run: Run;
  snapshots: RefreshSnapshot[];
  /** Phase 2; always present from the Phase 2 server (optional only for older fixtures). */
  metric_snapshots?: RefreshMetricSnapshot[];
  month_snapshots?: RefreshMonthSnapshot[];
}

// ---- GET /snapshots/{id} ----------------------------------------------------

export interface ProvenanceSource {
  source: string;
  job_id: string | null;
  bytes_processed: number | null;
  bytes_billed: number | null;
  cache_hit: boolean | null;
  duration_ms: number | null;
  sql: string;
  params: Record<string, string>;
}

export interface SnapshotProvenance {
  id: number;
  week: WeekID;
  week_start: DateString;
  week_end: DateString;
  fetched_at: Timestamp;
  status_at_fetch: WeekStatus;
  week_status: WeekStatus;
  stale: boolean;
  run_id: number;
  row_count: number;
  gross_total: number;
  credits_total: number;
  net_total: number;
  projects: string[];
  formula: string;
  source: ProvenanceSource;
}

// ---- GET /snapshots/{id}/breakdown -----------------------------------------

export type BreakdownLevel = 'service' | 'sku' | 'resource';

export interface BreakdownRow {
  key: string;
  gross: number;
  credits: number;
  net: number;
  line_items: number;
  /** `level=service`: the Cost-table column this raw service maps to. */
  column_key?: string;
  /** `level=sku`. */
  sku_id?: string | null;
  /** `level=resource`. */
  project_id?: string | null;
  resource_name?: string | null;
  resource_global_name?: string | null;
}

export interface BreakdownTotals {
  gross: number;
  credits: number;
  net: number;
}

export interface BreakdownResponse {
  snapshot_id: number;
  level: BreakdownLevel;
  scope: Scope;
  service: string | null;
  sku: string | null;
  rows: BreakdownRow[];
  totals: BreakdownTotals;
}

export interface BreakdownQuery {
  level: BreakdownLevel;
  scope?: Scope;
  service?: string;
  sku?: string;
}

// ---- GET /runs, GET /runs/{id} ---------------------------------------------

export interface RunListItem extends Run {
  snapshot_ids: number[];
}

export interface RunsResponse {
  runs: RunListItem[];
}

// ---- GET/PUT /settings ------------------------------------------------------

export interface Settings {
  anomaly_pct_threshold: number;
  anomaly_abs_threshold_usd: number;
  updated_at: Timestamp;
}

export interface SettingsUpdate {
  anomaly_pct_threshold: number;
  anomaly_abs_threshold_usd: number;
}

// ---- GET/PUT /annotations/{week} -------------------------------------------

export interface Annotation {
  week: WeekID;
  body: string;
  created_at: Timestamp | null;
  updated_at: Timestamp | null;
}

export const ANNOTATION_MAX_CHARS = 10_000;

// ---- Phase 2 (spec/api.md → "Phase 2 additions") ----------------------------

/** `YYYY-MM` UTC calendar month. */
export type MonthID = string;

export interface RefreshMetricSnapshot {
  id: number;
  week: WeekID;
  status_at_fetch: WeekStatus;
  row_count: number;
}

export interface RefreshMonthSnapshot {
  id: number;
  month: MonthID;
  status_at_fetch: WeekStatus;
  row_count: number;
  net_total: number;
}

export interface MetricWindow {
  start: Timestamp;
  end: Timestamp;
}

export interface Provenance {
  source: string;
  query: string;
  aggregation: string;
  window: MetricWindow;
  fetched_at: Timestamp;
}

export type LimitLevel = 'ok' | 'near' | 'over';

/** Exactly one of `value` / `gap` is non-null; `hint` only with a gap. */
export interface MetricCell {
  value: number | null;
  unit: string;
  gap: string | null;
  hint: string | null;
  provenance: Provenance;
  limit_level: LimitLevel | null;
}

/** `id` is the run id for error batches. */
export interface SnapshotRef {
  id: number;
  fetched_at: Timestamp;
  status_at_fetch: WeekStatus;
  run_id: number;
  row_count: number;
  stale: boolean;
}

// ---- GET /metrics/table -----------------------------------------------------

export type MetricGroupId = 'vm' | 'cloudsql' | 'networking' | 'cloudrun' | 'gcs' | 'logging' | 'errors';

export interface MetricColumn {
  key: string;
  label: string;
  unit: string;
}

export interface MetricTableRow {
  project: string;
  /** `""` on the group TOTAL row (`is_total`); otherwise the resource (service, VM, bucket, project). */
  resource: string;
  /** The group's TOTAL row, assembled server-side (last row of the group); `project` is `""` there. */
  is_total: boolean;
  /** One entry per column of the group. */
  cells: Record<string, MetricCell>;
}

export interface MetricGroup {
  group: MetricGroupId;
  label: string;
  columns: MetricColumn[];
  rows: MetricTableRow[];
}

export interface MetricsTableResponse {
  week: WeekID;
  week_start: DateString;
  week_end: DateString;
  week_status: WeekStatus;
  scope: Scope;
  generated_at: Timestamp;
  /** `null` when the week has no metric snapshot (every group then has `rows: []`). */
  snapshot: SnapshotRef | null;
  /** Always seven groups: vm, cloudsql, networking, cloudrun, gcs, logging, errors. */
  groups: MetricGroup[];
}

// ---- GET /errors/table ------------------------------------------------------

export type ErrorSource = 'cloudrun' | 'error_reporting' | 'load_balancer' | 'nginx';

export type ErrorVerdict = 'needs_attention' | 'watch' | 'noise' | 'healthy';

/** Which slice of a service's failing requests an on-demand log sample returns. */
export type SampleFocus = 'server' | 'client' | 'notfound' | 'all';

export interface ErrorRates {
  requests: number;
  rate_5xx: number;
  rate_client_4xx: number;
  rate_404: number;
}

export interface ErrorChange {
  /** Previous ISO week, e.g. "2026-W41". */
  prev_week: WeekID;
  prev_5xx: number;
  prev_client_4xx: number;
  prev_by_code: Record<string, number> | null;
  /** This week's 5xx divided by last week's; null when last week had none. */
  ratio_5xx: number | null;
}

export interface ErrorsSummary {
  needs_attention: number;
  watch: number;
  noise: number;
  healthy: number;
  headline: string;
}

export interface ErrorTableRow {
  source: ErrorSource;
  project: string;
  service: string;
  c2xx: number | null;
  c3xx: number | null;
  c4xx: number | null;
  c5xx: number | null;
  groups: number | null;
  occurrences: number | null;
  /** Cloud Run only: exact HTTP status code -> request count; null otherwise or for older batches. */
  by_code: Record<string, number> | null;
  /** Cloud Run only: Google Cloud Logs Explorer deep link for this service and week. */
  logs_url: string | null;
  /** Triage fields: present on every non-total row from a triage-aware backend. */
  verdict?: ErrorVerdict;
  reasons?: string[];
  /** 1 = look at first; 0 for healthy rows. */
  rank?: number;
  rates?: ErrorRates;
  change?: ErrorChange | null;
  gap: string | null;
  hint: string | null;
  provenance: Provenance;
}

export interface ErrorTotals {
  c2xx: number | null;
  c3xx: number | null;
  c4xx: number | null;
  c5xx: number | null;
  groups: number | null;
  occurrences: number | null;
}

export interface ErrorsTableResponse {
  week: WeekID;
  scope: Scope;
  generated_at: Timestamp;
  snapshot: SnapshotRef | null;
  rows: ErrorTableRow[];
  totals: ErrorTotals;
  summary?: ErrorsSummary;
}

// ---- GET /errors/samples ----------------------------------------------------

export interface SamplePathStat {
  path: string;
  status: number;
  count: number;
  /** Heuristic path-pattern guess, not proof. */
  scanner_like: boolean;
}

export interface SampleEntry {
  timestamp: Timestamp;
  status: number;
  method: string;
  path: string;
  user_agent: string;
  /** Truncated network (IPv4 /24, IPv6 /48), never a full address. */
  remote_ip: string;
  latency_ms: number;
  trace: string;
  scanner_like: boolean;
}

export interface ErrorSamplesResponse {
  week: WeekID;
  project: string;
  service: string;
  min_status: number;
  generated_at: Timestamp;
  logs_url: string;
  scanned: number;
  sampled: boolean;
  note: string;
  out_of_retention: boolean;
  retention_note: string;
  status_counts: Record<string, number>;
  top_paths: SamplePathStat[];
  scanner_share_4xx: number | null;
  heuristic_note: string;
  entries: SampleEntry[];
  focus?: SampleFocus;
  /** Log entries that could not be parsed into a request. */
  unparsed?: number;
}

// ---- GET/PUT /limits --------------------------------------------------------

export type LimitStatusLevel = 'ok' | 'near' | 'over' | 'stub' | 'no data';

export interface LimitStatus {
  id: string;
  service: string;
  label: string;
  unit: string;
  allowance: number;
  default_allowance: number;
  used: number | null;
  projected: number | null;
  pct: number | null;
  level: LimitStatusLevel;
  week_used: number | null;
  stub: boolean;
  reason: string | null;
}

export interface LimitsResponse {
  week: WeekID;
  month: MonthID;
  month_start: DateString;
  month_end: DateString;
  warn_pct: number;
  updated_at: Timestamp | null;
  generated_at: Timestamp;
  limits: LimitStatus[];
}

/** At least one key. Allowances are finite numbers >= 0 keyed by allowance id; warn_pct in (0, 100]. */
export interface LimitsUpdate {
  allowances?: Record<string, number>;
  warn_pct?: number;
}

// ---- GET /cost/month --------------------------------------------------------

/** Month-table column: the Cost-table `Column` plus the leading `month` kind. */
export type MonthColumn = Omit<Column, 'kind'> & { kind: ColumnKind | 'month' };

export type MonthStatus = 'partial' | 'settling' | 'final';

export interface MonthRow {
  month: MonthID;
  month_start: DateString;
  month_end: DateString;
  month_status: MonthStatus;
  snapshot: SnapshotRef | null;
  /** Same cell shape and column keys as the Cost table; `null` when no snapshot exists. */
  cells: Record<string, Cell> | null;
  pct_change: number | null;
  flagged: boolean;
  invoice_months: string[];
}

export interface MonthSummary {
  /** The current month, regardless of from/to. */
  month: MonthID;
  net: number;
  target: number | null;
  projected: number;
  status: MonthStatus;
  has_data: boolean;
}

export interface MonthTableResponse {
  scope: Scope;
  from: MonthID;
  to: MonthID;
  generated_at: Timestamp;
  thresholds: Thresholds;
  /** First column is `{key: "month", kind: "month"}`, then the Cost-table columns. */
  columns: MonthColumn[];
  rows: MonthRow[];
  summary: MonthSummary;
}

// ---- Alerts feature (spec/api.md → "Alerts additions"; Go: internal/alerting/types.go) ----

export type AlertSeverity = 'CRITICAL' | 'ERROR' | 'WARNING';
export type AlertKind = 'policy' | 'uptime_check' | 'budget' | 'snooze' | 'channel';
export type AlertAction = 'create' | 'update' | 'delete' | 'enable' | 'disable' | 'end';
export type HistoryKind = AlertKind | 'routing';
export type AdoptShape =
  | 'zero_cpu_spike'
  | 'high_memory_load'
  | 'uptime_backend_down'
  | 'uptime_down_2h'
  | 'budget_account';
export type TemplateId =
  | 'vm_cpu_high'
  | 'vm_memory_high'
  | 'vm_disk_high'
  | 'run_5xx_burst'
  | 'run_5xx_rate'
  | 'run_error_logged'
  | 'lb_5xx_burst'
  | 'sql_down'
  | 'sql_cpu_high'
  | 'sql_memory_high'
  | 'sql_disk_high'
  | 'uptime_down'
  | 'run_401_403_spike';

/** Raw GCP REST JSON (AlertPolicy, UptimeCheckConfig, Budget …). */
export type RawJson = Record<string, unknown>;

export interface ProjectFailure {
  project: string;
  code: string;
  message: string;
  hint: string | null;
}

export interface AlertsStatus {
  alerts_configured: boolean;
  billing_account_configured: boolean;
  projects: string[];
  generated_at: Timestamp;
}

export interface AlertChannel {
  name: string;
  project: string;
  type: string;
  display_name: string;
  enabled: boolean;
  verification_status: 'VERIFIED' | 'UNVERIFIED' | 'VERIFICATION_STATUS_UNSPECIFIED';
  address: string;
  warnings: string[];
}

export interface ChannelLabelSpec {
  key: string;
  description: string;
  required: boolean;
  sensitive: boolean;
}

export interface ChannelDescriptor {
  type: string;
  display_name: string;
  creatable: boolean;
  hint: string;
  labels: ChannelLabelSpec[];
}

export interface ChannelsResponse {
  channels: AlertChannel[];
  descriptors: ChannelDescriptor[];
  generated_at: Timestamp;
}

/** Create body for kind "channel"; sensitive_labels are passed through to GCP only. */
export interface ChannelDraft {
  project: string;
  type: string;
  display_name: string;
  labels: Record<string, string>;
  sensitive_labels?: Record<string, string>;
}

export interface ChannelRef {
  name: string;
  display_name: string;
  type: string;
  verification_status: string;
}

export interface ConditionSummary {
  display_name: string;
  type: 'threshold' | 'absent' | 'log_match' | 'other';
  summary: string;
  filter: string;
  comparison: string;
  threshold: number | null;
  duration_seconds: number | null;
}

export interface PolicyView {
  name: string;
  project: string;
  display_name: string;
  enabled: boolean;
  /** '' when unset on the live policy. */
  severity: AlertSeverity | '';
  managed: boolean;
  template_id: TemplateId | '';
  instance: string;
  conditions: ConditionSummary[];
  channels: ChannelRef[];
  adopt_shape: AdoptShape | '';
  warnings: string[];
  updated_at: Timestamp | null;
  raw_json: RawJson;
}

export interface PoliciesResponse {
  policies: PolicyView[];
  projects_failed: ProjectFailure[];
  generated_at: Timestamp;
}

export interface TemplateParams {
  threshold?: number;
  duration_seconds?: number;
  window_seconds?: number;
  rate_limit_seconds?: number;
  severity?: AlertSeverity;
  enabled?: boolean;
  channel_override?: string[];
  instance?: string;
  uptime_check_ids?: string[];
}

export interface ParamSpec {
  key: string;
  label: string;
  type: 'number' | 'int' | 'bool' | 'enum';
  unit: string;
  min: number | null;
  max: number | null;
  options: string[];
  help: string;
}

export interface CoveredResource {
  project: string;
  resource_type: 'vm' | 'cloudsql' | 'cloud_run' | 'lb' | 'uptime_check';
  name: string;
  covered: boolean;
  note: string;
}

export interface AlertTemplate {
  id: TemplateId;
  kind: 'metric_threshold' | 'metric_ratio' | 'log_match' | 'uptime' | 'metric_absent';
  title: string;
  description: string;
  severity: AlertSeverity;
  default_enabled: boolean;
  param_specs: ParamSpec[];
  defaults: TemplateParams;
  params: TemplateParams | null;
  installed: string[];
  covered_resources: CoveredResource[];
}

export interface TemplatesResponse {
  templates: AlertTemplate[];
  generated_at: Timestamp;
}

export interface UptimeCheck {
  name: string;
  project: string;
  display_name: string;
  host: string;
  path: string;
  port: number;
  use_ssl: boolean;
  period_seconds: number;
  timeout_seconds: number;
  content_match: string;
  managed: boolean;
  raw_json: RawJson;
}

export interface UptimeChecksResponse {
  checks: UptimeCheck[];
  generated_at: Timestamp;
}

export interface UptimeDraft {
  project: string;
  display_name: string;
  host: string;
  path: string;
  port: number;
  use_ssl: boolean;
  period_seconds: 60 | 300 | 600 | 900;
  timeout_seconds: number;
  content_match: string;
}

export interface Snooze {
  name: string;
  project: string;
  display_name: string;
  policy_names: string[];
  start_time: Timestamp;
  end_time: Timestamp;
  state: 'active' | 'scheduled' | 'expired';
}

export interface SnoozesResponse {
  snoozes: Snooze[];
  generated_at: Timestamp;
}

export interface SnoozeDraft {
  project: string;
  display_name: string;
  policy_names: string[];
  start_time?: Timestamp;
  duration_seconds: number;
}

export interface Incident {
  id: string;
  project: string;
  policy_name: string;
  policy_display_name: string;
  severity: AlertSeverity | '';
  state: 'open' | 'closed';
  opened_at: Timestamp;
  closed_at: Timestamp | null;
  resource_type: string;
  resource_name: string;
  summary: string;
  url: string;
}

export interface IncidentsResponse {
  incidents: Incident[];
  total: number;
  projects_failed: ProjectFailure[];
  generated_at: Timestamp;
}

export interface AlertHistoryRow {
  project: string;
  policy_name: string;
  policy_display_name: string;
  severity: AlertSeverity | '';
  opened: number;
  still_open: number;
  resources: string[];
  first_opened_at: Timestamp;
  last_opened_at: Timestamp;
}

export interface AlertHistoryResponse {
  week: WeekID;
  total_opened: number;
  still_open: number;
  rows: AlertHistoryRow[];
  projects_failed: ProjectFailure[];
  generated_at: Timestamp;
}

export interface CoverageSignal {
  signal: 'cpu' | 'memory' | 'disk' | 'errors' | 'down';
  covered: boolean;
  policies: string[];
  reason: string;
}

export interface CoverageRow {
  project: string;
  resource_type: CoveredResource['resource_type'];
  resource: string;
  signals: CoverageSignal[];
  hint: string;
}

export interface CoverageResponse {
  rows: CoverageRow[];
  generated_at: Timestamp;
}

export interface RoutingResponse {
  routing: Record<AlertSeverity, string[]>;
  defaulted: boolean;
  updated_at: Timestamp | null;
  warnings: string[];
  channels: AlertChannel[];
}

export interface RoutingUpdate {
  routing: Record<AlertSeverity, string[]>;
}

export interface BudgetThreshold {
  percent: number;
  basis: 'current_spend' | 'forecasted_spend';
}

export interface BudgetView {
  name: string;
  display_name: string;
  managed: boolean;
  amount_kind: 'specified' | 'last_period';
  amount: number | null;
  currency: string;
  projects: string[];
  service_ids: string[];
  thresholds: BudgetThreshold[];
  channel_names: string[];
  default_iam_recipients: boolean;
  adopt_shape: 'budget_account' | '';
  warnings: string[];
  raw_json: RawJson;
}

export interface BudgetsResponse {
  billing_account: string;
  currency: string;
  budgets: BudgetView[];
  generated_at: Timestamp;
}

export interface BudgetDraft {
  display_name: string;
  amount: number;
  projects: string[];
  service_ids: string[];
  thresholds: BudgetThreshold[];
  severity?: AlertSeverity;
  channel_override?: string[];
  default_iam_recipients?: boolean;
}

export interface SuggestionBasis {
  method: 'snapshots_avg' | 'monthly_total_setting' | 'none';
  weeks_used: number;
  avg_weekly: number;
  multiplier: number;
  explanation: string;
}

export interface BudgetSuggestion {
  scope: 'account' | 'service';
  service_id: string;
  service_name: string;
  amount: number;
  basis: SuggestionBasis;
  draft: BudgetDraft;
  exists: boolean;
}

export interface BudgetSuggestionsResponse {
  currency: string;
  suggestions: BudgetSuggestion[];
}

export interface PreviewRequest {
  kind: AlertKind;
  action: AlertAction;
  /** Resource name; '' for create. */
  target: string;
  project?: string;
  template_id?: TemplateId;
  /** TemplateParams | UptimeDraft | BudgetDraft | SnoozeDraft | ChannelDraft. Exactly one of params/desired. */
  params?: TemplateParams | UptimeDraft | BudgetDraft | SnoozeDraft | ChannelDraft;
  desired?: RawJson;
}

export interface DiffChange {
  path: string;
  op: 'add' | 'remove' | 'change';
  before: unknown;
  after: unknown;
}

export interface AlertPreview {
  preview_token: string;
  expires_at: Timestamp;
  kind: AlertKind;
  action: AlertAction;
  target: string;
  current: RawJson | null;
  desired: RawJson | null;
  diff: { changes: DiffChange[]; unified: string };
  gcloud: string;
  warnings: string[];
  requires_confirm: boolean;
}

export interface ApplyRequest {
  preview_token: string;
  confirm_delete?: boolean;
}

export interface ApplyResult {
  ok: boolean;
  kind: AlertKind;
  action: AlertAction;
  target: string;
  resource: RawJson | null;
  change_history_id: number;
  applied_at: Timestamp;
  warnings: string[];
}

export interface AdoptRequest {
  shape: AdoptShape;
  target?: string;
}

export interface AdoptFollowUp {
  action: 'disable';
  kind: AlertKind;
  target: string;
  note: string;
}

export interface AdoptPreview {
  shape: AdoptShape;
  replaces: string[];
  previews: AlertPreview[];
  follow_up: AdoptFollowUp[];
  notes: string[];
}

export interface ChangeRecord {
  id: number;
  at: Timestamp;
  actor: string;
  kind: HistoryKind;
  action: AlertAction;
  target: string;
  before: unknown;
  after: unknown;
  preview_hash: string;
}

export interface ChangeHistoryResponse {
  entries: ChangeRecord[];
  limit: number;
}
