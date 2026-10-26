import type { ExplainResponse } from '@/lib/attributionTypes';
import { useCurrency } from '@/lib/currency';
import { formatMoney } from '@/lib/format';
import styles from './AttributionPanel.module.css';

export interface ExplainState {
  loading: boolean;
  error: string | null;
  data: ExplainResponse | null;
}

export interface ExplainDrawerProps {
  query: string;
  state: ExplainState;
  /** Whether the section is expanded (always rendered so a resource can be typed). */
  onQuery: (q: string) => void;
  onSubmit: () => void;
  onClose: () => void;
}

export default function ExplainDrawer({ query, state, onQuery, onSubmit, onClose }: ExplainDrawerProps) {
  const currency = useCurrency();
  return (
    <section className={state.data ? styles.drawer : styles.section} data-testid="explain-drawer" aria-labelledby="explain-h">
      <div className={styles.sectionHead}>
        <h3 id="explain-h">Explain a resource</h3>
        <form
          className={styles.toolbar}
          onSubmit={(e) => {
            e.preventDefault();
            onSubmit();
          }}
        >
          <input
            className={styles.input}
            style={{ width: '18rem' }}
            placeholder="resource name or global name"
            aria-label="Resource to explain"
            data-testid="explain-input"
            value={query}
            onChange={(e) => onQuery(e.target.value)}
          />
          <button type="submit" className={styles.btn} data-testid="explain-btn" disabled={state.loading || query.trim() === ''}>
            {state.loading ? 'Explaining…' : 'Explain'}
          </button>
        </form>
        <span className={styles.spacer} />
        {state.data || state.error ? (
          <button type="button" className={styles.btn} data-testid="explain-close" onClick={onClose}>
            Close
          </button>
        ) : null}
      </div>
      {state.error ? (
        <p className={styles.err} role="alert" data-testid="explain-error">
          {state.error}
        </p>
      ) : null}
      {state.data ? (
        <div data-testid="explain-results">
          <p className={styles.small}>
            “{state.data.resource}” matched {state.data.matches} billed resource{state.data.matches === 1 ? '' : 's'} in {state.data.week}.
          </p>
          {state.data.results.map((r, ri) => (
            <div key={`${r.project_id}:${r.service}:${ri}`} data-testid={`explain-result-${ri}`}>
              <h4 className={styles.tierHead}>
                {r.resource_name ?? r.resource_global_name} <span className={styles.muted}>· {r.project_id} · {r.service} · {formatMoney(r.net, currency)}</span>
              </h4>
              <p className={styles.small} data-testid={`explain-outcome-${ri}`}>
                Result:{' '}
                {r.evaluation.shared ? <span className={styles.chip}>shared</span> : <span className={styles.chip}>product: {r.evaluation.product || '(unattributed)'}</span>}
                <span className={styles.chip}>env: {r.evaluation.environment || '(no environment)'}</span>
                {Object.keys(r.labels).length > 0 ? (
                  <span className={styles.muted}>
                    {' '}
                    labels: {Object.entries(r.labels)
                      .map(([k, v]) => `${k}=${v}`)
                      .join(', ')}
                  </span>
                ) : null}
              </p>
              <ol className={styles.trail} data-testid={`explain-trail-${ri}`}>
                {r.trail.map((s, si) => (
                  <li
                    key={si}
                    className={`${styles.trailStep} ${s.matched ? styles.trailMatched : ''} ${s.applied ? styles.trailApplied : ''}`}
                    data-testid={`trail-step-${ri}-${si}`}
                    data-matched={s.matched}
                    data-applied={s.applied ?? 'none'}
                  >
                    <strong>{s.builtin ? 'built-in' : `rule ${s.rule_id}`}</strong>
                    {s.priority !== undefined ? <span className={styles.muted}> · priority {s.priority}</span> : null} — {s.description}
                    <div className={styles.small}>
                      <span className={styles.chip}>{s.matched ? 'matched' : 'no match'}</span>
                      {s.applied ? <span className={styles.chip}>applied: {s.applied}</span> : s.matched ? <span className={styles.chip}>already decided</span> : null}
                      <span className={styles.muted}>{s.reason}</span>
                    </div>
                  </li>
                ))}
              </ol>
            </div>
          ))}
        </div>
      ) : null}
    </section>
  );
}
