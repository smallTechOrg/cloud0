import { formatLabels, labelProgress, resourceTypeLabel } from '@/lib/attributionFormat';
import type { LabelSuggestionsResponse } from '@/lib/attributionTypes';
import { useCurrency } from '@/lib/currency';
import { formatMoney } from '@/lib/format';
import styles from './AttributionPanel.module.css';

export interface LabelSuggestionsProps {
  data: LabelSuggestionsResponse | null;
  loading: boolean;
  error: string | null;
  /** Key of the command most recently copied (shows "Copied"). */
  copiedKey: string | null;
  onCopy: (key: string, command: string) => void;
  onExport: () => void;
}

export default function LabelSuggestions({ data, loading, error, copiedKey, onCopy, onExport }: LabelSuggestionsProps) {
  const currency = useCurrency();
  const progress = data ? labelProgress(data.done_count, data.count) : null;
  return (
    <section className={styles.section} data-testid="labels-section" aria-labelledby="labels-h">
      <div className={styles.sectionHead}>
        <h3 id="labels-h">Label suggestions</h3>
        <span className={styles.muted}>Add product/env labels in GCP so attribution no longer depends on name rules. cloud0 never runs these commands.</span>
        <span className={styles.spacer} />
        <button type="button" className={styles.btn} data-testid="labels-export" disabled={!data || data.items.length === 0} onClick={onExport}>
          Export CSV
        </button>
      </div>
      {error ? (
        <p className={styles.err} role="alert" data-testid="labels-error">
          {error}
        </p>
      ) : null}
      {data === null ? (
        loading ? (
          <div className={styles.skeleton} aria-busy="true" aria-label="loading label suggestions" />
        ) : (
          <div className={styles.notice} data-testid="labels-unavailable">
            Label suggestions could not be loaded. Check the error above.
          </div>
        )
      ) : data.snapshot === null ? (
        <div className={styles.notice} data-testid="labels-nosnapshot">
          No snapshot for {data.week}. Refresh the week first.
        </div>
      ) : (
        <>
          {progress ? (
            <div data-testid="labels-progress">
              <strong data-testid="labels-progress-text">{progress.text}</strong>
              <div className={styles.progressTrack} role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress.pct} aria-label="labelling progress">
                <div className={styles.progressFill} style={{ width: `${progress.pct}%` }} />
              </div>
            </div>
          ) : null}
          {!data.labels_captured ? (
            <div className={styles.notice} data-testid="labels-nolabels">
              Labels were not captured for this week; refresh it so already-labelled resources are recognised.
            </div>
          ) : null}
          {data.items.length === 0 ? (
            <div className={styles.okNotice} data-testid="labels-empty">
              No resources need new labels this week.
            </div>
          ) : (
            <div className={styles.scroller}>
              <table className={styles.table} data-testid="labels-table">
                <thead>
                  <tr>
                    <th scope="col">Resource</th>
                    <th scope="col">Type</th>
                    <th scope="col" className={styles.num}>
                      Net
                    </th>
                    <th scope="col">Labels to add</th>
                    <th scope="col">Risk</th>
                    <th scope="col">Command</th>
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((it, i) => {
                    const key = `${it.project_id}/${it.resource_name}`;
                    return (
                      <tr key={key} data-testid={`label-row-${i}`}>
                        <td>
                          <div className={styles.mono}>{it.resource_name}</div>
                          <div className={`${styles.muted} ${styles.small}`}>
                            {it.project_id}
                            {it.region_or_zone ? ` · ${it.region_or_zone}` : ''}
                          </div>
                        </td>
                        <td>{resourceTypeLabel(it.resource_type)}</td>
                        <td className={styles.num}>{formatMoney(it.net, currency)}</td>
                        <td data-testid={`label-labels-${i}`}>
                          {Object.entries(it.labels).length === 0 ? <span className={styles.muted}>—</span> : formatLabels(it.labels)}
                        </td>
                        <td className={styles.small} data-testid={`label-risk-${i}`}>
                          {it.risk}
                        </td>
                        <td>
                          {it.command ? (
                            <div className={styles.command}>
                              <code data-testid={`label-command-${i}`}>{it.command}</code>
                              <button type="button" className={styles.btn} data-testid={`label-copy-${i}`} onClick={() => onCopy(key, it.command ?? '')}>
                                {copiedKey === key ? 'Copied' : 'Copy'}
                              </button>
                            </div>
                          ) : (
                            <span className={styles.muted} data-testid={`label-nocommand-${i}`}>
                              No command for this resource type — label it in the console.
                            </span>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </section>
  );
}
