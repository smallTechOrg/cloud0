import { useState } from 'react';
import {
  autoName,
  buildDraft,
  defaultThresholds,
  formatThresholdLabel,
  sortThresholds,
  suggestedFor,
  type FormState,
  type ScopeKind,
} from '@/lib/budgetsFormat';
import type { AlertChannel, AlertSeverity, BudgetDraft, BudgetSuggestion, BudgetThreshold, RoutingResponse } from '@/lib/types';
import styles from './BudgetsPanel.module.css';

export type BudgetFormMode = 'create' | 'edit' | 'adopt';

export interface BudgetFormProps {
  initial: FormState;
  mode: BudgetFormMode;
  /** Editing an unmanaged budget in place: the cloud0- name prefix is not required. */
  unmanagedEdit?: boolean;
  suggestions: BudgetSuggestion[];
  channels: AlertChannel[];
  routing: RoutingResponse | null;
  projects: string[];
  currency: string;
  canWrite: boolean;
  /** Called with a validated draft; the panel opens the preview drawer. */
  onSubmit: (draft: BudgetDraft) => void;
  onCancel: () => void;
  /** Tests render validation errors statically. */
  initialErrors?: Record<string, string>;
}

const SEVERITIES: AlertSeverity[] = ['CRITICAL', 'ERROR', 'WARNING'];
const SCOPES: { id: ScopeKind; label: string }[] = [
  { id: 'account', label: 'Account-wide' },
  { id: 'services', label: 'Per service' },
  { id: 'projects', label: 'Per project' },
];
const TITLES: Record<BudgetFormMode, string> = { create: 'Create budget', edit: 'Edit budget', adopt: 'Adopt budget as managed copy' };

function toggle(list: string[], v: string): string[] {
  return list.includes(v) ? list.filter((x) => x !== v) : [...list, v];
}

