import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { MAX_REGEX_LEN, newRule } from '@/lib/attributionFormat';
import { attributionRulesFixture, costByProductFixture, explainFixture, labelSuggestionsFixture, proposeFixture, seedPreviewFixture, unattributedFixture } from '@/lib/fixtures4';
import AttributionPanel, { CoverageHeader, type CoverageHeaderProps } from './AttributionPanel';
import ExplainDrawer, { type ExplainDrawerProps } from './ExplainDrawer';
import LabelSuggestions, { type LabelSuggestionsProps } from './LabelSuggestions';
import ProposeDialog, { type ProposeDialogProps } from './ProposeDialog';
import RulesEditor, { type EditorRule, type RulesEditorProps } from './RulesEditor';
import SeedPreview, { type SeedPreviewProps } from './SeedPreview';
import UnattributedList, { type UnattributedListProps } from './UnattributedList';

const noop = () => undefined;
const html = <P extends object>(c: (p: P) => React.ReactElement | null, p: P) => renderToStaticMarkup(createElement(c as never, p as never));

const drafts: EditorRule[] = attributionRulesFixture.rules.map((rule, i) => ({ key: `k${i}`, rule }));

function editor(over: Partial<RulesEditorProps> = {}): string {
  return html(RulesEditor, {
    drafts,
    allocate: 'none',
    aliasRows: [{ key: 'example-alias', product: 'example-product' }],
    aliasErrors: [],
    serverErrors: {},
    dirty: false,
    saving: false,
    saveError: null,
    savedAt: null,
    newKey: () => 'n',
    onDrafts: noop,
    onAllocate: noop,
    onAliasRows: noop,
    onSave: noop,
    onDiscard: noop,
    ...over,
  } satisfies RulesEditorProps);
}

function coverage(over: Partial<CoverageHeaderProps> = {}): string {
  return html(CoverageHeader, { coverage: costByProductFixture.coverage, loading: false, error: null, ruleCount: 4, enabledCount: 4, noSnapshot: false, week: '2026-W43', ...over });
}

describe('CoverageHeader', () => {
  it('shows pct, counts, unattributed and shared net and the bar', () => {
    const h = coverage();
    expect(h).toContain('data-testid="coverage-pct"');
    expect(h).toContain('87.0 %');
    expect(h).toContain('data-testid="coverage-bar"');
    expect(h).toContain('62.50'); // unattributed net, formatted money
    expect(h).toContain('(4 enabled)');
  });
  it('loading, error, no-snapshot states', () => {
    expect(coverage({ coverage: null, loading: true })).toContain('loading coverage');
    expect(coverage({ coverage: null, loading: false, error: 'boom' })).toContain('data-testid="coverage-error"');
    expect(coverage({ coverage: null })).not.toContain('data-testid="coverage-pct"');
    expect(coverage({ noSnapshot: true })).toContain('No snapshot for 2026-W43');
  });
});

