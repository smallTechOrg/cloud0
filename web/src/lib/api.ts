// Typed client for /api/v1 (spec/api.md). Every call goes through request();
// failures surface as ApiError built from the error envelope. A network-level
// failure (server down, wrong origin) is also an ApiError with code
// "unreachable" and status 0 so components have one error shape to render.

import type {
  Annotation,
  AppConfig,
  BreakdownQuery,
  BreakdownResponse,
  CostTableResponse,
  ErrorEnvelope,
  Health,
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