export function BudgetForm({
  initial,
  mode,
  unmanagedEdit = false,
  suggestions,
  channels,
  routing,
  projects,
  currency,
  canWrite,
  onSubmit,
  onCancel,
  initialErrors = {},
}: BudgetFormProps) {
  const [f, setF] = useState<FormState>(initial);
  const [nameTouched, setNameTouched] = useState(mode !== 'create');
  const [amountTouched, setAmountTouched] = useState(mode !== 'create' || initial.amount !== '');
  const [errors, setErrors] = useState<Record<string, string>>(initialErrors);

  const serviceSuggestions = suggestions.filter((s) => s.scope === 'service');
  const nameOf = (id: string) => serviceSuggestions.find((s) => s.service_id === id)?.service_name ?? id;

  /** Apply a scope/selection change, re-deriving the auto name and suggested amount unless the user edited them. */
  function change(patch: Partial<FormState>) {
    setF((cur) => {
      const next = { ...cur, ...patch };
      if (!nameTouched) next.name = autoName(next.scope, next.serviceIds.map(nameOf), next.projects);
      if (!amountTouched) {
        const s = suggestedFor(next.scope, next.serviceIds, suggestions, currency);
        next.amount = s ? String(s.amount) : '';
      }
      return next;
    });
  }

  const suggested = suggestedFor(f.scope, f.serviceIds, suggestions, currency);
  const severityRouting = routing?.routing[f.severity] ?? [];
  const channelLabel = (n: string) => channels.find((c) => c.name === n)?.display_name ?? n.split('/').pop() ?? n;

  function setThreshold(i: number, patch: Partial<BudgetThreshold>) {
    setF((cur) => ({ ...cur, thresholds: cur.thresholds.map((t, j) => (j === i ? { ...t, ...patch } : t)) }));
  }

  function submit(e: React.FormEvent) {
    e.preventDefault();
    const { draft, errors: errs } = buildDraft(f, { requirePrefix: !unmanagedEdit });
    setErrors(errs);
    if (draft) onSubmit(draft);
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate data-testid="budget-form" data-mode={mode} aria-label={TITLES[mode]}>
      <div className={styles.sectionHead}>
        <h3 style={{ margin: 0 }}>{TITLES[mode]}</h3>
        {mode === 'adopt' ? <span className={styles.hint}>Creates a cloud0-managed copy; the original budget stays untouched.</span> : null}
      </div>

      <fieldset className={styles.field}>
        <legend>Scope</legend>
        <div className={styles.options} role="radiogroup" aria-label="Budget scope">
          {SCOPES.map((s) => (
            <label key={s.id} className={styles.option}>
              <input type="radio" name="budget-scope" value={s.id} checked={f.scope === s.id} data-testid={`budget-scope-${s.id}`} onChange={() => change({ scope: s.id })} />
              {s.label}
            </label>
          ))}
        </div>
        {f.scope === 'services' ? (
          serviceSuggestions.length === 0 && f.serviceIds.length === 0 ? (
            <p className={styles.hint}>No services known yet: refresh a week so services can be listed.</p>
          ) : (
            <div className={styles.options} data-testid="budget-services">
              {[...new Set([...serviceSuggestions.map((s) => s.service_id), ...f.serviceIds])].map((id) => (
                <label key={id} className={styles.option}>
                  <input type="checkbox" checked={f.serviceIds.includes(id)} onChange={() => change({ serviceIds: toggle(f.serviceIds, id) })} />
                  {nameOf(id)}
                </label>
              ))}
            </div>
          )
        ) : null}
        {f.scope === 'projects' ? (
          <div className={styles.options} data-testid="budget-projects">
            {[...new Set([...projects, ...f.projects])].map((p) => (
              <label key={p} className={styles.option}>
                <input type="checkbox" checked={f.projects.includes(p)} onChange={() => change({ projects: toggle(f.projects, p) })} />
                {p}
              </label>
            ))}
          </div>
        ) : null}
        {errors.scope ? (
          <p className={styles.error} role="alert" data-testid="budget-error-scope">
            {errors.scope}
          </p>
        ) : null}
      </fieldset>

      <div className={styles.field}>
        <label className={styles.label} htmlFor="budget-name">
          Name
        </label>
        <input
          id="budget-name"
          className={styles.input}
          data-testid="budget-name-input"
          value={f.name}
          onChange={(e) => {
            setNameTouched(true);
            setF({ ...f, name: e.target.value });
          }}
        />
        {errors.name ? (
          <p className={styles.error} role="alert" data-testid="budget-error-name">
            {errors.name}
          </p>
        ) : null}
      </div>

      <div className={styles.field}>
        <label className={styles.label} htmlFor="budget-amount">
          Monthly amount ({currency})
        </label>
        <input
          id="budget-amount"
          className={`${styles.input} ${styles.amountInput}`}
          data-testid="budget-amount-input"
          inputMode="decimal"
          value={f.amount}
          onChange={(e) => {
            setAmountTouched(true);
            setF({ ...f, amount: e.target.value });
          }}
        />
        {suggested ? (
          <p className={styles.basis} data-testid="budget-basis">
            Suggested: {suggested.text}
            {Number(f.amount) !== suggested.amount ? (
              <>
                {' '}
                <button
                  type="button"
                  className={styles.link}
                  onClick={() => {
                    setAmountTouched(false);
                    setF({ ...f, amount: String(suggested.amount) });
                  }}
                >
                  Use suggestion
                </button>
              </>
            ) : null}
          </p>
        ) : (
          <p className={styles.hint} data-testid="budget-basis">
            No suggestion for this scope yet (needs stored weekly snapshots). Enter an amount.
          </p>
        )}
        {errors.amount ? (
          <p className={styles.error} role="alert" data-testid="budget-error-amount">
            {errors.amount}
          </p>
        ) : null}
      </div>

      <fieldset className={styles.field}>
        <legend>Thresholds</legend>
        {f.thresholds.map((t, i) => (
          <div key={i} className={styles.thresholdRow} data-testid="budget-threshold-row">
            <input
              className={`${styles.input} ${styles.num}`}
              type="number"
              min={1}
              step="any"
              aria-label={`Threshold ${i + 1} percent`}
              value={Number.isFinite(t.percent) ? t.percent : ''}
              onChange={(e) => setThreshold(i, { percent: e.target.value === '' ? Number.NaN : Number(e.target.value) })}
            />
            <span>%</span>
            <select className={styles.select} aria-label={`Threshold ${i + 1} basis`} value={t.basis} onChange={(e) => setThreshold(i, { basis: e.target.value as BudgetThreshold['basis'] })}>
              <option value="current_spend">of spend</option>
              <option value="forecasted_spend">of forecast</option>
            </select>
            <button type="button" className={styles.btn} aria-label={`Remove threshold ${formatThresholdLabel(t)}`} onClick={() => setF({ ...f, thresholds: f.thresholds.filter((_, j) => j !== i) })}>
              Remove
            </button>
          </div>
        ))}
        <div className={styles.actions}>
          <button type="button" className={styles.btn} data-testid="budget-threshold-add" onClick={() => setF({ ...f, thresholds: [...f.thresholds, { percent: 90, basis: 'current_spend' }] })}>
            Add threshold
          </button>
          <button type="button" className={styles.btn} onClick={() => setF({ ...f, thresholds: sortThresholds(defaultThresholds()) })}>
            Reset to 50 / 80 / 100 % + forecast
          </button>
        </div>
        {errors.thresholds ? (
          <p className={styles.error} role="alert" data-testid="budget-error-thresholds">
            {errors.thresholds}
          </p>
        ) : null}
      </fieldset>

      <fieldset className={styles.field}>
        <legend>Notifications</legend>
        <label className={styles.option}>
          Severity
          <select className={styles.select} data-testid="budget-severity" value={f.severity} onChange={(e) => setF({ ...f, severity: e.target.value as AlertSeverity })}>
            {SEVERITIES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>
        <label className={styles.option}>
          <input type="checkbox" checked={f.overrideChannels} data-testid="budget-override" onChange={(e) => setF({ ...f, overrideChannels: e.target.checked })} />
          Choose channels explicitly (otherwise follow the severity routing)
        </label>
        {f.overrideChannels ? (
          <div className={styles.options} data-testid="budget-channels-pick">
            {channels.length === 0 ? <span className={styles.hint}>No notification channels found.</span> : null}
            {channels.map((c) => (
              <label key={c.name} className={styles.option}>
                <input type="checkbox" checked={f.channels.includes(c.name)} onChange={() => setF({ ...f, channels: toggle(f.channels, c.name) })} />
                {c.display_name} <span className={styles.muted}>({c.project})</span>
              </label>
            ))}
          </div>
        ) : (
          <p className={styles.hint} data-testid="budget-routing">
            {severityRouting.length > 0
              ? `Following routing for ${f.severity}: ${severityRouting.map(channelLabel).join(', ')}.`
              : `Following routing for ${f.severity}; the server resolves the channels when you preview.`}{' '}
            Default billing-admin recipients stay on.
          </p>
        )}
        {errors.channels ? (
          <p className={styles.error} role="alert" data-testid="budget-error-channels">
            {errors.channels}
          </p>
        ) : null}
      </fieldset>

      <div className={styles.actions}>
        <button type="submit" className={styles.primary} data-testid="budget-submit" disabled={!canWrite} title={canWrite ? undefined : 'Set CLOUD0_ALERTS_CREDENTIALS and restart to enable changes — see README → Alerts setup.'}>
          Preview changes
        </button>
        <button type="button" className={styles.btn} data-testid="budget-cancel" onClick={onCancel}>
          Cancel
        </button>
        <span className={styles.hint}>Nothing changes in GCP until you apply the preview.</span>
      </div>
    </form>
  );
}
