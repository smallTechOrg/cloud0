import { ALLOCATE_MODES, MAX_RULES, duplicateRule, moveRule, newRule, nextPriority, sortByPriority, validateRule, type AliasRow } from '@/lib/attributionFormat';
import type { AllocateMode, AttributionRule } from '@/lib/attributionTypes';
import RuleRow from './RuleRow';
import styles from './AttributionPanel.module.css';

/** A rule in the editor, with a client-only stable key for React. */
export interface EditorRule {
  key: string;
  rule: AttributionRule;
}

export interface RulesEditorProps {
  drafts: EditorRule[];
  allocate: AllocateMode;
  aliasRows: AliasRow[];
  aliasErrors: string[];
  /** invalid_rule messages from the last failed save, by 0-based row index. */
  serverErrors: Record<number, string>;
  dirty: boolean;
  saving: boolean;
  saveError: string | null;
  savedAt: string | null;
  newKey: () => string;
  onDrafts: (next: EditorRule[]) => void;
  onAllocate: (m: AllocateMode) => void;
  onAliasRows: (rows: AliasRow[]) => void;
  onSave: () => void;
  onDiscard: () => void;
}

const ALLOCATE_HELP: Record<AllocateMode, string> = {
  none: 'Shared and unattributed cost stay in their own rows.',
  even: 'Shared cost is split evenly across products.',
  proportional: 'Shared cost is split in proportion to each product’s direct cost.',
};

