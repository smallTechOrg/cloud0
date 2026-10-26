import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { formFromBudget, initialForm } from '@/lib/budgetsFormat';
import { budgetSuggestionsFixture, budgetsFixture, channelsFixture, historyFixture, routingFixture } from '@/lib/fixtures3';
import type { BudgetView } from '@/lib/types';
import { BudgetCard } from './BudgetCard';
import { BudgetForm } from './BudgetForm';
import { BudgetsSetupHint } from './BudgetsPanel';
import { ChangeHistoryTable } from './ChangeHistoryTable';
import { SuggestionList } from './SuggestionList';

const noop = () => undefined;
const unmanaged = budgetsFixture.budgets[0] as BudgetView;
const suggestion = budgetSuggestionsFixture.suggestions[0]!;

function card(over: Record<string, unknown> = {}, b: BudgetView = unmanaged) {
  return renderToStaticMarkup(
    createElement(BudgetCard, { budget: b, currency: 'INR', canWrite: true, onAdopt: noop, onEdit: noop, onDelete: noop, ...over }),
  );
}

describe('BudgetCard', () => {
  it('shows an unmanaged budget with INR amount, scope, thresholds, channels and adopt', () => {
    const html = card();
    expect(html).toContain('Example-budget');
    expect(html).toContain('>Unmanaged<');
    expect(html).toContain('₹5,000.00');
    expect(html).toContain('Project proj-a');
    expect(html).toContain('50 % spend');
    expect(html).toContain('90 % spend');
    expect(html).toContain('No monitoring channels');
    expect(html).toContain('No monitoring notification channels');
    expect(html).toContain('data-testid="budget-adopt"');
    expect(html).toContain('data-testid="budget-edit"');
    expect(html).toContain('data-testid="budget-delete"');
  });
  it('renders the month-to-date progress bar with tone', () => {
    const html = card({ spent: 4200, month: '2026-10' });
    expect(html).toContain('data-testid="budget-bar"');
    expect(html).toContain('data-tone="near"');
    expect(html).toContain('width:84%');
    expect(html).toContain('₹4,200.00 spent in 2026-10');
    expect(card({ spent: null })).toContain('budget-no-progress');
  });
  it('managed budget has no adopt button and forecast chip', () => {
    const managed: BudgetView = { ...unmanaged, managed: true, display_name: 'cloud0-account-monthly', adopt_shape: '', thresholds: [{ percent: 100, basis: 'forecasted_spend' }], warnings: [] };
    const html = card({}, managed);
    expect(html).toContain('>Managed<');
    expect(html).not.toContain('budget-adopt');
    expect(html).toContain('100 % forecast');
  });
  it('disables write actions with the setup hint when alerts are not configured', () => {
    const html = card({ canWrite: false });
    expect(html).toContain('disabled=""');
    expect(html).toContain('CLOUD0_ALERTS_CREDENTIALS');
  });
  it('renders the inline delete confirmation', () => {
    const html = card({ initialConfirmDelete: true });
    expect(html).toContain('budget-delete-confirm');
    expect(html).toContain('Preview delete');
    expect(html).toContain('nothing is removed until you apply it');
  });
});

function form(over: Record<string, unknown> = {}) {
  return renderToStaticMarkup(
    createElement(BudgetForm, {
      initial: initialForm(suggestion),
      mode: 'create',
      suggestions: budgetSuggestionsFixture.suggestions,
      channels: channelsFixture.channels,
      routing: routingFixture,
      projects: ['proj-a'],
      currency: 'INR',
      canWrite: true,
      onSubmit: noop,
      onCancel: noop,
      ...over,
    }),
  );
}

