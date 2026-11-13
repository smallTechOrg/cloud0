// Pure helpers for the Attribution tab: rule validation, editor list operations,
// seed grouping, coverage, CSV. No React, no fetch.

import { csvField, csvNumber } from './csv';
import type {
  AllocateMode,
  AttributionRule,
  Coverage,
  LabelSuggestion,
  MatchField,
  MatchOp,
  UnattributedItem,
} from './attributionTypes';

export const MATCH_FIELDS: MatchField[] = ['label', 'project', 'resource_name', 'sku', 'service'];
export const MATCH_OPS: MatchOp[] = ['equals', 'contains', 'prefix', 'regex'];
export const ALLOCATE_MODES: AllocateMode[] = ['none', 'even', 'proportional'];
export const MAX_RULES = 500;
/** Client-side bound on regex length (RE2 patterns longer than this are almost surely mistakes). */
export const MAX_REGEX_LEN = 200;
export const NAME_RE = /^[a-z0-9][a-z0-9_-]{0,39}$/;

export function normalizeName(s: string): string {
  return s.trim().toLowerCase();
}

export type ActionKind = 'product' | 'environment' | 'shared';

export function actionKind(rule: AttributionRule): ActionKind {
  if (rule.action.shared) return 'shared';
  if (rule.action.product !== undefined && rule.action.product !== '') return 'product';
  if (rule.action.environment !== undefined && rule.action.environment !== '') return 'environment';
  return 'product';
}

/** Validation messages for one rule (empty = valid). Mirrors the server's invalid_rule checks. */
export function validateRule(rule: AttributionRule): string[] {
  const errs: string[] = [];
  const { match, action } = rule;
  if (!Number.isInteger(rule.priority) || rule.priority < 0) errs.push('Priority must be an integer ≥ 0.');
  if (match.field === 'label') {
    if (!match.key || match.key.trim() === '') errs.push('A label match needs a label key.');
  } else if (match.key !== undefined && match.key !== '') {
    errs.push('Only label matches take a key.');
  }
  if (match.value.trim() === '') errs.push('Match value must not be empty.');
  if (match.op === 'regex') {
    if (match.value.length > MAX_REGEX_LEN) errs.push(`Regex is too long (${match.value.length} > ${MAX_REGEX_LEN} characters).`);
    else if (match.value.trim() !== '') {
      try {
        new RegExp(match.value, 'i');
      } catch {
        errs.push('Regex does not compile.');
      }
    }
  }
  const product = (action.product ?? '').trim();
  const env = (action.environment ?? '').trim();
  if (action.shared && product !== '') errs.push('Shared cannot be combined with a product.');
  if (!action.shared && product === '' && env === '') errs.push('The action must set a product, an environment or shared.');
  if (product !== '' && !NAME_RE.test(normalizeName(product))) errs.push('Product must be lowercase letters, digits, "-" or "_" (max 40).');
  if (env !== '' && !NAME_RE.test(normalizeName(env))) errs.push('Environment must be lowercase letters, digits, "-" or "_" (max 40).');
  return errs;
}

export function newRule(priority = 400): AttributionRule {
  return {
    id: null,
    priority,
    match: { field: 'resource_name', op: 'contains', value: '' },
    action: { product: '' },
    enabled: true,
    source: 'user',
    note: null,
  };
}

export function duplicateRule(rule: AttributionRule): AttributionRule {
  return { ...rule, id: null, priority: rule.priority + 1, source: 'user', match: { ...rule.match }, action: { ...rule.action } };
}

/** Next free priority after the highest in the list that stays in the user tier (≥ 400). */
export function nextPriority(rules: AttributionRule[]): number {
  const max = rules.reduce((m, r) => Math.max(m, r.priority), 390);
  return Math.min(max + 10, 999);
}

/** Swaps list positions of i and i+dir and their priorities so evaluation order follows the visible order. */
export function moveRule<T extends { priority: number }>(list: T[], index: number, dir: -1 | 1): T[] {
  const j = index + dir;
  if (j < 0 || j >= list.length) return list;
  const out = list.slice();
  const a = out[index]!;
  const b = out[j]!;
  let pa = b.priority;
  let pb = a.priority;
  if (pa === pb) {
    // Tied priorities: separate them so the new order is explicit.
    if (dir === -1) {
      if (pa > 0) pa -= 1;
      else pb += 1;
    } else {
      pa += 1;
    }
  }
  out[index] = { ...b, priority: pb };
  out[j] = { ...a, priority: pa };
  return out;
}

/** Stable sort by priority (ties keep list order). */
export function sortByPriority<T extends { priority: number }>(list: T[]): T[] {
  return list.map((r, i) => ({ r, i })).sort((x, y) => x.r.priority - y.r.priority || x.i - y.i).map((x) => x.r);
}

