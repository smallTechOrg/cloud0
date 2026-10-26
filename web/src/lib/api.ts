// Typed client for /api/v1 (spec/api.md). Every call goes through request();
// failures surface as ApiError built from the error envelope. A network-level
// failure (server down, wrong origin) is also an ApiError with code
// "unreachable" and status 0 so components have one error shape to render.

import type {
  AdoptPreview,
  AdoptRequest,
  AlertHistoryResponse,
  AlertPreview,
  AlertsStatus,
  ApplyRequest,
  ApplyResult,
  BudgetSuggestionsResponse,
  BudgetsResponse,
  ChangeHistoryResponse,
  ChannelsResponse,
  CoverageResponse,
  HistoryKind,
  IncidentsResponse,
  PoliciesResponse,
  PolicyView,
  PreviewRequest,
  RoutingResponse,
  RoutingUpdate,
  SnoozesResponse,
  TemplatesResponse,
  UptimeChecksResponse,
  Annotation,
  AppConfig,
  BreakdownQuery,
  BreakdownResponse,
  CostTableResponse,
  ErrorEnvelope,
  ErrorSamplesResponse,
  ErrorsTableResponse,
  SampleFocus,
  Health,
  LimitsResponse,
  LimitsUpdate,
  MetricsTableResponse,
  MonthID,
  MonthTableResponse,
  RefreshRequest,
  RefreshResponse,
  Run,
  RunsResponse,
  Scope,
  Settings,
  SettingsUpdate,
  SnapshotProvenance,
  WeekID,
} from './types';

export type { AppConfig, Health, WeekID } from './types';

export class ApiError extends Error {
  readonly code: string;
  readonly hint: string | null;
  readonly status: number;
  /** `X-Request-Id` of the failed response, for log correlation. */
  readonly requestId: string | null;

  constructor(status: number, code: string, message: string, hint: string | null, requestId: string | null = null) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.hint = hint;
    this.requestId = requestId;
  }
}

export const API_BASE = '/api/v1';

export const UNREACHABLE_CODE = 'unreachable';

function isErrorEnvelope(v: unknown): v is ErrorEnvelope {
  if (typeof v !== 'object' || v === null || !('error' in v)) return false;
  const e = (v as { error: unknown }).error;
  return typeof e === 'object' && e !== null && 'code' in e && 'message' in e;
}

/** Message + hint used when fetch itself fails (no HTTP response at all). */
export function unreachableError(): ApiError {
  const origin = typeof window === 'undefined' ? '' : window.location.origin;
  return new ApiError(
    0,
    UNREACHABLE_CODE,
    'cloud0 server unreachable at this origin',
    `Expected the API at ${origin}${API_BASE} — is ./bin/cloud0 serve running?`,
  );
}

/** Turns any thrown value into an ApiError so UI code has one error shape. */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  if (err instanceof TypeError) return unreachableError();
  const message = err instanceof Error ? err.message : String(err);
  return new ApiError(0, 'client_error', message, null);
}

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      ...init,
      headers: { Accept: 'application/json', ...(init?.headers ?? {}) },
    });
  } catch {
    throw unreachableError();
  }
  const requestId = res.headers.get('X-Request-Id');
  const text = await res.text();
  let body: unknown = null;
  if (text.length > 0) {
    try {
      body = JSON.parse(text);
    } catch {
      throw new ApiError(res.status, 'invalid_response', `Server returned non-JSON (${res.status}).`, null, requestId);
    }
  }
  if (!res.ok) {
    if (isErrorEnvelope(body)) {
      throw new ApiError(res.status, body.error.code, body.error.message, body.error.hint ?? null, requestId);
    }
    throw new ApiError(res.status, 'http_error', `Request failed with status ${res.status}.`, null, requestId);
  }
  return body as T;
}

function jsonInit(method: 'POST' | 'PUT', body: unknown): RequestInit {
  return { method, body: JSON.stringify(body), headers: { 'Content-Type': 'application/json' } };
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') q.set(k, String(v));
  }
  const s = q.toString();
  return s.length > 0 ? `?${s}` : '';
}

// ---- Endpoints (one function per spec/api.md entry) -------------------------

export function getHealth(): Promise<Health> {
  return request<Health>('/health');
}

export function getConfig(): Promise<AppConfig> {
  return request<AppConfig>('/config');
}

export function getCostTable(params: { scope?: Scope; from?: WeekID; to?: WeekID } = {}): Promise<CostTableResponse> {
  return request<CostTableResponse>(`/cost/table${query(params)}`);
}

export function postRefresh(body: RefreshRequest): Promise<RefreshResponse> {
  return request<RefreshResponse>('/refresh', jsonInit('POST', body));
}

export function getSnapshot(id: number): Promise<SnapshotProvenance> {
  return request<SnapshotProvenance>(`/snapshots/${id}`);
}

export function getBreakdown(id: number, q: BreakdownQuery): Promise<BreakdownResponse> {
  return request<BreakdownResponse>(`/snapshots/${id}/breakdown${query({ ...q })}`);
}

export function getRuns(limit?: number): Promise<RunsResponse> {
  return request<RunsResponse>(`/runs${query({ limit })}`);
}

export function getRun(id: number): Promise<Run> {
  return request<Run>(`/runs/${id}`);
}

export function getSettings(): Promise<Settings> {
  return request<Settings>('/settings');
}

export function putSettings(body: SettingsUpdate): Promise<Settings> {
  return request<Settings>('/settings', jsonInit('PUT', body));
}