describe('RulesEditor', () => {
  it('renders the ordered table with toggles, source badges, builders and move buttons', () => {
    const h = editor();
    expect(h).toContain('data-testid="rules-table"');
    expect(h).toContain('data-testid="rule-row-3"');
    expect(h).toContain('data-source="seed"');
    expect(h).toContain('data-source="user"');
    expect(h).toContain('data-testid="rule-key-0"'); // label rule has a key input
    expect(h).not.toContain('data-testid="rule-key-1"');
    expect(h).toContain('data-testid="rule-up-0"');
    expect(h).toMatch(/data-testid="rule-up-0"[^>]*disabled/);
    expect(h).toMatch(/data-testid="rule-down-3"[^>]*disabled/);
    expect(h).toContain('data-testid="rule-add"');
    expect(h).toContain('data-testid="allocate-select"');
    expect(h).toContain('data-testid="alias-key-0"');
  });
  it('Save is disabled when clean and enabled when dirty+valid', () => {
    expect(editor()).toMatch(/data-testid="rules-save"[^>]*disabled/);
    const dirty = editor({ dirty: true });
    expect(dirty).not.toMatch(/data-testid="rules-save"[^>]*disabled/);
    expect(dirty).toContain('Unsaved changes');
  });
  it('empty state points at Auto-seed', () => {
    const h = editor({ drafts: [] });
    expect(h).toContain('data-testid="rules-empty"');
    expect(h).toContain('Auto-seed');
  });
  it('inline validation: empty value/action, regex length bound, label key', () => {
    const long = { ...newRule(400), match: { field: 'sku' as const, op: 'regex' as const, value: 'a'.repeat(MAX_REGEX_LEN + 1) }, action: { product: 'x' } };
    const h = editor({ drafts: [{ key: 'a', rule: newRule(400) }, { key: 'b', rule: long }, { key: 'c', rule: { ...newRule(400), match: { field: 'label', op: 'equals', value: 'v' }, action: { product: 'x' } } }], dirty: true });
    expect(h).toContain('Match value must not be empty.');
    expect(h).toContain('The action must set a product');
    expect(h).toContain('Regex is too long');
    expect(h).toContain(`${MAX_REGEX_LEN + 1} / ${MAX_REGEX_LEN} characters`);
    expect(h).toContain('A label match needs a label key.');
    expect(h).toMatch(/data-testid="rules-save"[^>]*disabled/);
    expect(h).toContain('data-testid="rules-invalid-count"');
  });
  it('shows invalid_rule server errors on the offending row only', () => {
    const h = editor({ serverErrors: { 2: 'rules[2]: regex does not compile' }, saveError: 'invalid_rule — rules[2]: regex does not compile Nothing was saved.', dirty: true });
    expect(h).toContain('data-testid="rule-server-error-2"');
    expect(h).not.toContain('data-testid="rule-server-error-1"');
    expect(h).toContain('data-testid="rules-save-error"');
  });
  it('disabled rules are visibly distinct', () => {
    const h = editor({ drafts: [{ key: 'a', rule: { ...attributionRulesFixture.rules[0]!, enabled: false } }] });
    expect(h).toContain('data-enabled="false"');
  });
  it('alias errors block save', () => {
    const h = editor({ dirty: true, aliasErrors: ['Alias row 1: key is empty.'] });
    expect(h).toContain('data-testid="alias-error"');
    expect(h).toMatch(/data-testid="rules-save"[^>]*disabled/);
  });
});

function seed(over: Partial<SeedPreviewProps> = {}): string {
  return html(SeedPreview, {
    preview: seedPreviewFixture,
    loading: false,
    applying: false,
    error: null,
    selected: new Set([0]),
    sharedTokens: '',
    applyBlocked: null,
    appliedMessage: null,
    onSharedTokens: noop,
    onPreview: noop,
    onToggle: noop,
    onToggleAll: noop,
    onApply: noop,
    ...over,
  } satisfies SeedPreviewProps);
}

describe('SeedPreview', () => {
  it('before preview: only the Preview button', () => {
    const h = seed({ preview: null });
    expect(h).toContain('data-testid="seed-preview-btn"');
    expect(h).toContain('Preview seed');
    expect(h).not.toContain('data-testid="seed-preview"');
  });
  it('shows tiered proposals with matched counts, net, coverage before → after and per-rule checkbox', () => {
    const h = seed();
    expect(h).toContain('data-testid="seed-tier-name"');
    expect(h).toContain('59.6 % → 91.9 %');
    expect(h).toContain('data-testid="seed-check-0"');
    expect(h).toContain('checked');
    expect(h).toContain('412.30'); // net matched
    expect(h).toContain('Apply seed (1)');
    expect(h).toContain('example-product');
  });
  it('Apply disabled with explanation when blocked or nothing selected', () => {
    const blocked = seed({ applyBlocked: 'Save or discard your rule edits before applying a seed.' });
    expect(blocked).toMatch(/data-testid="seed-apply"[^>]*disabled/);
    expect(blocked).toContain('data-testid="seed-apply-blocked"');
    expect(seed({ selected: new Set() })).toMatch(/data-testid="seed-apply"[^>]*disabled/);
  });
  it('warnings, loading, error and applied states', () => {
    expect(seed({ preview: { ...seedPreviewFixture, warnings: ['Labels not captured for this week; refresh it to enable label rules.'] } })).toContain('data-testid="seed-warning"');
    expect(seed({ preview: null, loading: true })).toContain('loading seed preview');
    expect(seed({ preview: null, error: 'invalid_week — nope' })).toContain('data-testid="seed-error"');
    expect(seed({ preview: null, appliedMessage: 'Applied 14 seed rules.' })).toContain('Applied 14 seed rules.');
  });
  it('no-snapshot preview shows only the warning', () => {
    const h = seed({ preview: { ...seedPreviewFixture, snapshot: null, proposed: [], warnings: ['No snapshot for this week — Refresh first.'] } });
    expect(h).toContain('No snapshot for this week');
    expect(h).not.toContain('data-testid="seed-apply"');
  });
});

