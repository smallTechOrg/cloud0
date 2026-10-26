// Pure helpers for the Alerts tab (templates, policies, routing, channels and
// the preview/apply drawer). No React, no fetch.

import type {
  AdoptShape,
  AlertChannel,
  AlertSeverity,
  AlertTemplate,
  ChannelDescriptor,
  ChannelDraft,
  CoveredResource,
  ParamSpec,
  PolicyView,
  RawJson,
  TemplateId,
  TemplateParams,
} from './types';

export const SEVERITIES: readonly AlertSeverity[] = ['CRITICAL', 'ERROR', 'WARNING'];

export type ErrorClass = 'stale' | 'not_configured' | 'permission' | 'confirm' | 'api_disabled' | 'invalid' | 'other';

/** Maps an API error code to how the drawer presents it. */
export function classifyAlertError(code: string): ErrorClass {
  switch (code) {
    case 'preview_stale':
      return 'stale';
    case 'alerts_not_configured':
      return 'not_configured';
    case 'permission_denied':
      return 'permission';
    case 'confirm_required':
      return 'confirm';
    case 'api_disabled':
      return 'api_disabled';
    case 'invalid_params':
    case 'invalid_request':
      return 'invalid';
    default:
      return 'other';
  }
}

export const NOT_CONFIGURED_TEXT =
  'Applying changes needs the write-capable cloud0-alerts service account. Set CLOUD0_ALERTS_CREDENTIALS to the path of its key file and restart cloud0 (README → Alerts setup). Previews for policies still work without it.';

export const STALE_TEXT = 'Live state changed since this preview — preview again and review the new diff. Nothing was applied.';

export function severityTone(s: AlertSeverity | ''): 'critical' | 'error' | 'warning' | 'none' {
  switch (s) {
    case 'CRITICAL':
      return 'critical';
    case 'ERROR':
      return 'error';
    case 'WARNING':
      return 'warning';
    default:
      return 'none';
  }
}

export function severityLabel(s: AlertSeverity | ''): string {
  return s === '' ? 'unset' : s.toLowerCase();
}

// ---- diff -----------------------------------------------------------------

export interface DiffLine {
  kind: 'add' | 'remove' | 'ctx';
  text: string;
}

/** Splits the server's unified diff into classified lines ("+ x" / "- x" / context). */
export function parseUnifiedDiff(unified: string): DiffLine[] {
  if (unified.trim() === '') return [];
  return unified.split('\n').map((line) => {
    if (line.startsWith('+')) return { kind: 'add', text: line };
    if (line.startsWith('-')) return { kind: 'remove', text: line };
    return { kind: 'ctx', text: line };
  });
}

export function formatDiffValue(v: unknown): string {
  if (v === null || v === undefined) return '∅';
  if (typeof v === 'string') return JSON.stringify(v);
  return JSON.stringify(v);
}

export function prettyJson(v: unknown): string {
  return JSON.stringify(v, null, 2) ?? 'null';
}

// ---- durations -------------------------------------------------------------

export function formatSeconds(n: number): string {
  if (n % 3600 === 0 && n >= 3600) return `${n / 3600} h`;
  if (n % 60 === 0 && n >= 60) return `${n / 60} min`;
  return `${n} s`;
}

// ---- templates --------------------------------------------------------------

export type TemplateGroup = 'VM' | 'Errors' | 'Database' | 'Uptime';
export const TEMPLATE_GROUPS: readonly TemplateGroup[] = ['VM', 'Errors', 'Database', 'Uptime'];

export function templateGroup(id: TemplateId): TemplateGroup {
  if (id.startsWith('vm_')) return 'VM';
  if (id.startsWith('sql_')) return 'Database';
  if (id.startsWith('uptime_')) return 'Uptime';
  return 'Errors';
}

export function groupTemplates(templates: AlertTemplate[]): { group: TemplateGroup; templates: AlertTemplate[] }[] {
  return TEMPLATE_GROUPS.map((group) => ({ group, templates: templates.filter((t) => templateGroup(t.id) === group) })).filter(
    (g) => g.templates.length > 0,
  );
}