export function getAnnotation(week: WeekID): Promise<Annotation> {
  return request<Annotation>(`/annotations/${encodeURIComponent(week)}`);
}

export function putAnnotation(week: WeekID, body: string): Promise<Annotation> {
  return request<Annotation>(`/annotations/${encodeURIComponent(week)}`, jsonInit('PUT', { body }));
}

// ---- Phase 2 endpoints ------------------------------------------------------

export function getMetricsTable(params: { week?: WeekID; scope?: Scope } = {}): Promise<MetricsTableResponse> {
  return request<MetricsTableResponse>(`/metrics/table${query(params)}`);
}

export function getErrorsTable(params: { week?: WeekID; scope?: Scope } = {}): Promise<ErrorsTableResponse> {
  return request<ErrorsTableResponse>(`/errors/table${query(params)}`);
}

export function getErrorSamples(params: { week?: WeekID; service: string; project?: string; minStatus?: number; limit?: number; focus?: SampleFocus }): Promise<ErrorSamplesResponse> {
  return request<ErrorSamplesResponse>(
    `/errors/samples${query({ week: params.week, service: params.service, project: params.project, min_status: params.minStatus, limit: params.limit, focus: params.focus })}`,
  );
}

export function getLimits(week?: WeekID): Promise<LimitsResponse> {
  return request<LimitsResponse>(`/limits${query({ week })}`);
}

export function putLimits(body: LimitsUpdate, week?: WeekID): Promise<LimitsResponse> {
  return request<LimitsResponse>(`/limits${query({ week })}`, jsonInit('PUT', body));
}

export function getMonth(params: { from?: MonthID; to?: MonthID; scope?: Scope } = {}): Promise<MonthTableResponse> {
  return request<MonthTableResponse>(`/cost/month${query(params)}`);
}

export function refreshWeek(week: WeekID): Promise<RefreshResponse> {
  return postRefresh({ mode: 'week', week });
}

export function refreshMonth(month: MonthID): Promise<RefreshResponse> {
  return postRefresh({ mode: 'month', month });
}

export function refreshAll(): Promise<RefreshResponse> {
  return postRefresh({ mode: 'all' });
}

// ---- Alerts endpoints (spec/api.md → "Alerts additions") ---------------------

export function getAlertsStatus(): Promise<AlertsStatus> {
  return request<AlertsStatus>('/alerts/status');
}

export function getChannels(): Promise<ChannelsResponse> {
  return request<ChannelsResponse>('/alerts/channels');
}

export function getPolicies(params: { project?: string; managed?: boolean } = {}): Promise<PoliciesResponse> {
  return request<PoliciesResponse>(`/alerts/policies${query({ project: params.project, managed: params.managed === undefined ? undefined : String(params.managed) })}`);
}

/** `name` is the full resource name `projects/<p>/alertPolicies/<id>`. */
export function getPolicy(name: string): Promise<PolicyView> {
  const [, project, , id] = name.split('/');
  return request<PolicyView>(`/alerts/policies/${encodeURIComponent(project ?? '')}/${encodeURIComponent(id ?? '')}`);
}

export function getTemplates(): Promise<TemplatesResponse> {
  return request<TemplatesResponse>('/alerts/templates');
}

export function getUptimeChecks(): Promise<UptimeChecksResponse> {
  return request<UptimeChecksResponse>('/alerts/uptime-checks');
}

export function getSnoozes(): Promise<SnoozesResponse> {
  return request<SnoozesResponse>('/alerts/snoozes');
}

export function getIncidents(state: 'open' | 'closed' | 'all' = 'open', project?: string): Promise<IncidentsResponse> {
  return request<IncidentsResponse>(`/alerts/incidents${query({ state, project })}`);
}

export function getAlertHistory(week?: WeekID): Promise<AlertHistoryResponse> {
  return request<AlertHistoryResponse>(`/alerts/history${query({ week })}`);
}

export function getCoverage(): Promise<CoverageResponse> {
  return request<CoverageResponse>('/alerts/coverage');
}

export function getRouting(): Promise<RoutingResponse> {
  return request<RoutingResponse>('/alerts/routing');
}

export function putRouting(body: RoutingUpdate): Promise<RoutingResponse> {
  return request<RoutingResponse>('/alerts/routing', jsonInit('PUT', body));
}

/** Read-only: returns the exact JSON, gcloud command and diff; changes nothing in GCP. */
export function previewAlert(body: PreviewRequest): Promise<AlertPreview> {
  return request<AlertPreview>('/alerts/preview', jsonInit('POST', body));
}

export function applyAlert(body: ApplyRequest): Promise<ApplyResult> {
  return request<ApplyResult>('/alerts/apply', jsonInit('POST', body));
}

export function adoptPreview(body: AdoptRequest): Promise<AdoptPreview> {
  return request<AdoptPreview>('/alerts/adopt/preview', jsonInit('POST', body));
}

export function getBudgets(): Promise<BudgetsResponse> {
  return request<BudgetsResponse>('/budgets');
}

export function getBudgetSuggestions(): Promise<BudgetSuggestionsResponse> {
  return request<BudgetSuggestionsResponse>('/budgets/suggestions');
}

export function getHistory(params: { kind?: HistoryKind; limit?: number } = {}): Promise<ChangeHistoryResponse> {
  return request<ChangeHistoryResponse>(`/history${query(params)}`);
}