function unattr(over: Partial<UnattributedListProps> = {}): string {
  return html(UnattributedList, { data: unattributedFixture, loading: false, error: null, onCreateRule: noop, onExplain: noop, onExport: noop, ...over } satisfies UnattributedListProps);
}

describe('UnattributedList', () => {
  it('lists resources with suggested chips, create-rule and explain actions', () => {
    const h = unattr();
    expect(h).toContain('data-testid="unattributed-table"');
    expect(h).toContain('example-orphan-vm');
    expect(h).toContain('product: example-orphan');
    expect(h).toContain('data-testid="unattributed-create-0"');
    expect(h).toContain('data-testid="unattributed-explain-0"');
    expect(h).not.toContain('data-testid="unattributed-explain-1"'); // SKU rows can't be explained
    expect(h).toContain('no suggestion');
    expect(h).toContain('Network Inter Region Egress');
  });
  it('empty, no snapshot, loading, error, truncated', () => {
    expect(unattr({ data: { ...unattributedFixture, items: [], item_count: 0 } })).toContain('data-testid="unattributed-empty"');
    expect(unattr({ data: { ...unattributedFixture, snapshot: null, items: [] } })).toContain('data-testid="unattributed-nosnapshot"');
    expect(unattr({ data: null, loading: true })).toContain('loading unattributed');
    expect(unattr({ data: null, error: 'unreachable — x' })).toContain('data-testid="unattributed-error"');
    expect(unattr({ data: null })).toContain('data-testid="unattributed-unavailable"');
    expect(unattr({ data: { ...unattributedFixture, truncated: true } })).toContain('data-testid="unattributed-truncated"');
  });
  it('export disabled with no items', () => {
    expect(unattr({ data: { ...unattributedFixture, items: [] } })).toMatch(/data-testid="unattributed-export"[^>]*disabled/);
  });
});

function labels(over: Partial<LabelSuggestionsProps> = {}): string {
  return html(LabelSuggestions, { data: labelSuggestionsFixture, loading: false, error: null, copiedKey: null, onCopy: noop, onExport: noop, ...over } satisfies LabelSuggestionsProps);
}

describe('LabelSuggestions', () => {
  it('shows type, labels, risk, gcloud command with Copy and progress', () => {
    const h = labels();
    expect(h).toContain('data-testid="label-command-0"');
    expect(h).toContain('gcloud compute instances add-labels example-vm');
    expect(h).toContain('data-testid="label-copy-0"');
    expect(h).toContain('compute instance');
    expect(h).toContain('product=example-product, env=prod');
    expect(h).toContain('Metadata-only change');
    expect(h).toContain('5 of 7 resources labelled');
    expect(h).toContain('role="progressbar"');
  });
  it('copied feedback, unknown type without command, empty, no snapshot, loading, error', () => {
    expect(labels({ copiedKey: 'proj-a/example-vm' })).toContain('Copied');
    const unknown = { ...labelSuggestionsFixture, items: [{ ...labelSuggestionsFixture.items[0]!, resource_type: 'unknown' as const, command: null }] };
    expect(labels({ data: unknown })).toContain('label it in the console');
    expect(labels({ data: { ...labelSuggestionsFixture, items: [], count: 0 } })).toContain('data-testid="labels-empty"');
    expect(labels({ data: { ...labelSuggestionsFixture, snapshot: null, items: [] } })).toContain('data-testid="labels-nosnapshot"');
    expect(labels({ data: null, loading: true })).toContain('loading label suggestions');
    expect(labels({ data: null, error: 'boom' })).toContain('data-testid="labels-error"');
  });
});