export function coverageSummary(t: AlertTemplate): { covered: number; total: number; skipped: CoveredResource[] } {
  const skipped = t.covered_resources.filter((r) => !r.covered);
  return { covered: t.covered_resources.length - skipped.length, total: t.covered_resources.length, skipped };
}

export function skippedLabel(r: CoveredResource): string {
  return r.note ? `${r.name}: ${r.note}` : `${r.name}: skipped`;
}

/** "80 % for 5 min" style one-liner for the param keys a template has. */
export function paramsSummary(t: AlertTemplate, p: TemplateParams): string {
  const parts: string[] = [];
  for (const spec of t.param_specs) {
    if (spec.key === 'severity') continue;
    const v = (p as Record<string, unknown>)[spec.key];
    if (v === undefined || v === null) continue;
    parts.push(`${spec.label} ${formatParamValue(spec, v)}`);
  }
  if (p.enabled === false) parts.push('disabled');
  return parts.join(' · ');
}

export function formatParamValue(spec: ParamSpec, v: unknown): string {
  if (spec.unit === 'seconds' && typeof v === 'number') return formatSeconds(v);
  if (spec.unit) return `${String(v)} ${spec.unit}`;
  return String(v);
}

export type FormValue = string | boolean | string[];
export type TemplateForm = Record<string, FormValue>;

const SPECIAL_KEYS = new Set(['enabled', 'channel_override']);

/** Initial form state: installed params if present, else the defaults. */
export function initialTemplateForm(t: AlertTemplate): TemplateForm {
  const src: TemplateParams = { ...t.defaults, ...(t.params ?? {}) };
  const form: TemplateForm = {};
  for (const spec of t.param_specs) {
    const v = (src as Record<string, unknown>)[spec.key];
    form[spec.key] = v === undefined || v === null ? '' : String(v);
  }
  if (!('severity' in form)) form.severity = src.severity ?? t.severity;
  form.enabled = src.enabled ?? t.default_enabled;
  form.channel_override = src.channel_override ?? [];
  if (t.id === 'uptime_down') form.instance = src.instance ?? 'warning';
  return form;
}

export type BuildResult<T> = { ok: true; value: T } | { ok: false; error: string };

/** Validates the form against ParamSpecs (min/max, int) and builds TemplateParams. */
export function buildTemplateParams(t: AlertTemplate, form: TemplateForm): BuildResult<TemplateParams> {
  const out: Record<string, unknown> = {};
  for (const spec of t.param_specs) {
    const raw = form[spec.key];
    const text = typeof raw === 'string' ? raw.trim() : '';
    if (spec.type === 'bool') {
      out[spec.key] = raw === true;
      continue;
    }
    if (text === '') return { ok: false, error: `${spec.label} is required.` };
    if (spec.type === 'enum') {
      if (spec.options.length > 0 && !spec.options.includes(text)) return { ok: false, error: `${spec.label} must be one of ${spec.options.join(', ')}.` };
      out[spec.key] = text;
      continue;
    }
    const n = Number(text);
    if (!Number.isFinite(n)) return { ok: false, error: `${spec.label} must be a number.` };
    if (spec.type === 'int' && !Number.isInteger(n)) return { ok: false, error: `${spec.label} must be a whole number.` };
    if (spec.min !== null && n < spec.min) return { ok: false, error: `${spec.label} must be at least ${spec.min}${spec.unit ? ` ${spec.unit}` : ''}.` };
    if (spec.max !== null && n > spec.max) return { ok: false, error: `${spec.label} must be at most ${spec.max}${spec.unit ? ` ${spec.unit}` : ''}.` };
    if (spec.key === 'duration_seconds' && n % 60 !== 0) return { ok: false, error: `${spec.label} must be a multiple of 60 seconds.` };
    out[spec.key] = n;
  }
  if (!t.param_specs.some((s) => s.key === 'severity')) {
    const sev = form.severity;
    if (typeof sev === 'string' && (SEVERITIES as readonly string[]).includes(sev)) out.severity = sev;
  }
  out.enabled = form.enabled !== false;
  const ov = form.channel_override;
  if (Array.isArray(ov) && ov.length > 0) out.channel_override = ov;
  if (t.id === 'uptime_down' && typeof form.instance === 'string' && form.instance !== '') out.instance = form.instance;
  return { ok: true, value: out as TemplateParams };
}

