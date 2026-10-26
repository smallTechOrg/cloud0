// Typed client for /api/v1 (spec/api.md). Every call goes through request();
// failures surface as ApiError built from the error envelope. Phase 1 slice S4
// extends this file with the cost/refresh/snapshots/runs/settings/annotations
// calls and moves the shared types into src/lib/types.ts.

export type WeekID = string;
export type WeekStatus = 'partial' | 'settling' | 'final';

export interface BigQueryHealth {
  status: 'ok' | 'error';
  checked_at: string;
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
  bigquery: BigQueryHealth | null;
}

export interface AppConfig {
  projects: string[];
  first_week: WeekID;
  current_week: WeekID;
  default_week: WeekID;
  settle_hours: number;
  now: string;
}

interface ErrorEnvelope {
  error: { code: string; message: string; hint: string | null };
}

export class ApiError extends Error {
  readonly code: string;
  readonly hint: string | null;
  readonly status: number;

  constructor(status: number, code: string, message: string, hint: string | null) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.hint = hint;
  }
}

export const API_BASE = '/api/v1';

function isErrorEnvelope(v: unknown): v is ErrorEnvelope {
  if (typeof v !== 'object' || v === null || !('error' in v)) return false;
  const e = (v as { error: unknown }).error;
  return typeof e === 'object' && e !== null && 'code' in e && 'message' in e;
}

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: { Accept: 'application/json', ...(init?.headers ?? {}) },
  });
  const text = await res.text();
  let body: unknown = null;
  if (text.length > 0) {
    try {
      body = JSON.parse(text);
    } catch {
      throw new ApiError(res.status, 'invalid_response', `Server returned non-JSON (${res.status}).`, null);
    }
  }
  if (!res.ok) {
    if (isErrorEnvelope(body)) {
      throw new ApiError(res.status, body.error.code, body.error.message, body.error.hint ?? null);
    }
    throw new ApiError(res.status, 'http_error', `Request failed with status ${res.status}.`, null);
  }
  return body as T;
}

export function getHealth(): Promise<Health> {
  return request<Health>('/health');
}

export function getConfig(): Promise<AppConfig> {
  return request<AppConfig>('/config');
}