function propose(over: Partial<ProposeDialogProps> = {}): string {
  return html(ProposeDialog, { state: { status: 'idle' }, configured: true, model: 'example/model', selected: new Set([0]), onPropose: noop, onToggle: noop, onAddToEditor: noop, onDismiss: noop, ...over } satisfies ProposeDialogProps);
}

describe('ProposeDialog', () => {
  it('button is always rendered; enabled when configured and states that nothing is auto-applied', () => {
    const h = propose();
    expect(h).toContain('data-testid="propose-btn"');
    expect(h).not.toMatch(/data-testid="propose-btn"[^>]*disabled/);
    expect(h).toContain('nothing is applied automatically');
  });
  it('is disabled and explained when the proposer is not configured', () => {
    for (const h of [propose({ configured: false }), propose({ state: { status: 'disabled', hint: 'Set CLOUD0_OPENROUTER_API_KEY in .env and restart to enable LLM rule proposals.' } })]) {
      expect(h).toContain('data-testid="propose-btn"');
      expect(h).toMatch(/data-testid="propose-btn"[^>]*disabled/);
      expect(h).toContain('set an OpenRouter key');
      expect(h).toContain('Nothing is applied automatically');
    }
  });
  it('error, loading and ready states', () => {
    expect(propose({ state: { status: 'error', code: 'proposer_failed', message: 'bad output', hint: null } })).toContain('No rules were changed');
    expect(propose({ state: { status: 'loading' } })).toContain('Asking…');
    const ready = propose({ state: { status: 'ready', data: proposeFixture } });
    expect(ready).toContain('data-testid="proposal-0"');
    expect(ready).toContain('example-orphan');
    expect(ready).toContain('disabled drafts');
    expect(ready).toContain('94.4 %');
    expect(propose({ state: { status: 'ready', data: { ...proposeFixture, proposals: [], dropped: 2 } } })).toContain('data-testid="propose-none"');
  });
});

function explain(over: Partial<ExplainDrawerProps> = {}, state: ExplainDrawerProps['state'] = { loading: false, error: null, data: null }): string {
  return html(ExplainDrawer, { query: '', state, onQuery: noop, onSubmit: noop, onClose: noop, ...over } satisfies ExplainDrawerProps);
}

describe('ExplainDrawer', () => {
  it('idle: input and disabled Explain button', () => {
    const h = explain();
    expect(h).toContain('data-testid="explain-input"');
    expect(h).toMatch(/data-testid="explain-btn"[^>]*disabled/);
  });
  it('renders the rule trail with matched / applied flags', () => {
    const h = explain({ query: 'example-web' }, { loading: false, error: null, data: explainFixture });
    expect(h).toContain('data-testid="explain-trail-0"');
    expect(h).toContain('data-testid="trail-step-0-3"');
    expect(h).toContain('data-matched="true" data-applied="product"');
    expect(h).toContain('data-matched="false" data-applied="none"');
    expect(h).toContain('built-in');
    expect(h).toContain('applied: environment');
    expect(h).toContain('product: example-product');
  });
  it('error state', () => {
    expect(explain({ query: 'x' }, { loading: false, error: 'not_found — none', data: null })).toContain('data-testid="explain-error"');
  });
});

describe('AttributionPanel (initial render)', () => {
  it('renders all sections with loading skeletons before data arrives', () => {
    const h = renderToStaticMarkup(createElement(AttributionPanel, { week: '2026-W43', scope: 'all', refreshToken: 0 }));
    expect(h).toContain('data-testid="attribution-panel"');
    expect(h).toContain('data-testid="coverage-header"');
    expect(h).toContain('loading rules');
    for (const id of ['seed-section', 'propose-section', 'unattributed-section', 'explain-drawer', 'labels-section']) expect(h).toContain(`data-testid="${id}"`);
    expect(h).toContain('data-testid="propose-btn"'); // always shown
  });
});