/** Request-shape rules: names normalised, key only for label, new rules get id 0. */
export function toWireRule(rule: AttributionRule): AttributionRule {
  const match = { ...rule.match, value: rule.match.value };
  if (match.field !== 'label') delete match.key;
  else match.key = (match.key ?? '').trim();
  const action: AttributionRule['action'] = {};
  if (rule.action.shared) action.shared = true;
  else if ((rule.action.product ?? '').trim() !== '') action.product = normalizeName(rule.action.product ?? '');
  if ((rule.action.environment ?? '').trim() !== '') action.environment = normalizeName(rule.action.environment ?? '');
  return { ...rule, id: rule.id ?? 0, match, action, note: rule.note && rule.note.trim() !== '' ? rule.note : null };
}

export function rulesEqual(a: AttributionRule[], b: AttributionRule[]): boolean {
  return JSON.stringify(a.map(toWireRule)) === JSON.stringify(b.map(toWireRule));
}

/** Extracts the 0-based rule index named by an invalid_rule message ("rules[3]: …", "rule 3 …", "index 3"). */
export function parseInvalidRuleIndex(message: string): number | null {
  const m = /rules?\s*\[\s*(\d+)\s*\]|\brule\s+#?(\d+)|\bindex\s+(\d+)|#(\d+)/i.exec(message);
  if (!m) return null;
  const n = m[1] ?? m[2] ?? m[3] ?? m[4];
  return n === undefined ? null : Number(n);
}

export function describeMatch(rule: AttributionRule): string {
  const { match } = rule;
  const subject = match.field === 'label' ? `label ${match.key ?? ''}` : match.field.replace('_', ' ');
  return `${subject} ${match.op} "${match.value}"`;
}

export function describeAction(rule: AttributionRule): string {
  const parts: string[] = [];
  if (rule.action.shared) parts.push('shared');
  if (rule.action.product) parts.push(`product ${rule.action.product}`);
  if (rule.action.environment) parts.push(`environment ${rule.action.environment}`);
  return parts.length > 0 ? parts.join(' + ') : '(no action)';
}

export function describeRule(rule: AttributionRule): string {
  return `${describeMatch(rule)} → ${describeAction(rule)}`;
}

// ---- Aliases ---------------------------------------------------------------

export interface AliasRow {
  key: string;
  product: string;
}

export function aliasesToRows(aliases: Record<string, string>): AliasRow[] {
  return Object.entries(aliases)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, product]) => ({ key, product }));
}

/** Rows → map; rows with an empty key are dropped; later duplicates win. Errors name the row. */
export function rowsToAliases(rows: AliasRow[]): { aliases: Record<string, string>; errors: string[] } {
  const aliases: Record<string, string> = {};
  const errors: string[] = [];
  rows.forEach((r, i) => {
    const key = r.key.trim().toLowerCase();
    const product = normalizeName(r.product);
    if (key === '' && product === '') return;
    if (key === '') errors.push(`Alias row ${i + 1}: key is empty.`);
    else if (!NAME_RE.test(product)) errors.push(`Alias row ${i + 1}: product must be a valid name.`);
    else aliases[key] = product;
  });
  return { aliases, errors };
}

// ---- Seed preview ----------------------------------------------------------

export interface SeedTier {
  id: 'label' | 'project' | 'name' | 'sku' | 'shared' | 'other';
  title: string;
  range: string;
}

export const SEED_TIERS: SeedTier[] = [
  { id: 'label', title: 'Label values', range: '100–199' },
  { id: 'project', title: 'Project defaults', range: '200–299' },
  { id: 'name', title: 'Resource-name tokens', range: '300–599' },
  { id: 'sku', title: 'SKU / service fallbacks', range: '600–899' },
  { id: 'shared', title: 'Shared fallbacks', range: '900–999' },
  { id: 'other', title: 'Other', range: 'outside 100–999' },
];

export function tierOf(priority: number): SeedTier['id'] {
  if (priority >= 100 && priority <= 199) return 'label';
  if (priority >= 200 && priority <= 299) return 'project';
  if (priority >= 300 && priority <= 599) return 'name';
  if (priority >= 600 && priority <= 899) return 'sku';
  if (priority >= 900 && priority <= 999) return 'shared';
  return 'other';
}

export interface IndexedRule {
  /** Index in the original `proposed` array (stable checkbox identity). */
  index: number;
  rule: AttributionRule;
}

export function groupSeedByTier(rules: AttributionRule[]): { tier: SeedTier; rules: IndexedRule[] }[] {
  return SEED_TIERS.map((tier) => ({
    tier,
    rules: rules.map((rule, index) => ({ rule, index })).filter((x) => tierOf(x.rule.priority) === tier.id),
  })).filter((g) => g.rules.length > 0);
}

