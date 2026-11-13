import { describe, expect, it } from 'vitest';
import {
  autoName,
  basisText,
  buildDraft,
  buildHistoryCsv,
  defaultThresholds,
  ensureManagedName,
  formFromBudget,
  historyCsvFilename,
  initialForm,
  monthScopeFor,
  parseAmount,
  scopeLabel,
  spendProgress,
  validateName,
  validateThresholds,
} from './budgetsFormat';
import { budgetSuggestionsFixture, budgetsFixture, historyFixture } from './fixtures3';

const suggestion = budgetSuggestionsFixture.suggestions[0]!;
const unmanaged = budgetsFixture.budgets[0]!;

describe('parseAmount', () => {
  it('accepts plain, grouped and rupee-prefixed numbers', () => {
    expect(parseAmount('6480')).toEqual({ ok: true, value: 6480 });
    expect(parseAmount('₹6,480.50')).toEqual({ ok: true, value: 6480.5 });
    expect(parseAmount(' 12 ')).toEqual({ ok: true, value: 12 });
  });
  it('rejects empty, zero, negative, text, too many decimals and absurd values', () => {
    for (const bad of ['', '  ', '0', '-5', 'abc', '1e3', '12.345', '2000000000']) {
      expect(parseAmount(bad).ok).toBe(false);
    }
  });
});

describe('validateThresholds', () => {
  it('accepts the defaults', () => {
    expect(validateThresholds(defaultThresholds())).toEqual([]);
  });
  it('flags empty, non-positive, too large and duplicate thresholds', () => {
    expect(validateThresholds([])).toHaveLength(1);
    expect(validateThresholds([{ percent: 0, basis: 'current_spend' }])[0]).toContain('greater than 0');
    expect(validateThresholds([{ percent: 5000, basis: 'current_spend' }])[0]).toContain('at most');
    const dup = validateThresholds([
      { percent: 100, basis: 'current_spend' },
      { percent: 100, basis: 'current_spend' },
    ]);
    expect(dup[0]).toContain('twice');
    // same percent, different basis is fine
    expect(validateThresholds([{ percent: 100, basis: 'current_spend' }, { percent: 100, basis: 'forecasted_spend' }])).toEqual([]);
  });
});

describe('basisText', () => {
  it('states multiplier, weeks and the INR amount', () => {
    expect(basisText(suggestion.basis, 6480)).toBe('1.2 × average of the last 4 weeks = ₹6,480.00');
  });
  it('singular week, setting and none methods', () => {
    expect(basisText({ ...suggestion.basis, weeks_used: 1 }, 100)).toContain('last 1 week =');
    expect(basisText({ ...suggestion.basis, method: 'monthly_total_setting', multiplier: 1 }, 5000)).toContain('target.monthly_total');
    expect(basisText({ ...suggestion.basis, method: 'none', explanation: 'refresh first' }, 0)).toBe('refresh first');
  });
});

describe('names', () => {
  it('requires the cloud0- prefix', () => {
    expect(validateName('cloud0-account-monthly')).toBeNull();
    expect(validateName('Example-budget')).toContain('cloud0-');
    expect(validateName('cloud0-')).not.toBeNull();
    expect(validateName('')).not.toBeNull();
  });
  it('ensureManagedName slugs unmanaged names', () => {
    expect(ensureManagedName('Example-budget')).toBe('cloud0-example-budget');
    expect(ensureManagedName('cloud0-x')).toBe('cloud0-x');
  });
  it('autoName by scope', () => {
    expect(autoName('account', [], [])).toBe('cloud0-account-monthly');
    expect(autoName('services', ['Cloud Run', 'Cloud SQL'], [])).toBe('cloud0-cloud-run-cloud-sql-monthly');
    expect(autoName('projects', [], ['proj-a'])).toBe('cloud0-proj-a-monthly');
  });
});

describe('forms and drafts', () => {
  it('prefills from a suggestion and builds the draft with the INR amount and defaults', () => {
    const f = initialForm(suggestion);
    expect(f.amount).toBe('6480');
    const { draft, errors } = buildDraft(f);
    expect(errors).toEqual({});
    expect(draft?.amount).toBe(6480);
    expect(draft?.thresholds).toHaveLength(4);
    expect(draft?.channel_override).toBeUndefined();
    expect(draft?.default_iam_recipients).toBe(true);
  });
  it('reports field errors and no draft', () => {
    const { draft, errors } = buildDraft({ ...initialForm(), amount: '', scope: 'services' });
    expect(draft).toBeNull();
    expect(errors.amount).toBeDefined();
    expect(errors.scope).toBeDefined();
  });
  it('channel override requires a channel', () => {
    const { errors } = buildDraft({ ...initialForm(suggestion), overrideChannels: true, channels: [] });
    expect(errors.channels).toBeDefined();
  });
  it('adopt copies the unmanaged budget under a cloud0- name', () => {
    const f = formFromBudget(unmanaged, true);
    expect(f.name).toBe('cloud0-example-budget');
    expect(f.scope).toBe('projects');
    expect(f.projects).toEqual(['proj-a']);
    expect(buildDraft(f).draft?.amount).toBe(5000);
  });
});

describe('scope and progress', () => {
  it('scopeLabel', () => {
    expect(scopeLabel({ projects: [], service_ids: [] })).toBe('Whole billing account');
    expect(scopeLabel(unmanaged)).toBe('Project proj-a');
    expect(scopeLabel({ projects: [], service_ids: ['services/x'] }, { 'services/x': 'Cloud Run' })).toBe('Services Cloud Run');
  });
  it('monthScopeFor', () => {
    expect(monthScopeFor({ projects: [], service_ids: [] })).toBe('all');
    expect(monthScopeFor({ projects: ['proj-a'], service_ids: [] })).toBe('proj-a');
    expect(monthScopeFor({ projects: ['a', 'b'], service_ids: [] })).toBeNull();
    expect(monthScopeFor({ projects: [], service_ids: ['s'] })).toBeNull();
  });
  it('spendProgress tones and clamping', () => {
    expect(spendProgress(500, 5000)).toMatchObject({ pct: 10, tone: 'ok' });
    expect(spendProgress(4200, 5000)?.tone).toBe('near');
    expect(spendProgress(7500, 5000)).toMatchObject({ width: 100, tone: 'over' });
    expect(spendProgress(10, null)).toBeNull();
    expect(spendProgress(10, 0)).toBeNull();
  });
});

describe('history csv', () => {
  it('has a header, CRLF lines and quoted JSON', () => {
    const csv = buildHistoryCsv(historyFixture.entries);
    const lines = csv.split('\r\n');
    expect(lines[0]).toBe('id,at,actor,kind,action,target,before,after,preview_hash');
    expect(lines[1]).toContain('12,2026-10-27T09:02:00Z,local-user,policy,create');
    expect(lines[1]).toContain('"{""name""');
    expect(csv.endsWith('\r\n')).toBe(true);
  });
  it('filename includes the kind', () => {
    expect(historyCsvFilename('')).toBe('cloud0-change-history.csv');
    expect(historyCsvFilename('budget')).toBe('cloud0-change-history-budget.csv');
  });
});
