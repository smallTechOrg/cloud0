import { useState } from 'react';
import { formatBudgetAmount, formatThresholdLabel, scopeLabel, sortThresholds, spendProgress } from '@/lib/budgetsFormat';
import { formatMoney, formatPct } from '@/lib/format';
import type { BudgetView } from '@/lib/types';
import styles from './BudgetsPanel.module.css';

export interface BudgetCardProps {
  budget: BudgetView;
  /** service id -> display name (from the suggestions). */
  serviceNames?: Readonly<Record<string, string>>;
  /** channel resource name -> display name. */
  channelNames?: Readonly<Record<string, string>>;
  currency: string;
  /** Month-to-date net for this budget's scope; null/undefined = not available. */
  spent?: number | null;
  /** Month id of `spent`, shown next to the bar. */
  month?: string;
  /** False when CLOUD0_ALERTS_CREDENTIALS is unset: write actions are disabled with the setup hint. */
  canWrite: boolean;
  onAdopt: (b: BudgetView) => void;
  onEdit: (b: BudgetView) => void;
  onDelete: (b: BudgetView) => void;
  /** Initial state of the inline delete confirmation (tests render it statically). */
  initialConfirmDelete?: boolean;
}

const WRITE_HINT = 'Set CLOUD0_ALERTS_CREDENTIALS and restart to enable changes — see README → Alerts setup.';

export function BudgetCard({
  budget: b,
  serviceNames = {},
  channelNames = {},
  currency,
  spent = null,
  month,
  canWrite,
  onAdopt,
  onEdit,
  onDelete,
  initialConfirmDelete = false,
}: BudgetCardProps) {
  const [confirming, setConfirming] = useState(initialConfirmDelete);
  const progress = spent !== null && b.amount_kind === 'specified' ? spendProgress(spent, b.amount) : null;
  const title = canWrite ? undefined : WRITE_HINT;
  return (
    <article className={styles.card} data-testid="budget-card" data-managed={b.managed} aria-label={b.display_name}>
      <div className={styles.cardHead}>
        <h4 className={styles.cardName} data-testid="budget-name">
          {b.display_name}
        </h4>
        <span className={`${styles.badge} ${b.managed ? styles.managed : styles.unmanaged}`} data-testid="budget-badge">
          {b.managed ? 'Managed' : 'Unmanaged'}
        </span>
        <span className={styles.amount} data-testid="budget-amount">
          {formatBudgetAmount({ ...b, currency })}
          {b.amount_kind === 'specified' ? <span className={styles.muted}> / month</span> : null}
        </span>
      </div>

      <dl className={styles.facts}>
        <dt>Scope</dt>
        <dd data-testid="budget-scope">{scopeLabel(b, serviceNames)}</dd>
        <dt>Thresholds</dt>
        <dd data-testid="budget-thresholds">
          {sortThresholds(b.thresholds).map((t) => (
            <span key={`${t.percent}-${t.basis}`} className={`${styles.chip} ${t.basis === 'forecasted_spend' ? styles.chipForecast : ''}`}>
              {formatThresholdLabel(t)}
            </span>
          ))}
        </dd>
        <dt>Notifies</dt>
        <dd data-testid="budget-channels">
          {b.channel_names.length > 0
            ? b.channel_names.map((n) => channelNames[n] ?? n.split('/').pop() ?? n).join(', ')
            : 'No monitoring channels'}
          {b.default_iam_recipients ? ' · billing admins (default recipients)' : ''}
        </dd>
      </dl>

      {progress ? (
        <div className={styles.barWrap} title={`Month-to-date net ${formatMoney(spent ?? 0, currency)} of ${formatMoney(b.amount ?? 0, currency)}`}>
          <span className={styles.track}>
            <span
              className={`${styles.fill} ${progress.tone === 'ok' ? '' : styles[`fill_${progress.tone}`] ?? ''}`}
              style={{ width: `${progress.width}%` }}
              data-testid="budget-bar"
              data-tone={progress.tone}
            />
          </span>
          <span className={styles.pct} data-testid="budget-pct">
            {formatPct(progress.pct)}
          </span>
          <span className={styles.muted}>
            {formatMoney(spent ?? 0, currency)} spent{month ? ` in ${month}` : ''}
          </span>
        </div>
      ) : (
        <p className={styles.hint} data-testid="budget-no-progress">
          Month-to-date progress is shown for account-wide and single-project budgets with data in the Month tab.
        </p>
      )}

      {b.warnings.map((w) => (
        <p key={w} className={styles.warning} data-testid="budget-warning">
          {w}
        </p>
      ))}

      {confirming ? (
        <div className={styles.confirm} role="alertdialog" aria-label={`Confirm delete ${b.display_name}`} data-testid="budget-delete-confirm">
          <span>
            Delete <strong>{b.display_name}</strong>?{b.managed ? '' : ' This budget is not managed by cloud0.'} A delete preview opens; nothing is removed until you apply it.
          </span>
          <button
            type="button"
            className={styles.danger}
            data-testid="budget-delete-confirm-yes"
            onClick={() => {
              setConfirming(false);
              onDelete(b);
            }}
          >
            Preview delete
          </button>
          <button type="button" className={styles.btn} onClick={() => setConfirming(false)}>
            Cancel
          </button>
        </div>
      ) : (
        <div className={styles.actions}>
          {!b.managed && b.adopt_shape === 'budget_account' ? (
            <button type="button" className={styles.primary} data-testid="budget-adopt" disabled={!canWrite} title={title} onClick={() => onAdopt(b)}>
              Adopt as managed copy
            </button>
          ) : null}
          <button type="button" className={styles.btn} data-testid="budget-edit" disabled={!canWrite} title={title} onClick={() => onEdit(b)}>
            Edit
          </button>
          <button type="button" className={styles.danger} data-testid="budget-delete" disabled={!canWrite} title={title} onClick={() => setConfirming(true)}>
            Delete
          </button>
          {!b.managed ? <span className={styles.hint}>Adopting creates a cloud0-managed copy; the original is never deleted automatically.</span> : null}
        </div>
      )}
    </article>
  );
}
