import { MATCH_FIELDS, MATCH_OPS, MAX_REGEX_LEN, actionKind, validateRule, type ActionKind } from '@/lib/attributionFormat';
import type { AttributionRule, MatchField, MatchOp } from '@/lib/attributionTypes';
import styles from './AttributionPanel.module.css';

export interface RuleRowProps {
  index: number;
  rule: AttributionRule;
  isFirst: boolean;
  isLast: boolean;
  /** Error returned by the server for this row (invalid_rule), shown inline. */
  serverError?: string | null;
  onChange: (rule: AttributionRule) => void;
  onMove: (dir: -1 | 1) => void;
  onDuplicate: () => void;
  onDelete: () => void;
  /** Priority input lost focus: parent re-sorts. */
  onCommitPriority: () => void;
}

export function SourceBadge({ source }: { source: AttributionRule['source'] }) {
  return (
    <span className={`${styles.badge} ${styles[`badge_${source}`] ?? ''}`} data-testid="rule-source" data-source={source}>
      {source}
    </span>
  );
}

export default function RuleRow({ index, rule, isFirst, isLast, serverError = null, onChange, onMove, onDuplicate, onDelete, onCommitPriority }: RuleRowProps) {
  const errors = validateRule(rule);
  const kind = actionKind(rule);
  const regexLen = rule.match.op === 'regex' ? rule.match.value.length : null;
  const n = index + 1;

  function setMatch(patch: Partial<AttributionRule['match']>) {
    onChange({ ...rule, match: { ...rule.match, ...patch } });
  }

  function setKind(k: ActionKind) {
    const env = rule.action.environment;
    if (k === 'shared') onChange({ ...rule, action: { shared: true, ...(env ? { environment: env } : {}) } });
    else if (k === 'environment') onChange({ ...rule, action: { environment: env ?? '' } });
    else onChange({ ...rule, action: { product: rule.action.product ?? '', ...(env ? { environment: env } : {}) } });
  }

  const hasError = errors.length > 0 || !!serverError;
  return (
    <>
      <tr
        className={`${rule.enabled ? '' : styles.disabledRow} ${serverError ? styles.errorRow : ''}`}
        data-testid={`rule-row-${index}`}
        data-enabled={rule.enabled}
        data-source={rule.source}
      >
        <td>
          <input type="checkbox" aria-label={`Enable rule ${n}`} data-testid={`rule-enabled-${index}`} checked={rule.enabled} onChange={(e) => onChange({ ...rule, enabled: e.target.checked })} />
        </td>
        <td>
          <div className={styles.rowActions}>
            <input
              className={`${styles.input} ${styles.narrow}`}
              type="number"
              min={0}
              step={1}
              aria-label={`Priority of rule ${n}`}
              data-testid={`rule-priority-${index}`}
              value={Number.isFinite(rule.priority) ? rule.priority : ''}
              onChange={(e) => onChange({ ...rule, priority: e.target.value === '' ? Number.NaN : Number(e.target.value) })}
              onBlur={onCommitPriority}
            />
            <button type="button" className={styles.btn} aria-label={`Move rule ${n} up`} data-testid={`rule-up-${index}`} disabled={isFirst} onClick={() => onMove(-1)}>
              ↑
            </button>
            <button type="button" className={styles.btn} aria-label={`Move rule ${n} down`} data-testid={`rule-down-${index}`} disabled={isLast} onClick={() => onMove(1)}>
              ↓
            </button>
          </div>
        </td>
        <td>
          <div className={styles.fieldGrid}>
            <select className={styles.select} aria-label={`Match field of rule ${n}`} data-testid={`rule-field-${index}`} value={rule.match.field} onChange={(e) => setMatch({ field: e.target.value as MatchField })}>
              {MATCH_FIELDS.map((f) => (
                <option key={f} value={f}>
                  {f.replace('_', ' ')}
                </option>
              ))}
            </select>
            {rule.match.field === 'label' ? (
              <input
                className={`${styles.input} ${!rule.match.key?.trim() ? styles.invalid : ''}`}
                placeholder="label key"
                aria-label={`Label key of rule ${n}`}
                data-testid={`rule-key-${index}`}
                value={rule.match.key ?? ''}
                onChange={(e) => setMatch({ key: e.target.value })}
              />
            ) : null}
            <select className={styles.select} aria-label={`Match operator of rule ${n}`} data-testid={`rule-op-${index}`} value={rule.match.op} onChange={(e) => setMatch({ op: e.target.value as MatchOp })}>
              {MATCH_OPS.map((o) => (
                <option key={o} value={o}>
                  {o}
                </option>
              ))}
            </select>
            <input
              className={`${styles.input} ${rule.match.value.trim() === '' ? styles.invalid : ''}`}
              placeholder="value"
              aria-label={`Match value of rule ${n}`}
              data-testid={`rule-value-${index}`}
              value={rule.match.value}
              onChange={(e) => setMatch({ value: e.target.value })}
            />
          </div>
          {regexLen !== null ? (
            <div className={`${styles.small} ${regexLen > MAX_REGEX_LEN ? styles.err : styles.muted}`} data-testid={`rule-regexlen-${index}`}>
              {regexLen} / {MAX_REGEX_LEN} characters · case-insensitive, unanchored
            </div>
          ) : null}
        </td>
        <td>
          <div className={styles.fieldGrid}>
            <select className={styles.select} aria-label={`Action of rule ${n}`} data-testid={`rule-action-${index}`} value={kind} onChange={(e) => setKind(e.target.value as ActionKind)}>
              <option value="product">set product</option>
              <option value="environment">set environment only</option>
              <option value="shared">mark shared</option>
            </select>
            {kind === 'product' ? (
              <input
                className={styles.input}
                placeholder="product"
                aria-label={`Product of rule ${n}`}
                data-testid={`rule-product-${index}`}
                value={rule.action.product ?? ''}
                onChange={(e) => onChange({ ...rule, action: { ...rule.action, product: e.target.value } })}
              />
            ) : null}
            {kind !== 'environment' ? null : (
              <input
                className={styles.input}
                placeholder="environment"
                aria-label={`Environment of rule ${n}`}
                data-testid={`rule-env-${index}`}
                value={rule.action.environment ?? ''}
                onChange={(e) => onChange({ ...rule, action: { environment: e.target.value } })}
              />
            )}
            {kind !== 'environment' ? (
              <input
                className={styles.input}
                placeholder="environment (optional)"
                aria-label={`Optional environment of rule ${n}`}
                data-testid={`rule-env-${index}`}
                value={rule.action.environment ?? ''}
                onChange={(e) => {
                  const environment = e.target.value;
                  const { environment: _drop, ...rest } = rule.action;
                  void _drop;
                  onChange({ ...rule, action: environment === '' ? rest : { ...rest, environment } });
                }}
              />
            ) : null}
          </div>
        </td>
        <td>
          <SourceBadge source={rule.source} />
        </td>
        <td>
          <input
            className={styles.input}
            placeholder="note"
            aria-label={`Note of rule ${n}`}
            value={rule.note ?? ''}
            onChange={(e) => onChange({ ...rule, note: e.target.value === '' ? null : e.target.value })}
          />
        </td>
        <td>
          <div className={styles.rowActions}>
            <button type="button" className={styles.btn} data-testid={`rule-dup-${index}`} aria-label={`Duplicate rule ${n}`} onClick={onDuplicate}>
              Duplicate
            </button>
            <button type="button" className={styles.danger} data-testid={`rule-del-${index}`} aria-label={`Delete rule ${n}`} onClick={onDelete}>
              Delete
            </button>
          </div>
        </td>
      </tr>
      {hasError ? (
        <tr className={styles.errorRow} data-testid={`rule-errors-${index}`}>
          <td />
          <td colSpan={6}>
            {serverError ? (
              <p className={styles.err} role="alert" data-testid={`rule-server-error-${index}`}>
                invalid_rule — {serverError}
              </p>
            ) : null}
            {errors.map((m) => (
              <p key={m} className={styles.err} data-testid={`rule-validation-${index}`}>
                {m}
              </p>
            ))}
          </td>
        </tr>
      ) : null}
    </>
  );
}
