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

export type RefreshRequest = { mode: 'week'; week: WeekID } | { mode: 'all' };

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
