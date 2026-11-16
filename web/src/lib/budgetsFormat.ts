// Pure helpers for the Budgets tab: amount parsing, threshold validation,
// suggestion basis text, draft building, scope labels and history CSV.
// Free of React/DOM so they are unit-testable (spec/capabilities/budgets.md).

import { csvField } from './csv';
import { DEFAULT_CURRENCY, formatMoney } from './format';
import type {
  AlertSeverity,
  BudgetDraft,
  BudgetSuggestion,
  BudgetThreshold,
  BudgetView,
  ChangeRecord,
  PreviewRequest,
  SuggestionBasis,
} from './types';

const CRLF = '\r\n';

export const MAX_BUDGET_AMOUNT = 1_000_000_000;
export const MAX_THRESHOLD_PERCENT = 1000;
export const MANAGED_PREFIX = 'cloud0-';

export type Parsed<T> = { ok: true; value: T } | { ok: false; error: string };

/** Parses a typed amount ("₹6,480", "6480.50"): positive, finite, at most 2 decimals. */
export function parseAmount(input: string): Parsed<number> {
  const cleaned = input.replace(/[₹,\s]/g, '').replace(/^INR/i, '');
  if (cleaned === '') return { ok: false, error: 'Enter an amount.' };
  if (!/^\d+(\.\d+)?$/.test(cleaned)) return { ok: false, error: 'Amount must be a plain number such as 6480 or 6480.50.' };
  const n = Number(cleaned);
  if (!Number.isFinite(n) || n <= 0) return { ok: false, error: 'Amount must be greater than 0.' };
  if (n > MAX_BUDGET_AMOUNT) return { ok: false, error: `Amount must be at most ${MAX_BUDGET_AMOUNT.toLocaleString('en-IN')}.` };
  if (!/^\d+(\.\d{1,2})?$/.test(cleaned)) return { ok: false, error: 'Amount can have at most 2 decimal places.' };
  return { ok: true, value: n };
}

export function defaultThresholds(): BudgetThreshold[] {
  return [
    { percent: 50, basis: 'current_spend' },
    { percent: 80, basis: 'current_spend' },
    { percent: 100, basis: 'current_spend' },
    { percent: 100, basis: 'forecasted_spend' },
  ];
}

/** Returns one message per problem; empty = valid. */
export function validateThresholds(list: readonly BudgetThreshold[]): string[] {
  const errors: string[] = [];
  if (list.length === 0) errors.push('Add at least one threshold.');
  const seen = new Set<string>();
  list.forEach((t, i) => {
    const label = `Threshold ${i + 1}`;
    if (!Number.isFinite(t.percent) || t.percent <= 0) errors.push(`${label}: percent must be greater than 0.`);
    else if (t.percent > MAX_THRESHOLD_PERCENT) errors.push(`${label}: percent must be at most ${MAX_THRESHOLD_PERCENT}.`);
    const key = `${t.percent}/${t.basis}`;
    if (seen.has(key)) errors.push(`${label}: ${formatThresholdLabel(t)} is listed twice.`);
    seen.add(key);
  });
  return errors;
}

export function formatThresholdLabel(t: BudgetThreshold): string {
  return `${t.percent} % ${t.basis === 'forecasted_spend' ? 'forecast' : 'spend'}`;
}

/** Thresholds sorted by percent, spend before forecast. */
export function sortThresholds(list: readonly BudgetThreshold[]): BudgetThreshold[] {
  return [...list].sort((a, b) => a.percent - b.percent || (a.basis === b.basis ? 0 : a.basis === 'current_spend' ? -1 : 1));
}

/** "1.2 × average of the last 4 weeks = ₹6,480.00" (never invents a basis the server did not give). */
export function basisText(basis: SuggestionBasis, amount: number, currency: string = DEFAULT_CURRENCY): string {
  const money = formatMoney(amount, currency);
  switch (basis.method) {
    case 'monthly_total_setting':
      return `Monthly target setting (target.monthly_total) = ${money}`;
    case 'none':
      return basis.explanation || 'No stored weekly snapshots yet — refresh a week first, then a suggestion appears.';
    default: {
      const weeks = basis.weeks_used;
      return `${basis.multiplier} × average of the last ${weeks} ${weeks === 1 ? 'week' : 'weeks'} = ${money}`;
    }
  }
}

