import { formatTimestamp } from '@/lib/format';
import { prettyAggregation } from '@/lib/metricsFormat';
import type { Provenance } from '@/lib/types';
import styles from './MetricProvenance.module.css';

interface Props {
  title: string;
  provenance: Provenance;
  /** Gap text and hint, when the clicked cell is a gap. */
  gap?: { text: string; hint: string | null } | null;
  onClose: () => void;
}

/** Provenance inspector for a Metrics/Errors cell: filter, aggregation, window, fetched_at. */
export function MetricProvenance({ title, provenance, gap = null, onClose }: Props) {
  return (
    <aside className={styles.card} aria-label="Cell provenance" data-testid="metric-provenance">
      <header className={styles.header}>
        <strong data-testid="metric-provenance-title">{title}</strong>
        <button type="button" className={styles.close} onClick={onClose} aria-label="Close provenance" data-testid="metric-provenance-close">
          ×
        </button>
      </header>
      {gap ? (
        <p className={styles.gap} data-testid="metric-provenance-gap">
          <em>{gap.text}</em>
          {gap.hint ? <strong className={styles.hint}> — {gap.hint}</strong> : null}
        </p>
      ) : null}
      <dl className={styles.kv}>
        <dt>Source</dt>
        <dd>{provenance.source === '' ? 'none (not collected)' : provenance.source}</dd>
        <dt>Window</dt>
        <dd data-testid="metric-provenance-window">
          {formatTimestamp(provenance.window.start)} → {formatTimestamp(provenance.window.end)}
        </dd>
        <dt>Fetched</dt>
        <dd data-testid="metric-provenance-fetched">{formatTimestamp(provenance.fetched_at)}</dd>
      </dl>
      <h4 className={styles.sub}>Filter</h4>
      <pre className={styles.pre} data-testid="metric-provenance-filter">
        {provenance.query === '' ? '—' : provenance.query}
      </pre>
      <h4 className={styles.sub}>Aggregation</h4>
      <pre className={styles.pre} data-testid="metric-provenance-aggregation">
        {provenance.aggregation === '' ? '—' : prettyAggregation(provenance.aggregation)}
      </pre>
    </aside>
  );
}
