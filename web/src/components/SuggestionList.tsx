import { basisText } from '@/lib/budgetsFormat';
import { formatMoney } from '@/lib/format';
import type { BudgetSuggestion } from '@/lib/types';
import styles from './BudgetsPanel.module.css';

export interface SuggestionListProps {
  suggestions: BudgetSuggestion[];
  currency: string;
  canWrite: boolean;
  onCreate: (s: BudgetSuggestion) => void;
}

/** Per-scope suggested monthly budgets with a Create shortcut that prefills the form. */
export function SuggestionList({ suggestions, currency, canWrite, onCreate }: SuggestionListProps) {
  if (suggestions.length === 0) {
    return (
      <p className={styles.hint} data-testid="suggestions-empty">
        No suggestions yet: suggestions come from stored weekly snapshots, so refresh a week first.
      </p>
    );
  }
  return (
    <div className={styles.scroller}>
      <table className={styles.table} data-testid="suggestions-table">
        <caption className={styles.hint} style={{ textAlign: 'left', padding: '0.4rem 0.7rem' }}>
          Suggested monthly budgets from stored weekly snapshots (net of credits).
        </caption>
        <thead>
          <tr>
            <th scope="col">Scope</th>
            <th scope="col" className={styles.numCell}>
              Suggested
            </th>
            <th scope="col">Basis</th>
            <th scope="col" />
          </tr>
        </thead>
        <tbody>
          {suggestions.map((s) => (
            <tr key={`${s.scope}-${s.service_id}`} data-testid="suggestion-row" data-scope={s.scope}>
              <th scope="row">{s.service_name}</th>
              <td className={styles.numCell} data-testid="suggestion-amount">
                {s.basis.method === 'none' ? '—' : formatMoney(s.amount, currency)}
              </td>
              <td data-testid="suggestion-basis">{basisText(s.basis, s.amount, currency)}</td>
              <td>
                {s.exists ? (
                  <span className={styles.muted} data-testid="suggestion-exists">
                    Budget exists
                  </span>
                ) : (
                  <button
                    type="button"
                    className={styles.btn}
                    data-testid="suggestion-create"
                    disabled={!canWrite}
                    title={canWrite ? undefined : 'Set CLOUD0_ALERTS_CREDENTIALS and restart to enable changes — see README → Alerts setup.'}
                    onClick={() => onCreate(s)}
                  >
                    Create
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
