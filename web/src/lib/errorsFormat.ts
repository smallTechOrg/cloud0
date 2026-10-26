// Pure helpers for the Errors tab's explainability view.

import type { ErrorSamplesResponse } from './types';

export interface CodeChip {
  code: string;
  count: number;
  /** 5xx are the application-failure signal and are emphasised. */
  kind: '4xx' | '5xx';
}

/** Error codes (>= 400) of a by_code map: 5xx first, then by count desc, then code. */
export function errorCodeChips(byCode: Record<string, number> | null): CodeChip[] {
  if (byCode === null) return [];
  const chips: CodeChip[] = [];
  for (const [code, count] of Object.entries(byCode)) {
    const n = Number(code);
    if (!Number.isFinite(n) || n < 400 || n > 599 || count <= 0) continue;
    chips.push({ code, count, kind: n >= 500 ? '5xx' : '4xx' });
  }
  return chips.sort((a, b) => (a.kind === b.kind ? 0 : a.kind === '5xx' ? -1 : 1) || b.count - a.count || a.code.localeCompare(b.code));
}

/** "93%" from a 0..1 share; null stays null. */
export function formatShare(share: number | null): string | null {
  return share === null ? null : `${Math.round(share * 100)}%`;
}

/** True when the sample came back with nothing to show (in retention, zero entries). */
export function sampleIsEmpty(s: ErrorSamplesResponse): boolean {
  return !s.out_of_retention && s.scanned === 0;
}
