import { coverageShift, describeRule, groupSeedByTier, parseSeedNote } from '@/lib/attributionFormat';
import type { SeedPreview as SeedPreviewData } from '@/lib/attributionTypes';
import { useCurrency } from '@/lib/currency';
import { formatMoney } from '@/lib/format';
import { SourceBadge } from './RuleRow';
import styles from './AttributionPanel.module.css';

export interface SeedPreviewProps {
  preview: SeedPreviewData | null;
  loading: boolean;
  applying: boolean;
  error: string | null;
  /** Indexes (into preview.proposed) that Apply would persist. */
  selected: ReadonlySet<number>;
  sharedTokens: string;
  /** Non-null → Apply is disabled and this explains why (e.g. unsaved edits). */
  applyBlocked: string | null;
  appliedMessage: string | null;
  onSharedTokens: (s: string) => void;
  onPreview: () => void;
  onToggle: (index: number) => void;
  onToggleAll: (all: boolean) => void;
  onApply: () => void;
}

export default function SeedPreview(p: SeedPreviewProps) {
  const currency = useCurrency();
  const pv = p.preview;
  const groups = pv ? groupSeedByTier(pv.proposed) : [];
  const allSelected = pv ? p.selected.size === pv.proposed.length : false;
  return (
    <section className={styles.section} data-testid="seed-section" aria-labelledby="seed-h">
      <div className={styles.sectionHead}>
        <h3 id="seed-h">Auto-seed</h3>
        <span className={styles.muted}>Derives rules from this week’s billing rows. Preview writes nothing.</span>
      </div>
      <div className={styles.toolbar}>
        <label>
          Shared resource-name tokens{' '}
          <input
            className={styles.input}
            style={{ width: '16rem' }}
            placeholder="comma-separated, optional"
            data-testid="seed-shared-tokens"
            value={p.sharedTokens}
            onChange={(e) => p.onSharedTokens(e.target.value)}
          />
        </label>
        <button type="button" className={styles.btn} data-testid="seed-preview-btn" disabled={p.loading} onClick={p.onPreview}>
          {p.loading ? 'Previewing…' : pv ? 'Re-preview' : 'Preview seed'}
        </button>
      </div>
      {p.error ? (
        <p className={styles.err} role="alert" data-testid="seed-error">
          {p.error}
        </p>
      ) : null}
      {p.appliedMessage ? (
        <div className={styles.okNotice} role="status" data-testid="seed-applied">
          {p.appliedMessage}
        </div>
      ) : null}
      {p.loading && !pv ? <div className={styles.skeleton} aria-busy="true" aria-label="loading seed preview" /> : null}
      {pv ? (
        <div data-testid="seed-preview">
          {pv.warnings.map((w) => (
            <div key={w} className={styles.notice} data-testid="seed-warning">
              {w}
            </div>
          ))}
          {pv.snapshot === null ? null : (
            <>
              <div className={styles.stats}>
                <div className={styles.stat}>
                  <span className={styles.statValue} data-testid="seed-coverage-shift">
                    {coverageShift(pv.coverage_before, pv.coverage)}
                  </span>
                  <span className={styles.statLabel}>attributed coverage, before → after</span>
                </div>
                <div className={styles.stat}>
                  <span className={styles.statValue}>{pv.counts.rules}</span>
                  <span className={styles.statLabel}>
                    rules ({pv.counts.products} products, {pv.counts.environments} environments)
                  </span>
                </div>
                <div className={styles.stat}>
                  <span className={styles.statValue}>{pv.counts.replaced_seed_rules}</span>
                  <span className={styles.statLabel}>seed rules replaced</span>
                </div>
                <div className={styles.stat}>
                  <span className={styles.statValue}>{pv.counts.kept_user_rules}</span>
                  <span className={styles.statLabel}>user rules kept</span>
                </div>
                <div className={styles.stat}>
                  <span className={styles.statValue}>{formatMoney(pv.coverage.unattributed_net, currency)}</span>
                  <span className={styles.statLabel}>unattributed after</span>
                </div>
              </div>
              {pv.products.length > 0 ? (
                <p className={styles.small} data-testid="seed-products">
                  Products found:{' '}
                  {pv.products.map((x) => (
                    <span key={x.product} className={styles.chip}>
                      {x.product} · {x.resources} · {formatMoney(x.net, currency)}
                    </span>
                  ))}
                </p>
              ) : null}
              <div className={styles.toolbar}>
                <label>
                  <input type="checkbox" data-testid="seed-select-all" checked={allSelected} onChange={(e) => p.onToggleAll(e.target.checked)} /> Select all ({p.selected.size} of {pv.proposed.length})
                </label>
                <span className={styles.spacer} />
                <button
                  type="button"
                  className={styles.primary}
                  data-testid="seed-apply"
                  disabled={p.applying || p.selected.size === 0 || p.applyBlocked !== null}
                  title={p.applyBlocked ?? 'Persists the selected rules; replaces stored seed rules, keeps your own'}
                  onClick={p.onApply}
                >
                  {p.applying ? 'Applying…' : `Apply seed (${p.selected.size})`}
                </button>
              </div>
              {p.applyBlocked ? (
                <p className={styles.muted} data-testid="seed-apply-blocked">
                  {p.applyBlocked}
                </p>
              ) : null}
              {groups.length === 0 ? (
                <div className={styles.notice} data-testid="seed-none">
                  The seeder found nothing to propose for this week.
                </div>
              ) : null}
              {groups.map((g) => (
                <div key={g.tier.id} data-testid={`seed-tier-${g.tier.id}`}>
                  <h4 className={styles.tierHead}>
                    {g.tier.title} <span className={styles.muted}>(priority {g.tier.range}) · {g.rules.length}</span>
                  </h4>
                  <div className={styles.scroller}>
                    <table className={styles.table}>
                      <thead>
                        <tr>
                          <th scope="col">Use</th>
                          <th scope="col">Priority</th>
                          <th scope="col">Rule</th>
                          <th scope="col" className={styles.num}>
                            Matched
                          </th>
                          <th scope="col" className={styles.num}>
                            Net matched
                          </th>
                          <th scope="col">Why</th>
                        </tr>
                      </thead>
                      <tbody>
                        {g.rules.map(({ rule, index }) => {
                          const note = parseSeedNote(rule.note);
                          return (
                            <tr key={index} data-testid={`seed-rule-${index}`}>
                              <td>
                                <input type="checkbox" aria-label={`Apply seed rule ${index + 1}`} data-testid={`seed-check-${index}`} checked={p.selected.has(index)} onChange={() => p.onToggle(index)} />
                              </td>
                              <td className={styles.num}>{rule.priority}</td>
                              <td>
                                <span className={styles.mono}>{describeRule(rule)}</span> <SourceBadge source={rule.source} />
                              </td>
                              <td className={styles.num}>{note ? note.resources : '—'}</td>
                              <td className={styles.num}>{note ? formatMoney(note.net, currency) : '—'}</td>
                              <td className={styles.small}>{rule.note ?? ''}</td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                </div>
              ))}
            </>
          )}
        </div>
      ) : null}
    </section>
  );
}
