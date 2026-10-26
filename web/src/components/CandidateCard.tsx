import type { RightsizingCandidate, RightsizingResponse } from '@/lib/attributionTypes';
import { formatMoney } from '@/lib/format';
import { CONFIDENCE_LABELS, evidenceLines, kindLabel, sparkThreshold, windowText } from '@/lib/rightsizingFormat';
import { EvidenceSpark } from './EvidenceSpark';
import styles from './RightsizingPanel.module.css';

interface Props {
  candidate: RightsizingCandidate;
  thresholds: RightsizingResponse['thresholds'];
  currency: string;
}

export function CandidateCard({ candidate: c, thresholds, currency }: Props) {
  const saving = c.estimated_monthly_saving;
  return (
    <article className={styles.card} data-testid="rightsizing-card" data-kind={c.kind} data-project={c.project_id}>
      <div className={styles.cardHead}>
        <h4 className={styles.resource} data-testid="candidate-resource">
          {c.resource_name}
        </h4>
        <span className={`${styles.badge} ${styles[`kind_${c.kind}`] ?? ''}`} data-testid="candidate-kind">
          {kindLabel(c.kind)}
        </span>
        <span className={`${styles.badge} ${styles[`conf_${c.confidence}`] ?? ''}`} data-testid="candidate-confidence">
          {CONFIDENCE_LABELS[c.confidence]}
        </span>
        <span className={styles.spacer} />
        <span className={styles.saving} data-testid="candidate-saving">
          {saving === null ? (
            <span className={styles.muted}>saving unknown</span>
          ) : (
            <>
              ≈ {formatMoney(saving, currency)}
              <span className={styles.perMonth}> / month</span>
            </>
          )}
        </span>
      </div>
      <div className={styles.meta}>
        <span data-testid="candidate-project">{c.project_id}</span>
        {c.location ? <span> · {c.location}</span> : null}
        {c.net_last_week !== null ? <span> · last week {formatMoney(c.net_last_week, currency)}</span> : null}
      </div>
      <p className={styles.action} data-testid="candidate-action">
        {c.action}
      </p>
      <div className={styles.evidenceRow}>
        <EvidenceSpark candidate={c} threshold={sparkThreshold(c, thresholds)} />
        <span className={styles.window} data-testid="candidate-window">
          {windowText(c)}
        </span>
      </div>
      <details className={styles.details}>
        <summary data-testid="candidate-evidence-toggle">Evidence</summary>
        <dl className={styles.evidenceList} data-testid="candidate-evidence">
          {evidenceLines(c, currency).map((l, i) => (
            <div key={`${l.label}-${i}`} className={styles.evidenceItem}>
              <dt>{l.label}</dt>
              <dd>{l.value}</dd>
            </div>
          ))}
        </dl>
        {c.evidence.notes.length > 0 ? (
          <ul className={styles.notes}>
            {c.evidence.notes.map((n) => (
              <li key={n}>{n}</li>
            ))}
          </ul>
        ) : null}
      </details>
    </article>
  );
}
