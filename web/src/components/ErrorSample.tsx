import type { ApiError } from '@/lib/api';
import { formatCount } from '@/lib/metricsFormat';
import { formatEntryTime, formatShare, sampleIsEmpty, splitNotFoundPaths } from '@/lib/errorsFormat';
import type { ErrorSamplesResponse, SampleFocus } from '@/lib/types';
import styles from './ErrorsTriage.module.css';

/** On-demand log sample of one Cloud Run row and focus. */
export type SampleState = { status: 'loading' } | { status: 'error'; error: ApiError } | { status: 'done'; data: ErrorSamplesResponse };

/** Sample states keyed by `sampleKey(rowIndex, focus)`. */
export type SampleMap = Readonly<Record<string, SampleState>>;

export function sampleKey(index: number, focus: SampleFocus): string {
  return `${index}:${focus}`;
}

interface Props {
  focus: SampleFocus;
  state: SampleState | undefined;
  /** Message shown when the sample loaded but nothing matched. */
  emptyText: string;
  onRetry?: () => void;
}

function PathsTable({ s, caption, focus }: { s: ErrorSamplesResponse; caption: string; focus: SampleFocus }) {
  if (s.top_paths.length === 0) return null;
  return (
    <table className={styles.sampleTable} data-testid="errors-sample-paths">
      <caption className={styles.caption}>{caption}</caption>
      <thead>
        <tr>
          <th scope="col">Path</th>
          <th scope="col">Status</th>
          <th scope="col" className={styles.numCol}>Count</th>
          <th scope="col">Kind</th>
        </tr>
      </thead>
      <tbody>
        {s.top_paths.map((p, i) => (
          <tr key={`${p.path}-${p.status}-${i}`} data-testid="errors-sample-path-row" data-scanner={p.scanner_like ? 'true' : 'false'}>
            <td className={styles.mono}>{p.path}</td>
            <td>{p.status}</td>
            <td className={styles.numCol}>{formatCount(p.count)}</td>
            <td>{p.scanner_like ? <span className={styles.scannerTag}>scanner-like</span> : focus === 'notfound' ? <span className={styles.brokenTag}>possible broken link</span> : null}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function EntriesTable({ s }: { s: ErrorSamplesResponse }) {
  if (s.entries.length === 0) return null;
  return (
    <table className={styles.sampleTable} data-testid="errors-sample-entries">
      <caption className={styles.caption}>Most recent entries (newest first)</caption>
      <thead>
        <tr>
          <th scope="col">Time (UTC)</th>
          <th scope="col">Method</th>
          <th scope="col">Path</th>
          <th scope="col">Status</th>
          <th scope="col" className={styles.numCol}>Latency</th>
          <th scope="col">Trace</th>
        </tr>
      </thead>
      <tbody>
        {s.entries.map((e, i) => (
          <tr key={i} data-testid="errors-sample-entry-row">
            <td className={styles.nowrap}>{formatEntryTime(e.timestamp)}</td>
            <td>{e.method}</td>
            <td className={styles.mono}>{e.path}</td>
            <td>{e.status}</td>
            <td className={styles.numCol}>{e.latency_ms.toLocaleString('en-US', { maximumFractionDigits: 0 })} ms</td>
            <td className={`${styles.mono} ${styles.trace}`} title={e.trace}>{e.trace === '' ? '-' : e.trace}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function NotFoundSummary({ s }: { s: ErrorSamplesResponse }) {
  const share = formatShare(s.scanner_share_4xx);
  const { possibleBroken } = splitNotFoundPaths(s.top_paths);
  return (
    <div data-testid="errors-notfound-summary">
      {share !== null ? (
        <p className={styles.shareLine} data-testid="errors-sample-share">
          <strong>{share}</strong> of the sampled 4xx responses look like scanner traffic.
        </p>
      ) : null}
      {possibleBroken.length > 0 ? (
        <div className={styles.broken} data-testid="errors-broken-links">
          <strong>Possible broken links</strong> (404 paths that do not look like scanner probes; check whether these are real pages):
          <ul className={styles.brokenList}>
            {possibleBroken.map((p, i) => (
              <li key={`${p.path}-${i}`} className={styles.mono} data-testid="errors-broken-link">
                {p.path} <span className={styles.muted}>x{formatCount(p.count)}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <p className={styles.okLine} data-testid="errors-no-broken-links">
          Every sampled 404 path looks like scanner traffic, so nothing here points to a broken link.
        </p>
      )}
    </div>
  );
}

/** Loading / error / out-of-retention / empty / result for one on-demand sample. Never a bare error. */
export function SampleBlock({ focus, state, emptyText, onRetry }: Props) {
  if (!state) return null;
  if (state.status === 'loading') {
    return (
      <div className={styles.loading} aria-busy="true" aria-label="loading log sample" data-testid="errors-sample-loading">
        <span className={styles.skeleton} />
        <span className={styles.skeleton} />
      </div>
    );
  }
  if (state.status === 'error') {
    return (
      <div className={styles.sampleError} role="alert" data-testid="errors-sample-error">
        <strong>{state.error.message}</strong>
        {state.error.hint ? <span> — {state.error.hint}</span> : null}
        {onRetry ? (
          <>
            {' '}
            <button type="button" className={styles.linkBtn} onClick={onRetry}>
              Try again
            </button>
          </>
        ) : null}
      </div>
    );
  }
  const s = state.data;
  if (s.out_of_retention) {
    return (
      <p className={styles.sampleNote} data-testid="errors-sample-retention">
        Out of retention: {s.retention_note || 'this week is older than Cloud Logging keeps logs (30 days), so requests cannot be listed.'}
      </p>
    );
  }
  if (sampleIsEmpty(s)) {
    return (
      <p className={styles.sampleNote} data-testid="errors-sample-empty">
        {emptyText}
        {s.scanned === 0 && s.retention_note ? ` ${s.retention_note}` : ''}
      </p>
    );
  }
  return (
    <div data-testid="errors-sample-result" data-focus={focus}>
      <p className={styles.sampleNote}>
        Scanned {formatCount(s.scanned)} log entries; showing the most recent entries. {s.note}
        {s.unparsed ? ` ${formatCount(s.unparsed)} entries could not be parsed into requests.` : ''}
      </p>
      {focus === 'notfound' ? <NotFoundSummary s={s} /> : null}
      <PathsTable s={s} caption="Top paths" focus={focus} />
      <EntriesTable s={s} />
      {s.heuristic_note ? <p className={styles.sampleNote}>{s.heuristic_note}</p> : null}
    </div>
  );
}