export function isSpecialKey(key: string): boolean {
  return SPECIAL_KEYS.has(key);
}

/** Uptime template: switching the instance prefills the frozen escalation defaults. */
export function uptimeInstanceDefaults(instance: string): { duration_seconds: number; severity: AlertSeverity } {
  return instance === 'critical' ? { duration_seconds: 7200, severity: 'CRITICAL' } : { duration_seconds: 600, severity: 'WARNING' };
}

/** Managed policies installed from a template. */
export function installedPolicies(policies: PolicyView[], id: TemplateId): PolicyView[] {
  return policies.filter((p) => p.managed && p.template_id === id);
}

// ---- policies ---------------------------------------------------------------

export type PolicyFilter = 'all' | 'managed' | 'unmanaged';

export function filterPolicies(policies: PolicyView[], f: PolicyFilter): PolicyView[] {
  if (f === 'all') return policies;
  return policies.filter((p) => (f === 'managed' ? p.managed : !p.managed));
}

export function conditionText(p: PolicyView): string {
  if (p.conditions.length === 0) return 'no conditions';
  return p.conditions.map((c) => c.summary || c.display_name).join('; ');
}

/** The id part of a resource name (`projects/p/alertPolicies/123` → `123`). */
export function resourceId(name: string): string {
  const i = name.lastIndexOf('/');
  return i < 0 ? name : name.slice(i + 1);
}

/** Cloud Console deep link for a policy (projects are ids, not secrets of this repo). */
export function policyConsoleUrl(p: PolicyView): string {
  return `https://console.cloud.google.com/monitoring/alerting/policies/${encodeURIComponent(resourceId(p.name))}?project=${encodeURIComponent(p.project)}`;
}

export function parseRawPolicy(text: string): BuildResult<RawJson> {
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch (e) {
    return { ok: false, error: `Not valid JSON: ${e instanceof Error ? e.message : String(e)}` };
  }
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return { ok: false, error: 'The policy must be a JSON object.' };
  return { ok: true, value: v as RawJson };
}

// ---- adopt --------------------------------------------------------------------

export interface AdoptInfo {
  shape: AdoptShape;
  title: string;
  /** Plain-language explanation of what is wrong and what the fix does. */
  explain: string;
  /** Budgets are handled in the Budgets tab. */
  inBudgetsTab: boolean;
}

export const ADOPT_INFO: Record<AdoptShape, AdoptInfo> = {
  zero_cpu_spike: {
    shape: 'zero_cpu_spike',
    title: 'CPU spike alert covers one VM only',
    explain:
      'This policy watches a single VM, so new VMs are never alerted. The fix installs the all-VM CPU template (80 % for 5 min) that covers every VM including future ones.',
    inBudgetsTab: false,
  },
  high_memory_load: {
    shape: 'high_memory_load',
    title: 'Memory alert fires on a single sample',
    explain:
      'The policy is meant to fire after 15 minutes above 85 %, but its duration is 0 seconds, so one momentary spike fires it. The fix installs the memory template with a true 15-minute duration on all VMs with the Ops Agent.',
    inBudgetsTab: false,
  },
  uptime_backend_down: {
    shape: 'uptime_backend_down',
    title: 'Two separate uptime policies',
    explain:
      'Down-for-10-minutes and down-for-2-hours are two unmanaged policies. The fix installs one managed uptime rule with escalation: a warning after 10 minutes and critical after 2 hours.',
    inBudgetsTab: false,
  },
  uptime_down_2h: {
    shape: 'uptime_down_2h',
    title: 'Two separate uptime policies',
    explain:
      'Down-for-10-minutes and down-for-2-hours are two unmanaged policies. The fix installs one managed uptime rule with escalation: a warning after 10 minutes and critical after 2 hours.',
    inBudgetsTab: false,
  },
  budget_account: {
    shape: 'budget_account',
    title: 'Existing budget has no notification channels',
    explain: 'The existing billing budget only emails billing admins. Adopting it is handled in the Budgets tab.',
    inBudgetsTab: true,
  },
};