/** Lower-case slug for a budget name part: "Cloud Run" -> "cloud-run". */
export function slug(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

export function isManagedName(name: string): boolean {
  return name.startsWith(MANAGED_PREFIX);
}

/** Ensures the `cloud0-` prefix (managed budgets are recognised by it). */
export function ensureManagedName(name: string): string {
  const t = name.trim();
  return isManagedName(t) ? t : `${MANAGED_PREFIX}${slug(t) || 'budget'}`;
}

export function validateName(name: string): string | null {
  const t = name.trim();
  if (t === '') return 'Enter a name.';
  if (!isManagedName(t)) return `Name must start with "${MANAGED_PREFIX}" (that is how managed budgets are recognised).`;
  if (t.length <= MANAGED_PREFIX.length) return 'Add a descriptive part after the prefix.';
  if (t.length > 60) return 'Name must be at most 60 characters.';
  return null;
}

export type ScopeKind = 'account' | 'services' | 'projects';

export interface FormState {
  name: string;
  scope: ScopeKind;
  serviceIds: string[];
  projects: string[];
  amount: string;
  thresholds: BudgetThreshold[];
  channels: string[];
  severity: AlertSeverity;
  /** Channel override chosen explicitly; false = follow severity routing. */
  overrideChannels: boolean;
}

export function autoName(scope: ScopeKind, serviceNames: readonly string[], projects: readonly string[]): string {
  if (scope === 'services') return `${MANAGED_PREFIX}${serviceNames.map(slug).filter(Boolean).join('-') || 'services'}-monthly`;
  if (scope === 'projects') return `${MANAGED_PREFIX}${projects.map(slug).filter(Boolean).join('-') || 'project'}-monthly`;
  return `${MANAGED_PREFIX}account-monthly`;
}

export function initialForm(suggestion?: BudgetSuggestion | null): FormState {
  if (suggestion) {
    const d = suggestion.draft;
    return {
      name: d.display_name,
      scope: d.service_ids.length > 0 ? 'services' : d.projects.length > 0 ? 'projects' : 'account',
      serviceIds: [...d.service_ids],
      projects: [...d.projects],
      amount: suggestion.amount > 0 ? String(suggestion.amount) : '',
      thresholds: sortThresholds(d.thresholds.length > 0 ? d.thresholds : defaultThresholds()),
      channels: d.channel_override ? [...d.channel_override] : [],
      severity: d.severity ?? 'ERROR',
      overrideChannels: Boolean(d.channel_override && d.channel_override.length > 0),
    };
  }
  return {
    name: `${MANAGED_PREFIX}account-monthly`,
    scope: 'account',
    serviceIds: [],
    projects: [],
    amount: '',
    thresholds: defaultThresholds(),
    channels: [],
    severity: 'ERROR',
    overrideChannels: false,
  };
}

/** Form for editing a managed budget or adopting an unmanaged one (copy gets a cloud0- name). */
export function formFromBudget(b: BudgetView, adopt: boolean): FormState {
  return {
    name: adopt ? ensureManagedName(b.display_name) : b.display_name,
    scope: b.service_ids.length > 0 ? 'services' : b.projects.length > 0 ? 'projects' : 'account',
    serviceIds: [...b.service_ids],
    projects: [...b.projects],
    amount: b.amount === null ? '' : String(b.amount),
    thresholds: sortThresholds(b.thresholds.length > 0 ? b.thresholds : defaultThresholds()),
    channels: [...b.channel_names],
    severity: 'ERROR',
    overrideChannels: b.channel_names.length > 0,
  };
}

/** Validates the form; on success returns the BudgetDraft sent as `params`. `requirePrefix:false` is for editing an unmanaged budget in place. */
export function buildDraft(f: FormState, opts: { requirePrefix?: boolean } = {}): { draft: BudgetDraft | null; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  const nameErr = opts.requirePrefix === false ? (f.name.trim() === '' ? 'Enter a name.' : null) : validateName(f.name);
  if (nameErr) errors.name = nameErr;
  const amt = parseAmount(f.amount);
  if (!amt.ok) errors.amount = amt.error;
  const th = validateThresholds(f.thresholds);
  if (th.length > 0) errors.thresholds = th.join(' ');
  if (f.scope === 'services' && f.serviceIds.length === 0) errors.scope = 'Pick at least one service.';
  if (f.scope === 'projects' && f.projects.length === 0) errors.scope = 'Pick at least one project.';
  if (f.overrideChannels && f.channels.length === 0) errors.channels = 'Pick at least one channel or follow the severity routing.';
  if (Object.keys(errors).length > 0 || !amt.ok) return { draft: null, errors };
  const draft: BudgetDraft = {
    display_name: f.name.trim(),
    amount: amt.value,
    projects: f.scope === 'projects' ? [...f.projects] : [],
    service_ids: f.scope === 'services' ? [...f.serviceIds] : [],
    thresholds: sortThresholds(f.thresholds),
    severity: f.severity,
    default_iam_recipients: true,
  };
  if (f.overrideChannels) draft.channel_override = [...f.channels];
  return { draft, errors };
}

export function createRequest(draft: BudgetDraft): PreviewRequest {
  return { kind: 'budget', action: 'create', target: '', params: draft };
}

export function updateRequest(target: string, draft: BudgetDraft): PreviewRequest {
  return { kind: 'budget', action: 'update', target, params: draft };
}

export function deleteRequest(target: string): PreviewRequest {
  return { kind: 'budget', action: 'delete', target };
}

/** Human scope: "Whole billing account", "Project proj-a", "Compute Engine, Cloud SQL". */
export function scopeLabel(b: Pick<BudgetView, 'projects' | 'service_ids'>, serviceNames: Readonly<Record<string, string>> = {}): string {
  const parts: string[] = [];
  if (b.projects.length > 0) parts.push(`${b.projects.length === 1 ? 'Project' : 'Projects'} ${b.projects.join(', ')}`);
  if (b.service_ids.length > 0) parts.push(`Services ${b.service_ids.map((id) => serviceNames[id] ?? id).join(', ')}`);
  return parts.length > 0 ? parts.join(' · ') : 'Whole billing account';
}

/** The Month-tab scope whose net cost matches the budget, or null when it cannot be derived. */
export function monthScopeFor(b: Pick<BudgetView, 'projects' | 'service_ids'>): string | null {
  if (b.service_ids.length > 0) return null;
  if (b.projects.length === 0) return 'all';
  if (b.projects.length === 1) return b.projects[0] ?? null;
  return null;
}

export interface Progress {
  pct: number;
  /** Bar width 0..100. */
  width: number;
  tone: 'ok' | 'near' | 'over';
}

/** Month-to-date spend vs budget; null when the amount is unknown/zero. */
export function spendProgress(spent: number, amount: number | null): Progress | null {
  if (amount === null || !(amount > 0) || !Number.isFinite(spent)) return null;
  const pct = (spent / amount) * 100;
  return { pct, width: Math.max(0, Math.min(100, pct)), tone: pct >= 100 ? 'over' : pct >= 80 ? 'near' : 'ok' };
}

export function formatBudgetAmount(b: Pick<BudgetView, 'amount_kind' | 'amount' | 'currency'>): string {
  if (b.amount_kind === 'last_period' || b.amount === null) return 'Last period spend';
  return formatMoney(b.amount, b.currency || DEFAULT_CURRENCY);
}

export const HISTORY_KINDS = ['', 'policy', 'uptime_check', 'budget', 'snooze', 'channel'] as const;

export function historyKindLabel(k: string): string {
  switch (k) {
    case '':
      return 'All kinds';
    case 'policy':
      return 'Policies';
    case 'uptime_check':
      return 'Uptime checks';
    case 'budget':
      return 'Budgets';
    case 'snooze':
      return 'Snoozes';
    case 'channel':
      return 'Channels';
    case 'routing':
      return 'Routing';
    default:
      return k;
  }
}

export function prettyJson(v: unknown): string {
  if (v === null || v === undefined) return '—';
  return JSON.stringify(v, null, 2);
}

/** CRLF CSV of the history entries; before/after are compact JSON. */
export function buildHistoryCsv(entries: readonly ChangeRecord[]): string {
  const header = ['id', 'at', 'actor', 'kind', 'action', 'target', 'before', 'after', 'preview_hash'].map(csvField).join(',');
  const lines = entries.map((e) =>
    [
      String(e.id),
      e.at,
      e.actor,
      e.kind,
      e.action,
      e.target,
      e.before === null || e.before === undefined ? '' : JSON.stringify(e.before),
      e.after === null || e.after === undefined ? '' : JSON.stringify(e.after),
      e.preview_hash,
    ]
      .map(csvField)
      .join(','),
  );
  return [header, ...lines].join(CRLF) + CRLF;
}

export function historyCsvFilename(kind: string): string {
  return `cloud0-change-history${kind ? `-${kind}` : ''}.csv`;
}

export interface SuggestedAmount {
  amount: number;
  /** Visible basis line under the amount field. */
  text: string;
}

/** The suggested amount + basis line for the form's current scope; null when there is none (amount 0 / method none). */
export function suggestedFor(
  scope: ScopeKind,
  serviceIds: readonly string[],
  suggestions: readonly BudgetSuggestion[],
  currency: string = DEFAULT_CURRENCY,
): SuggestedAmount | null {
  const usable = (s: BudgetSuggestion | undefined): s is BudgetSuggestion => s !== undefined && s.basis.method !== 'none' && s.amount > 0;
  if (scope === 'services') {
    const picked = suggestions.filter((s) => s.scope === 'service' && serviceIds.includes(s.service_id) && usable(s));
    if (picked.length === 0) return null;
    if (picked.length === 1) return { amount: picked[0]!.amount, text: `${picked[0]!.service_name}: ${basisText(picked[0]!.basis, picked[0]!.amount, currency)}` };
    const total = Math.round(picked.reduce((a, s) => a + s.amount, 0) * 100) / 100;
    return { amount: total, text: `Sum of ${picked.length} service suggestions (${picked.map((s) => s.service_name).join(', ')}) = ${formatMoney(total, currency)}` };
  }
  const account = suggestions.find((s) => s.scope === 'account');
  if (!usable(account)) return null;
  const text = basisText(account.basis, account.amount, currency);
  return { amount: account.amount, text: scope === 'projects' ? `${text} (whole-account suggestion — adjust for the chosen projects)` : text };
}