describe('BudgetForm', () => {
  it('prefills the suggested amount with the visible basis, the auto name and the default thresholds', () => {
    const html = form();
    expect(html).toContain('value="6480"');
    expect(html).toContain('1.2 × average of the last 4 weeks = ₹6,480.00');
    expect(html).toContain('value="cloud0-account-monthly"');
    expect(html.match(/data-testid="budget-threshold-row"/g)).toHaveLength(4);
    expect(html).toContain('of forecast');
    expect(html).toContain('Following routing for ERROR');
    expect(html).toContain('Nothing changes in GCP until you apply the preview.');
  });
  it('shows field errors', () => {
    const html = form({ initialErrors: { amount: 'Enter an amount.', thresholds: 'Add at least one threshold.' } });
    expect(html).toContain('Enter an amount.');
    expect(html).toContain('Add at least one threshold.');
    expect(html).toContain('role="alert"');
  });
  it('adopt mode explains the managed copy and prefills from the unmanaged budget', () => {
    const html = form({ mode: 'adopt', initial: formFromBudget(unmanaged, true) });
    expect(html).toContain('Adopt budget as managed copy');
    expect(html).toContain('value="cloud0-example-budget"');
    expect(html).toContain('value="5000"');
    expect(html).toContain('the original budget stays untouched');
  });
  it('submit is disabled without write credentials', () => {
    expect(form({ canWrite: false })).toMatch(/data-testid="budget-submit"[^>]*disabled/);
  });
});

describe('SuggestionList', () => {
  it('lists suggestions with the basis line and a Create shortcut', () => {
    const html = renderToStaticMarkup(createElement(SuggestionList, { suggestions: budgetSuggestionsFixture.suggestions, currency: 'INR', canWrite: true, onCreate: noop }));
    expect(html).toContain('All services');
    expect(html).toContain('₹6,480.00');
    expect(html).toContain('1.2 × average of the last 4 weeks');
    expect(html).toContain('suggestion-create');
  });
  it('marks existing budgets and renders the empty state', () => {
    const exists = renderToStaticMarkup(createElement(SuggestionList, { suggestions: [{ ...suggestion, exists: true }], currency: 'INR', canWrite: true, onCreate: noop }));
    expect(exists).toContain('Budget exists');
    expect(exists).not.toContain('suggestion-create');
    expect(renderToStaticMarkup(createElement(SuggestionList, { suggestions: [], currency: 'INR', canWrite: true, onCreate: noop }))).toContain('refresh a week first');
  });
});

describe('ChangeHistoryTable', () => {
  const props = { entries: historyFixture.entries, kind: '', onKind: noop, onExport: noop };
  it('renders who/when/what rows and the kind filter', () => {
    const html = renderToStaticMarkup(createElement(ChangeHistoryTable, props));
    expect(html).toContain('local-user');
    expect(html).toContain('create');
    expect(html).toContain('Show JSON');
    expect(html).toContain('Export CSV');
    for (const k of ['Policies', 'Uptime checks', 'Budgets', 'Snoozes', 'Channels']) expect(html).toContain(k);
    expect(html).not.toContain('history-json');
  });
  it('expands before/after JSON', () => {
    const html = renderToStaticMarkup(createElement(ChangeHistoryTable, { ...props, initialExpanded: [12] }));
    expect(html).toContain('history-json');
    expect(html).toContain('Before');
    expect(html).toContain('cloud0: VM CPU high');
    expect(html).toContain('Hide JSON');
  });
  it('empty state and disabled export', () => {
    const html = renderToStaticMarkup(createElement(ChangeHistoryTable, { ...props, entries: [], kind: 'budget' }));
    expect(html).toContain('No changes recorded for budgets yet');
    expect(html).toMatch(/data-testid="history-export"[^>]*disabled/);
  });
});

describe('BudgetsSetupHint', () => {
  it('shows the plain setup hint', () => {
    const html = renderToStaticMarkup(createElement(BudgetsSetupHint));
    expect(html).toContain('not set up yet');
    expect(html).toContain('CLOUD0_ALERTS_CREDENTIALS');
    expect(html).toContain('README');
  });
});