/** Unmanaged policies with a recognised shape; one entry per shape (uptime shapes fold into one). */
export function adoptCandidates(policies: PolicyView[]): { info: AdoptInfo; policies: PolicyView[] }[] {
  const byShape = new Map<AdoptShape, PolicyView[]>();
  for (const p of policies) {
    if (p.managed || p.adopt_shape === '') continue;
    const key: AdoptShape = p.adopt_shape === 'uptime_down_2h' ? 'uptime_backend_down' : p.adopt_shape;
    byShape.set(key, [...(byShape.get(key) ?? []), p]);
  }
  const order: AdoptShape[] = ['zero_cpu_spike', 'high_memory_load', 'uptime_backend_down'];
  return order.filter((s) => byShape.has(s)).map((s) => ({ info: ADOPT_INFO[s], policies: byShape.get(s) ?? [] }));
}

// ---- channels / routing ---------------------------------------------------------

export function verificationLabel(c: Pick<AlertChannel, 'type' | 'verification_status'>): string {
  if (c.type !== 'email') return c.verification_status === 'VERIFIED' ? 'verified' : 'n/a';
  switch (c.verification_status) {
    case 'VERIFIED':
      return 'verified';
    case 'UNVERIFIED':
      return 'unverified';
    default:
      return 'unverified (status unset)';
  }
}

export function channelIsWarned(c: AlertChannel): boolean {
  return c.warnings.length > 0 || (c.type === 'email' && c.verification_status !== 'VERIFIED');
}

/** Short warnings for the status strip, e.g. "email channel “Alert” unverified". */
export function channelWarningLines(channels: AlertChannel[]): string[] {
  return channels.filter(channelIsWarned).map((c) => `${c.type} channel “${c.display_name}” ${c.type === 'email' ? 'unverified' : 'needs attention'}`);
}

export function toggleName(list: string[], name: string): string[] {
  return list.includes(name) ? list.filter((n) => n !== name) : [...list, name];
}

export function validateRouting(routing: Record<AlertSeverity, string[]>): BuildResult<Record<AlertSeverity, string[]>> {
  for (const s of SEVERITIES) {
    if (routing[s].length === 0) return { ok: false, error: `${severityLabel(s)} needs at least one channel.` };
  }
  return { ok: true, value: routing };
}

export function routingEqual(a: Record<AlertSeverity, string[]>, b: Record<AlertSeverity, string[]>): boolean {
  return SEVERITIES.every((s) => [...a[s]].sort().join('|') === [...b[s]].sort().join('|'));
}

export function creatableDescriptors(d: ChannelDescriptor[]): ChannelDescriptor[] {
  return d.filter((x) => x.creatable);
}

export function slackGuidance(d: ChannelDescriptor[]): string | null {
  const slack = d.find((x) => x.type === 'slack');
  if (!slack) return null;
  return slack.creatable
    ? null
    : slack.hint || 'Slack channels need console OAuth: create it in Cloud Console → Monitoring → Notification channels, then pick it here.';
}

export function buildChannelDraft(
  project: string,
  descriptor: ChannelDescriptor,
  displayName: string,
  values: Record<string, string>,
): BuildResult<ChannelDraft> {
  if (displayName.trim() === '') return { ok: false, error: 'Display name is required.' };
  const labels: Record<string, string> = {};
  const sensitive: Record<string, string> = {};
  for (const l of descriptor.labels) {
    const v = (values[l.key] ?? '').trim();
    if (v === '') {
      if (l.required) return { ok: false, error: `${l.description || l.key} is required.` };
      continue;
    }
    if (l.sensitive) sensitive[l.key] = v;
    else labels[l.key] = v;
  }
  const draft: ChannelDraft = { project, type: descriptor.type, display_name: displayName.trim(), labels };
  if (Object.keys(sensitive).length > 0) draft.sensitive_labels = sensitive;
  return { ok: true, value: draft };
}