/** Parses the seeder's note ("7 resources, 412.30 net") → matched counts; null when absent/unrecognised. */
export function parseSeedNote(note: string | null): { resources: number; net: number } | null {
  if (!note) return null;
  const m = /(\d+)\s+(?:resources?|items?)\s*,\s*(-?[\d.]+)\s+net/i.exec(note);
  if (!m) return null;
  return { resources: Number(m[1]), net: Number(m[2]) };
}

// ---- Coverage ----------------------------------------------------------------

/** Bar segment widths (percent, clamped, summing ≤ 100). */
export function coverageSegments(c: Coverage): { attributed: number; shared: number; unattributed: number } {
  const clamp = (n: number) => Math.max(0, Math.min(100, n));
  const attributed = clamp(c.attributed_pct);
  const shared = Math.min(clamp(c.shared_pct), 100 - attributed);
  const unattributed = Math.min(clamp(c.unattributed_pct), 100 - attributed - shared);
  return { attributed, shared, unattributed };
}

export function formatCoveragePct(n: number): string {
  return `${n.toFixed(1)} %`;
}

export function coverageShift(before: Coverage, after: Coverage): string {
  return `${formatCoveragePct(before.attributed_pct)} → ${formatCoveragePct(after.attributed_pct)}`;
}

// ---- Unattributed -> rule -------------------------------------------------------

export function ruleFromUnattributed(item: UnattributedItem, priority: number): AttributionRule {
  const base = newRule(priority);
  const s = item.suggested;
  const action: AttributionRule['action'] = {};
  if (s?.product) action.product = s.product;
  if (s?.environment) action.environment = s.environment;
  if (!s) action.product = '';
  if (item.kind === 'resource') {
    return { ...base, match: { field: 'resource_name', op: 'equals', value: item.resource_name ?? item.key }, action, note: `from unattributed: ${item.key}` };
  }
  return { ...base, priority: Math.max(priority, 600), match: { field: 'sku', op: 'equals', value: item.sku ?? item.key }, action, note: `from unattributed: ${item.service}` };
}

export function reasonLabel(reason: UnattributedItem['reason']): string {
  switch (reason) {
    case 'no_resource_name':
      return 'No resource name';
    case 'no_matching_rule':
      return 'No matching rule';
    case 'environment_only':
      return 'Environment only';
  }
}

// ---- Label suggestions -------------------------------------------------------------

export function labelProgress(done: number, remaining: number): { text: string; pct: number } {
  const total = done + remaining;
  return { text: `${done} of ${total} resources labelled`, pct: total === 0 ? 100 : Math.round((done / total) * 100) };
}

export function formatLabels(labels: Record<string, string>): string {
  return Object.entries(labels)
    .map(([k, v]) => `${k}=${v}`)
    .join(', ');
}

export function resourceTypeLabel(t: LabelSuggestion['resource_type']): string {
  return t === 'unknown' ? 'unknown' : t.replace(/_/g, ' ');
}

// ---- CSV ---------------------------------------------------------------------------

const CRLF = '\r\n';

function toCsv(header: string[], rows: string[][]): string {
  return [header, ...rows].map((r) => r.map(csvField).join(',')).join(CRLF) + CRLF;
}

export function buildUnattributedCsv(items: UnattributedItem[]): string {
  return toCsv(
    ['kind', 'project', 'service', 'resource_or_sku', 'net', 'share_pct', 'line_items', 'reason', 'suggested_product', 'suggested_environment', 'confidence'],
    items.map((i) => [
      i.kind,
      i.project_id,
      i.service,
      i.key,
      csvNumber(i.net),
      csvNumber(i.share_pct),
      String(i.line_items),
      i.reason,
      i.suggested?.product ?? '',
      i.suggested?.environment ?? '',
      i.suggested?.confidence ?? '',
    ]),
  );
}

export function buildLabelSuggestionsCsv(items: LabelSuggestion[]): string {
  return toCsv(
    ['project', 'resource', 'type', 'location', 'net', 'labels_to_add', 'command', 'risk'],
    items.map((i) => [i.project_id, i.resource_name, i.resource_type, i.region_or_zone ?? '', csvNumber(i.net), formatLabels(i.labels), i.command ?? '', i.risk]),
  );
}

export function attributionCsvFilename(kind: 'unattributed' | 'label-suggestions', week: string, scope: string): string {
  return `cloud0-${kind}-${scope.replace(/[^A-Za-z0-9._-]/g, '_')}-${week}.csv`;
}

// ---- Proposer state -----------------------------------------------------------------

export const PROPOSER_DISABLED_TEXT =
  'AI rule suggestions are off: set an OpenRouter key (CLOUD0_OPENROUTER_API_KEY in .env) and restart to enable. Nothing is applied automatically.';
