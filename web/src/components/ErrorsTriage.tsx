import type { ReactNode } from 'react';
import { FOCUS_SECTIONS, VERDICT_LABELS, changeText, errorCodeChips, failingFocus, triageGroups, verdictBadge, type IndexedRow } from '@/lib/errorsFormat';
import { formatCount } from '@/lib/metricsFormat';
import type { ErrorsSummary, ErrorTableRow, SampleFocus } from '@/lib/types';
import { SampleBlock, sampleKey, type SampleMap } from './ErrorSample';
import styles from './ErrorsTriage.module.css';

export interface TriageProps {
  rows: readonly ErrorTableRow[];
  summary: ErrorsSummary;
  /** Row indexes whose Details are open. */
  expanded?: ReadonlySet<number>;
  onToggleExpand?: (index: number) => void;
  samples?: SampleMap;
  onSample?: (index: number, focus: SampleFocus) => void;
}

export function VerdictBadge({ verdict }: { verdict: NonNullable<ErrorTableRow['verdict']> }) {
  const b = verdictBadge(verdict);
  return (
    <span className={`${styles.badge} ${styles[`badge_${b.tone}`] ?? ''}`} data-testid="verdict-badge" data-verdict={verdict}>
      {b.label}
    </span>
  );
}

function rowName(r: ErrorTableRow): string {
  return r.service === '' ? r.project : r.service;
}

function Codes({ row }: { row: ErrorTableRow }) {
  if (row.by_code === null) {
    return (
      <p className={styles.muted} data-testid="errors-codes-missing">
        Exact status codes are not stored for this batch: refresh the week for exact codes.
      </p>
    );
  }
  const chips = errorCodeChips(row.by_code);
  if (chips.length === 0) return <p className={styles.muted}>No 4xx/5xx responses this week.</p>;
  return (
    <div className={styles.chips} data-testid="errors-codes">
      {chips.map((c) => (
        <span key={c.code} className={`${styles.chip} ${c.kind === '5xx' ? styles.chip5 : styles.chip4}`} data-testid="errors-code-chip" data-kind={c.kind}>
          {c.code} ×{formatCount(c.count)}
        </span>
      ))}
    </div>
  );
}

interface ItemProps {
  item: IndexedRow;
  samples?: SampleMap;
  expanded: boolean;
  onToggleExpand?: (index: number) => void;
  onSample?: (index: number, focus: SampleFocus) => void;
}

function DetailSections({ index, samples, onSample }: { index: number; samples?: SampleMap; onSample?: (index: number, focus: SampleFocus) => void }) {
  return (
    <div className={styles.details} data-testid="errors-detail">
      {FOCUS_SECTIONS.map((sec) => {
        const state = samples?.[sampleKey(index, sec.focus)];
        return (
          <section key={sec.focus} className={styles.detailSection} data-testid="errors-detail-section" data-focus={sec.focus}>
            <h5 className={styles.detailTitle}>{sec.title}</h5>
            {state ? (
              <SampleBlock focus={sec.focus} state={state} emptyText={sec.empty} onRetry={onSample ? () => onSample(index, sec.focus) : undefined} />
            ) : (
              <p className={styles.muted}>
                Not loaded yet.{' '}
                {onSample ? (
                  <button type="button" className={styles.linkBtn} onClick={() => onSample(index, sec.focus)}>
                    Load
                  </button>
                ) : null}
              </p>
            )}
          </section>
        );
      })}
    </div>
  );
}

