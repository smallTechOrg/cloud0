import { reasonLabel } from '@/lib/attributionFormat';
import type { UnattributedItem, UnattributedResponse } from '@/lib/attributionTypes';
import { useCurrency } from '@/lib/currency';
import { formatMoney } from '@/lib/format';
import styles from './AttributionPanel.module.css';

export interface UnattributedListProps {
  data: UnattributedResponse | null;
  loading: boolean;
  error: string | null;
  onCreateRule: (item: UnattributedItem) => void;
  onExplain: (item: UnattributedItem) => void;
  onExport: () => void;
}

export default function UnattributedList({ data, loading, error, onCreateRule, onExplain, onExport }: UnattributedListProps) {
  const currency = useCurrency();
  return (
    <section className={styles.section} data-testid="unattributed-section" aria-labelledby="unattr-h">
      <div className={styles.sectionHead}>
        <h3 id="unattr-h">Unattributed</h3>
        {data && data.snapshot ? (
          <span className={styles.muted} data-testid="unattributed-summary">
            {data.item_count} item{data.item_count === 1 ? '' : 's'} · {formatMoney(data.unattributed_net, currency)} ({data.unattributed_pct.toFixed(1)} % of net) · sorted by net
          </span>
        ) : null}
        <span className={styles.spacer} />
        <button type="button" className={styles.btn} data-testid="unattributed-export" disabled={!data || data.items.length === 0} onClick={onExport}>
          Export CSV
        </button>
      </div>
      {error ? (
        <p className={styles.err} role="alert" data-testid="unattributed-error">
          {error}
        </p>
      ) : null}
      {data === null ? (
        loading ? (
          <div className={styles.skeleton} aria-busy="true" aria-label="loading unattributed" />
        ) : (
          <div className={styles.notice} data-testid="unattributed-unavailable">
            The unattributed list could not be loaded. Check the error above, then switch week or refresh.
          </div>
        )
      ) : data.snapshot === null ? (
        <div className={styles.notice} data-testid="unattributed-nosnapshot">
          No snapshot for {data.week}. Refresh the week to fetch billing rows first.
        </div>
      ) : data.items.length === 0 ? (
        <div className={styles.okNotice} data-testid="unattributed-empty">
          Everything in {data.week} is attributed or marked shared.
        </div>
      ) : (
        <>
          {!data.labels_captured ? (
            <div className={styles.notice} data-testid="unattributed-nolabels">
              Labels were not captured for this week; refresh it to enable label rules.
            </div>
          ) : null}
          <div className={styles.scroller}>
            <table className={styles.table} data-testid="unattributed-table">
              <thead>
                <tr>
                  <th scope="col">Resource / SKU</th>
                  <th scope="col">Project · service</th>
                  <th scope="col" className={styles.num}>
                    Net
                  </th>
                  <th scope="col" className={styles.num}>
                    Share
                  </th>
                  <th scope="col">Why</th>
                  <th scope="col">Suggested</th>
                  <th scope="col">Actions</th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((it, i) => (
                  <tr key={`${it.kind}:${it.project_id}:${it.key}:${it.service}`} data-testid={`unattributed-row-${i}`} data-kind={it.kind}>
                    <td>
                      <div className={styles.mono}>{it.key}</div>
                      <div className={styles.muted + ' ' + styles.small}>{it.kind === 'sku' ? 'SKU (no resource name)' : 'resource'}</div>
                    </td>
                    <td>
                      {it.project_id} · {it.service}
                    </td>
                    <td className={styles.num}>{formatMoney(it.net, currency)}</td>
                    <td className={styles.num}>{it.share_pct.toFixed(2)} %</td>
                    <td>
                      <div>{reasonLabel(it.reason)}</div>
                      {it.evidence.map((e) => (
                        <div key={e} className={`${styles.muted} ${styles.small}`}>
                          {e}
                        </div>
                      ))}
                    </td>
                    <td data-testid={`unattributed-suggested-${i}`}>
                      {it.suggested ? (
                        <>
                          <span className={`${styles.chip} ${styles[`conf_${it.suggested.confidence}`] ?? ''}`} title={`${it.suggested.confidence} confidence — ${it.suggested.basis}`}>
                            product: {it.suggested.product}
                          </span>
                          {it.suggested.environment ? <span className={styles.chip}>env: {it.suggested.environment}</span> : null}
                          <div className={`${styles.muted} ${styles.small}`}>
                            {it.suggested.confidence} · {it.suggested.basis}
                          </div>
                        </>
                      ) : (
                        <span className={styles.muted}>no suggestion</span>
                      )}
                    </td>
                    <td>
                      <div className={styles.rowActions}>
                        <button type="button" className={styles.btn} data-testid={`unattributed-create-${i}`} onClick={() => onCreateRule(it)}>
                          Create rule from this
                        </button>
                        {it.kind === 'resource' ? (
                          <button type="button" className={styles.btn} data-testid={`unattributed-explain-${i}`} onClick={() => onExplain(it)}>
                            Explain
                          </button>
                        ) : null}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {data.truncated ? (
            <p className={styles.muted} data-testid="unattributed-truncated">
              List truncated to the largest {data.items.length}; the smaller items together make up the rest of the unattributed net.
            </p>
          ) : null}
        </>
      )}
    </section>
  );
}
