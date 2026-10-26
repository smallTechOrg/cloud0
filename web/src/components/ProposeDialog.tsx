import { describeRule, PROPOSER_DISABLED_TEXT } from '@/lib/attributionFormat';
import type { ProposeResponse } from '@/lib/attributionTypes';
import { SourceBadge } from './RuleRow';
import styles from './AttributionPanel.module.css';

export type ProposeState =
  | { status: 'idle' }
  | { status: 'loading' }
  /** 503 proposer_not_configured (or rules.proposer.enabled === false). */
  | { status: 'disabled'; hint: string | null }
  | { status: 'error'; code: string; message: string; hint: string | null }
  | { status: 'ready'; data: ProposeResponse };

export interface ProposeDialogProps {
  state: ProposeState;
  /** From GET /attribution/rules: proposer.enabled; false disables the button up front. */
  configured: boolean;
  model: string | null;
  selected: ReadonlySet<number>;
  onPropose: () => void;
  onToggle: (index: number) => void;
  /** Adds the selected proposals to the editor as disabled drafts. */
  onAddToEditor: () => void;
  onDismiss: () => void;
}

export default function ProposeDialog({ state, configured, model, selected, onPropose, onToggle, onAddToEditor, onDismiss }: ProposeDialogProps) {
  const disabled = !configured || state.status === 'disabled';
  const hint = state.status === 'disabled' ? state.hint : null;
  return (
    <section className={styles.section} data-testid="propose-section" aria-labelledby="propose-h">
      <div className={styles.sectionHead}>
        <h3 id="propose-h">Suggest rules with AI</h3>
        <button
          type="button"
          className={styles.btn}
          data-testid="propose-btn"
          data-state={disabled ? 'disabled' : state.status}
          disabled={disabled || state.status === 'loading'}
          aria-describedby="propose-explain"
          onClick={onPropose}
        >
          {state.status === 'loading' ? 'Asking…' : 'Suggest rules with AI'}
        </button>
        <span id="propose-explain" className={styles.muted} data-testid="propose-explain">
          {disabled
            ? PROPOSER_DISABLED_TEXT
            : `Sends resource names, labels, project ids and services (no costs)${model ? ` to ${model}` : ''}. Proposals open as disabled drafts; nothing is applied automatically.`}
        </span>
      </div>
      {hint && !PROPOSER_DISABLED_TEXT.includes(hint) ? (
        <p className={styles.muted} data-testid="propose-hint">
          {hint}
        </p>
      ) : null}
      {state.status === 'error' ? (
        <p className={styles.err} role="alert" data-testid="propose-error">
          {state.code} — {state.message} {state.hint ?? ''} No rules were changed.
        </p>
      ) : null}
      {state.status === 'ready' ? (
        <div data-testid="propose-results">
          {state.data.proposals.length === 0 ? (
            <div className={styles.notice} data-testid="propose-none">
              The model proposed no rules{state.data.dropped > 0 ? ` (${state.data.dropped} dropped as invalid)` : ''}.
            </div>
          ) : (
            <>
              <p className={styles.small}>
                {state.data.proposals.length} proposal{state.data.proposals.length === 1 ? '' : 's'} from {state.data.model} · {state.data.latency_ms} ms · coverage if applied{' '}
                <strong>{state.data.coverage_if_applied.attributed_pct.toFixed(1)} %</strong>
                {state.data.dropped > 0 ? ` · ${state.data.dropped} dropped as invalid` : ''}
              </p>
              <div className={styles.scroller}>
                <table className={styles.table}>
                  <thead>
                    <tr>
                      <th scope="col">Add</th>
                      <th scope="col">Rule</th>
                      <th scope="col">Confidence</th>
                      <th scope="col">Covers</th>
                      <th scope="col">Rationale</th>
                    </tr>
                  </thead>
                  <tbody>
                    {state.data.proposals.map((p, i) => (
                      <tr key={i} data-testid={`proposal-${i}`}>
                        <td>
                          <input type="checkbox" aria-label={`Add proposal ${i + 1}`} data-testid={`proposal-check-${i}`} checked={selected.has(i)} onChange={() => onToggle(i)} />
                        </td>
                        <td>
                          <span className={styles.mono}>{describeRule(p.rule)}</span> <SourceBadge source="proposed" />
                        </td>
                        <td>{p.confidence}</td>
                        <td className={styles.num}>{p.covers}</td>
                        <td className={styles.small}>{p.rationale}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <div className={styles.toolbar}>
                <button type="button" className={styles.primary} data-testid="propose-add" disabled={selected.size === 0} onClick={onAddToEditor}>
                  Add {selected.size} to editor as disabled drafts
                </button>
                <button type="button" className={styles.btn} onClick={onDismiss}>
                  Dismiss
                </button>
              </div>
            </>
          )}
        </div>
      ) : null}
    </section>
  );
}
