// Typed client for the Phase 3 attribution endpoints (spec/api.md →
// "Attribution additions"). Reuses request()/ApiError from api.ts, so failures
// surface as ApiError (codes: invalid_rule, invalid_allocate, invalid_target,
// invalid_dimension, proposer_not_configured, proposer_failed, plus the common
// ones).

import { request } from './api';
import type { Scope, WeekID } from './types';
import type {
  AllocateMode,
  BreakdownResponse,
  ExplainRequest,
  ExplainResponse,
  LabelSuggestionsResponse,
  ProposeRequest,
  ProposeResponse,
  RightsizingResponse,
  RulesResponse,
  RulesUpdate,
  SeedApplied,
  SeedPreview,
  SeedRequest,
  TargetsResponse,
  TargetsUpdate,
  UnattributedResponse,
} from './attributionTypes';

function qs(params: object): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params as Record<string, string | number | undefined>)) {
    if (v !== undefined && v !== '') q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : '';
}

function send(method: 'POST' | 'PUT', body: unknown): RequestInit {
  return { method, body: JSON.stringify(body), headers: { 'Content-Type': 'application/json' } };
}

export function getAttributionRules(): Promise<RulesResponse> {
  return request<RulesResponse>('/attribution/rules');
}

/** Replaces the whole rule list atomically; 400 invalid_rule names the bad index. */
export function putAttributionRules(update: RulesUpdate): Promise<RulesResponse> {
  return request<RulesResponse>('/attribution/rules', send('PUT', update));
}

export function previewSeed(body: SeedRequest = {}): Promise<SeedPreview> {
  return request<SeedPreview>('/attribution/seed/preview', send('POST', body));
}

export function applySeed(body: SeedRequest = {}): Promise<SeedApplied> {
  return request<SeedApplied>('/attribution/seed/apply', send('POST', body));
}

export function getUnattributed(params: { week?: WeekID; scope?: Scope; limit?: number } = {}): Promise<UnattributedResponse> {
  return request<UnattributedResponse>(`/attribution/unattributed${qs(params)}`);
}

export function explainResource(body: ExplainRequest): Promise<ExplainResponse> {
  return request<ExplainResponse>('/attribution/explain', send('POST', body));
}

export interface BreakdownParams {
  week?: WeekID;
  scope?: Scope;
  allocate?: AllocateMode;
}

/** `env` filters to one environment; `(no environment)` is allowed. */
export function getCostByProduct(params: BreakdownParams & { env?: string } = {}): Promise<BreakdownResponse> {
  return request<BreakdownResponse>(`/cost/by-product${qs(params)}`);
}

/** `product` filters to one product; `(shared)` and `(unattributed)` are allowed. */
export function getCostByEnvironment(params: BreakdownParams & { product?: string } = {}): Promise<BreakdownResponse> {
  return request<BreakdownResponse>(`/cost/by-environment${qs(params)}`);
}

export function getLabelSuggestions(params: { week?: WeekID; scope?: Scope; limit?: number } = {}): Promise<LabelSuggestionsResponse> {
  return request<LabelSuggestionsResponse>(`/attribution/label-suggestions${qs(params)}`);
}

export function getTargets(params: { month?: string; scope?: Scope } = {}): Promise<TargetsResponse> {
  return request<TargetsResponse>(`/targets${qs(params)}`);
}

export function putTargets(update: TargetsUpdate, params: { month?: string } = {}): Promise<TargetsResponse> {
  return request<TargetsResponse>(`/targets${qs(params)}`, send('PUT', update));
}

export function getRightsizing(params: { week?: WeekID; scope?: Scope } = {}): Promise<RightsizingResponse> {
  return request<RightsizingResponse>(`/rightsizing${qs(params)}`);
}

/** 503 proposer_not_configured when CLOUD0_OPENROUTER_API_KEY is unset. Never applies rules. */
export function proposeRules(body: ProposeRequest = {}): Promise<ProposeResponse> {
  return request<ProposeResponse>('/attribution/propose', send('POST', body));
}