function TriageItem({ item, samples, expanded, onToggleExpand, onSample }: ItemProps) {
  const { row, index } = item;
  const verdict = row.verdict;
  if (!verdict) return null;
  const focus = failingFocus(row);
  const inline = samples?.[sampleKey(index, focus)];
  const change = changeText(row);
  const healthy = verdict === 'healthy';
  const loading = inline?.status === 'loading';
  const sampleLabel = focus === 'notfound' ? 'Check paths' : 'Show failing requests';
  const sec = FOCUS_SECTIONS.find((s) => s.focus === focus);
  return (
    <li className={`${styles.item} ${styles[`item_${verdictBadge(verdict).tone}`] ?? ''}`} data-testid="triage-item" data-verdict={verdict} data-service={row.service}>
      <div className={styles.itemHead}>
        <VerdictBadge verdict={verdict} />
        <strong className={styles.name}>{rowName(row)}</strong>
        <span className={styles.project}>{row.service === '' ? '' : row.project}</span>
        {change ? (
          <span className={styles.change} data-testid="triage-change">
            {change}
          </span>
        ) : null}
      </div>
      {row.reasons && row.reasons.length > 0 ? (
        <ul className={styles.reasons} data-testid="triage-reasons">
          {row.reasons.map((r, i) => (
            <li key={i}>{r}</li>
          ))}
        </ul>
      ) : null}
      {healthy ? null : <Codes row={row} />}
      <div className={styles.actions}>
        {healthy ? null : (
          <button type="button" className={styles.primary} disabled={loading || !onSample} onClick={() => onSample?.(index, focus)} data-testid="triage-sample-button" data-focus={focus}>
            {loading ? 'Loading…' : sampleLabel}
          </button>
        )}
        {row.logs_url ? (
          <a href={row.logs_url} target="_blank" rel="noopener noreferrer" className={styles.logsLink} data-testid="errors-logs-link">
            Open in Logs Explorer
          </a>
        ) : null}
        <button type="button" className={styles.secondary} aria-expanded={expanded} disabled={!onToggleExpand} onClick={() => onToggleExpand?.(index)} data-testid="triage-details-button">
          {expanded ? 'Hide details' : 'Details'}
        </button>
      </div>
      {!expanded && inline ? (
        <div className={styles.inline} data-testid="triage-sample">
          <SampleBlock focus={focus} state={inline} emptyText={sec?.empty ?? 'Nothing matched in the sampled logs.'} onRetry={onSample ? () => onSample(index, focus) : undefined} />
        </div>
      ) : null}
      {expanded ? (
        <DetailSections index={index} samples={samples} onSample={onSample} />
      ) : null}
    </li>
  );
}

function ItemList({ items, ...rest }: { items: IndexedRow[] } & Omit<ItemProps, 'item' | 'expanded'> & { expanded?: ReadonlySet<number> }): ReactNode {
  return (
    <ol className={styles.list}>
      {items.map((item) => (
        <TriageItem key={item.index} item={item} samples={rest.samples} expanded={rest.expanded?.has(item.index) ?? false} onToggleExpand={rest.onToggleExpand} onSample={rest.onSample} />
      ))}
    </ol>
  );
}

/** "What to look at": headline, ordered needs-attention then watch rows, and collapsed noise / healthy. */
export function TriageView({ rows, summary, expanded, onToggleExpand, samples, onSample }: TriageProps) {
  const g = triageGroups(rows);
  const todo = [...g.attention, ...g.watch];
  const shared = { samples, expanded, onToggleExpand, onSample };
  return (
    <div className={styles.triage} data-testid="errors-triage">
      <section className={styles.card} aria-labelledby="errors-look-at" data-testid="errors-look-at">
        <h3 id="errors-look-at" className={styles.cardTitle}>
          What to look at
        </h3>
        {summary.headline ? (
          <p className={styles.headline} data-testid="errors-headline">
            <strong>{summary.headline}</strong>
          </p>
        ) : null}
        {todo.length === 0 ? (
          <p className={styles.allClear} role="status" data-testid="errors-all-clear">
            Nothing needs attention this week
          </p>
        ) : (
          <ItemList items={todo} {...shared} />
        )}
      </section>

      {g.noise.length > 0 ? (
        <details className={styles.fold} data-testid="errors-noise">
          <summary className={styles.foldSummary}>
            {VERDICT_LABELS.noise} ({g.noise.length})
          </summary>
          <p className={styles.noiseLine}>No action needed unless these paths are real pages</p>
          <ItemList items={g.noise} {...shared} />
        </details>
      ) : null}

      {g.healthy.length > 0 ? (
        <details className={styles.fold} data-testid="errors-healthy">
          <summary className={styles.foldSummary}>
            {VERDICT_LABELS.healthy} ({g.healthy.length})
          </summary>
          <ItemList items={g.healthy} {...shared} />
        </details>
      ) : null}
    </div>
  );
}