export default function RulesEditor(p: RulesEditorProps) {
  const invalidCount = p.drafts.filter((d) => validateRule(d.rule).length > 0).length;
  const canSave = p.dirty && !p.saving && invalidCount === 0 && p.aliasErrors.length === 0 && p.drafts.length <= MAX_RULES;

  const setRule = (i: number, rule: AttributionRule) => p.onDrafts(p.drafts.map((d, j) => (j === i ? { ...d, rule } : d)));
  const move = (i: number, dir: -1 | 1) => {
    const moved = moveRule(
      p.drafts.map((d) => ({ ...d.rule, __key: d.key })),
      i,
      dir,
    );
    p.onDrafts(moved.map(({ __key, ...rule }) => ({ key: __key, rule })));
  };
  const add = () => p.onDrafts([...p.drafts, { key: p.newKey(), rule: newRule(nextPriority(p.drafts.map((d) => d.rule))) }]);

  return (
    <section className={styles.section} data-testid="rules-editor" aria-labelledby="rules-editor-h">
      <div className={styles.sectionHead}>
        <h3 id="rules-editor-h">Rules</h3>
        <span className={styles.muted} data-testid="rules-count">
          {p.drafts.length} rule{p.drafts.length === 1 ? '' : 's'} · lower priority runs first · built-in <code>product</code>/<code>env</code> labels always win
        </span>
        <span className={styles.spacer} />
        {p.dirty ? (
          <span className={styles.badge} data-testid="rules-unsaved" role="status">
            Unsaved changes
          </span>
        ) : p.savedAt ? (
          <span className={styles.muted} data-testid="rules-saved">
            Saved
          </span>
        ) : null}
        <button type="button" className={styles.btn} data-testid="rule-add" onClick={add} disabled={p.drafts.length >= MAX_RULES}>
          Add rule
        </button>
        <button type="button" className={styles.btn} data-testid="rules-discard" disabled={!p.dirty || p.saving} onClick={p.onDiscard}>
          Discard changes
        </button>
        <button
          type="button"
          className={styles.primary}
          data-testid="rules-save"
          disabled={!canSave}
          title={invalidCount > 0 ? `${invalidCount} rule(s) need fixing` : !p.dirty ? 'No changes to save' : 'Replaces the whole rule list atomically'}
          onClick={p.onSave}
        >
          {p.saving ? 'Saving…' : 'Save rules'}
        </button>
      </div>

      {p.saveError ? (
        <p className={styles.err} role="alert" data-testid="rules-save-error">
          {p.saveError}
        </p>
      ) : null}
      {invalidCount > 0 ? (
        <p className={styles.err} data-testid="rules-invalid-count">
          {invalidCount} rule{invalidCount === 1 ? '' : 's'} need fixing before they can be saved.
        </p>
      ) : null}

      {p.drafts.length === 0 ? (
        <div className={styles.notice} data-testid="rules-empty">
          No rules yet. Use <strong>Auto-seed</strong> below to derive a starting set from this week’s billing rows, or <strong>Add rule</strong>.
        </div>
      ) : (
        <div className={styles.scroller}>
          <table className={styles.table} data-testid="rules-table">
            <caption className={styles.small}>Evaluation order is top to bottom. Product and environment resolve independently: the first matching rule that sets each wins.</caption>
            <thead>
              <tr>
                <th scope="col">On</th>
                <th scope="col">Priority / order</th>
                <th scope="col">Match</th>
                <th scope="col">Action</th>
                <th scope="col">Source</th>
                <th scope="col">Note</th>
                <th scope="col">
                  Actions
                </th>
              </tr>
            </thead>
            <tbody>
              {p.drafts.map((d, i) => (
                <RuleRow
                  key={d.key}
                  index={i}
                  rule={d.rule}
                  isFirst={i === 0}
                  isLast={i === p.drafts.length - 1}
                  serverError={p.serverErrors[i] ?? null}
                  onChange={(r) => setRule(i, r)}
                  onMove={(dir) => move(i, dir)}
                  onDuplicate={() => p.onDrafts([...p.drafts.slice(0, i + 1), { key: p.newKey(), rule: duplicateRule(d.rule) }, ...p.drafts.slice(i + 1)])}
                  onDelete={() => p.onDrafts(p.drafts.filter((_, j) => j !== i))}
                  onCommitPriority={() => {
                    const sorted = sortByPriority(p.drafts.map((x) => ({ ...x.rule, __key: x.key })));
                    const next = sorted.map(({ __key, ...rule }) => ({ key: __key, rule }));
                    if (next.some((n, j) => n.key !== p.drafts[j]?.key)) p.onDrafts(next);
                  }}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className={styles.toolbar} data-testid="allocate-mode">
        <label>
          Allocate shared cost:{' '}
          <select className={styles.select} data-testid="allocate-select" value={p.allocate} onChange={(e) => p.onAllocate(e.target.value as AllocateMode)}>
            {ALLOCATE_MODES.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </label>
        <span className={styles.muted} data-testid="allocate-help">
          {ALLOCATE_HELP[p.allocate]}
        </span>
      </div>

      <div data-testid="aliases-editor">
        <strong>Aliases</strong> <span className={styles.muted}>— label/name token → product, used by the seeder</span>
        {p.aliasRows.length === 0 ? (
          <p className={styles.muted} data-testid="aliases-empty">
            No aliases.
          </p>
        ) : null}
        {p.aliasRows.map((r, i) => (
          <div key={i} className={styles.aliasRow}>
            <input
              className={styles.input}
              placeholder="alias"
              aria-label={`Alias ${i + 1} key`}
              data-testid={`alias-key-${i}`}
              value={r.key}
              onChange={(e) => p.onAliasRows(p.aliasRows.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))}
            />
            <span aria-hidden="true">→</span>
            <input
              className={styles.input}
              placeholder="product"
              aria-label={`Alias ${i + 1} product`}
              data-testid={`alias-product-${i}`}
              value={r.product}
              onChange={(e) => p.onAliasRows(p.aliasRows.map((x, j) => (j === i ? { ...x, product: e.target.value } : x)))}
            />
            <button type="button" className={styles.danger} aria-label={`Remove alias ${i + 1}`} onClick={() => p.onAliasRows(p.aliasRows.filter((_, j) => j !== i))}>
              ×
            </button>
          </div>
        ))}
        {p.aliasErrors.map((m) => (
          <p key={m} className={styles.err} data-testid="alias-error">
            {m}
          </p>
        ))}
        <button type="button" className={styles.btn} data-testid="alias-add" onClick={() => p.onAliasRows([...p.aliasRows, { key: '', product: '' }])}>
          Add alias
        </button>
      </div>
    </section>
  );
}
