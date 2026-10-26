import { useCallback, useEffect, useRef, useState } from 'react';
import { getAlertHistory, getIncidents, getSnoozes, toApiError } from '@/lib/api';
import { downloadTextFile } from '@/lib/csv';
import { formatTimestamp } from '@/lib/format';
import { buildHistoryCsv, buildReviewCsv, buildReviewMarkdown, historyCsvFilename, relativeTime, snoozeRequestForIncident, endSnoozeRequest } from '@/lib/incidentsFormat';
import type { AlertHistoryResponse, ApplyResult, Incident, IncidentsResponse, PreviewRequest, ProjectFailure, Snooze, SnoozesResponse, WeekID } from '@/lib/types';
import { EmptyState } from './EmptyState';
import { ErrorBanner, bannerFromError, type BannerData } from './ErrorBanner';
import { HistoryTable } from './HistoryTable';
import { IncidentRow } from './IncidentRow';
import PreviewApplyDrawer from './PreviewApplyDrawer';
import { SnoozeList } from './SnoozeList';
import styles from './IncidentsPanel.module.css';

export const AUTO_REFRESH_MS = 60_000;

/** One section's load state. */
export interface Load<T> {
  data: T | null;
  loading: boolean;
  banner: BannerData | null;
}

function Failures({ items }: { items: ProjectFailure[] }) {
  if (items.length === 0) return null;
  return (
    <ul className={styles.failures} data-testid="projects-failed">
      {items.map((f) => (
        <li key={f.project}>
          <strong>{f.project}</strong>: <code>{f.code}</code> — {f.message}
          {f.hint ? <div className={styles.hint}>{f.hint}</div> : null}
        </li>
      ))}
    </ul>
  );
}

function Skeleton({ label }: { label: string }) {
  return <div className={styles.skeleton} aria-busy="true" aria-label={label} data-testid="loading" />;
}

export interface ActiveCardProps {
  load: Load<IncidentsResponse>;
  now: Date;
  onRefresh: () => void;
  onSnooze: (incident: Incident, seconds: number) => void;
  onDismiss?: () => void;
}

/** "Active now": currently open incidents or the green "Nothing firing" state. */
export function ActiveCard({ load, now, onRefresh, onSnooze, onDismiss }: ActiveCardProps) {
  const { data, loading, banner } = load;
  return (
    <section className={styles.card} data-testid="incidents-active" aria-labelledby="inc-active-h">
      <div className={styles.cardHead}>
        <h2 className={styles.title} id="inc-active-h">
          Active now
        </h2>
        <span className={styles.muted} data-testid="incidents-checked">
          {data ? `Last checked ${formatTimestamp(data.generated_at)} (${relativeTime(data.generated_at, now)}) · auto-refreshes every 60 s while visible` : 'Not checked yet'}
        </span>
        <span className={styles.spacer} />
        <button type="button" className={styles.btn} data-testid="incidents-refresh" disabled={loading} onClick={onRefresh}>
          {loading ? 'Refreshing…' : 'Refresh'}
        </button>
      </div>
      {banner ? <ErrorBanner banner={banner} onDismiss={onDismiss} /> : null}
      {data === null ? (
        loading ? (
          <Skeleton label="loading incidents" />
        ) : banner ? null : (
          <EmptyState title="Incidents unavailable" testId="incidents-unavailable">
            The active incidents could not be loaded.
          </EmptyState>
        )
      ) : (
        <>
          <Failures items={data.projects_failed} />
          {data.incidents.length === 0 ? (
            <div className={styles.nothing} role="status" data-testid="incidents-none">
              <span aria-hidden="true">✓</span> Nothing firing
              <span className={styles.muted}>— no open alerts as of {formatTimestamp(data.generated_at)}</span>
            </div>
          ) : (
            <ul className={styles.list} data-testid="incident-list">
              {data.incidents.map((i) => (
                <IncidentRow key={`${i.project}/${i.id}`} incident={i} now={now} onSnooze={onSnooze} />
              ))}
            </ul>
          )}
        </>
      )}
    </section>
  );
}

export interface HistoryCardProps {
  week: WeekID;
  load: Load<AlertHistoryResponse>;
  copied?: string | null;
  onCopy: (format: 'markdown' | 'csv') => void;
  onExport: () => void;
  onDismiss?: () => void;
}

/** "This week": per-policy alert history with copy/export actions. */
export function HistoryCard({ week, load, copied = null, onCopy, onExport, onDismiss }: HistoryCardProps) {
  const { data, loading, banner } = load;
  const has = data !== null && data.rows.length > 0;
  return (
    <section className={styles.card} data-testid="incidents-history" aria-labelledby="inc-history-h">
      <div className={styles.cardHead}>
        <h2 className={styles.title} id="inc-history-h">
          This week <span className={styles.muted}>({week})</span>
        </h2>
        <span className={styles.spacer} />
        {copied ? (
          <span className={styles.copied} role="status" data-testid="history-copied">
            {copied}
          </span>
        ) : null}
        <button type="button" className={styles.btn} data-testid="history-copy" disabled={!has} onClick={() => onCopy('markdown')}>
          Copy for weekly review
        </button>
        <button type="button" className={styles.btn} data-testid="history-copy-csv" disabled={!has} onClick={() => onCopy('csv')}>
          Copy as CSV
        </button>
        <button type="button" className={styles.btn} data-testid="history-export" disabled={!has} onClick={onExport}>
          Export CSV
        </button>
      </div>
      {banner ? <ErrorBanner banner={banner} onDismiss={onDismiss} /> : null}
      {data === null ? (
        loading ? (
          <Skeleton label="loading alert history" />
        ) : banner ? null : (
          <EmptyState title="History unavailable" testId="history-unavailable">
            The alert history could not be loaded.
          </EmptyState>
        )
      ) : (
        <>
          <Failures items={data.projects_failed} />
          <HistoryTable data={data} />
        </>
      )}
    </section>
  );
}

export interface SnoozesCardProps {
  load: Load<SnoozesResponse>;
  now: Date;
  onEnd: (s: Snooze) => void;
  onDismiss?: () => void;
}

export function SnoozesCard({ load, now, onEnd, onDismiss }: SnoozesCardProps) {
  const { data, loading, banner } = load;
  return (
    <section className={styles.card} data-testid="incidents-snoozes" aria-labelledby="inc-snooze-h">
      <div className={styles.cardHead}>
        <h2 className={styles.title} id="inc-snooze-h">
          Snoozes
        </h2>
        <span className={styles.muted}>Muted policies do not notify while a snooze is active.</span>
      </div>
      {banner ? <ErrorBanner banner={banner} onDismiss={onDismiss} /> : null}
      {data === null ? loading ? <Skeleton label="loading snoozes" /> : null : <SnoozeList snoozes={data.snoozes} now={now} onEnd={onEnd} />}
    </section>
  );
}

const EMPTY: Load<never> = { data: null, loading: true, banner: null };

interface Props {
  week: WeekID;
  refreshToken: number;
}

export default function IncidentsPanel({ week, refreshToken }: Props) {
  const [active, setActive] = useState<Load<IncidentsResponse>>(EMPTY);
  const [history, setHistory] = useState<Load<AlertHistoryResponse>>(EMPTY);
  const [snoozes, setSnoozes] = useState<Load<SnoozesResponse>>(EMPTY);
  const [now, setNow] = useState(() => new Date());
  const [request, setRequest] = useState<PreviewRequest | null>(null);
  const [copied, setCopied] = useState<string | null>(null);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const loadActive = useCallback(() => {
    setActive((s) => ({ ...s, loading: true }));
    getIncidents('open')
      .then((data) => alive.current && (setActive({ data, loading: false, banner: null }), setNow(new Date())))
      .catch((err: unknown) => alive.current && setActive((s) => ({ ...s, loading: false, banner: bannerFromError('incidents', 'Incidents', toApiError(err)) })));
  }, []);

  const loadSnoozes = useCallback(() => {
    setSnoozes((s) => ({ ...s, loading: true }));
    getSnoozes()
      .then((data) => alive.current && setSnoozes({ data, loading: false, banner: null }))
      .catch((err: unknown) => alive.current && setSnoozes((s) => ({ ...s, loading: false, banner: bannerFromError('snoozes', 'Snoozes', toApiError(err)) })));
  }, []);

  const loadHistory = useCallback(() => {
    setHistory((s) => ({ ...s, loading: true }));
    getAlertHistory(week)
      .then((data) => alive.current && setHistory({ data, loading: false, banner: null }))
      .catch((err: unknown) => alive.current && setHistory((s) => ({ ...s, loading: false, banner: bannerFromError('alert-history', 'Alert history', toApiError(err)) })));
  }, [week]);

  useEffect(() => {
    loadActive();
    loadSnoozes();
  }, [loadActive, loadSnoozes, refreshToken]);

  useEffect(() => {
    loadHistory();
  }, [loadHistory, refreshToken]);

  // Auto-refresh every 60 s, only while the tab is visible; catch up on return.
  useEffect(() => {
    const tick = () => {
      if (document.visibilityState === 'visible') {
        loadActive();
        loadSnoozes();
      }
    };
    const id = window.setInterval(tick, AUTO_REFRESH_MS);
    const onVis = () => {
      if (document.visibilityState === 'visible') tick();
    };
    document.addEventListener('visibilitychange', onVis);
    return () => {
      window.clearInterval(id);
      document.removeEventListener('visibilitychange', onVis);
    };
  }, [loadActive, loadSnoozes]);

  const onApplied = useCallback(
    (_r: ApplyResult) => {
      setRequest(null);
      loadActive();
      loadSnoozes();
    },
    [loadActive, loadSnoozes],
  );

  async function copy(format: 'markdown' | 'csv') {
    if (!history.data) return;
    const text = format === 'markdown' ? buildReviewMarkdown(history.data) : buildReviewCsv(history.data);
    try {
      await navigator.clipboard.writeText(text);
      setCopied(`Copied ${history.data.rows.length} line(s) as ${format === 'markdown' ? 'markdown' : 'CSV'}`);
    } catch {
      setCopied('Copy blocked by the browser — use Export CSV instead');
    }
    window.setTimeout(() => alive.current && setCopied(null), 4000);
  }

  return (
    <div className={styles.panel} data-testid="incidents-panel">
      <ActiveCard
        load={active}
        now={now}
        onRefresh={loadActive}
        onSnooze={(i, seconds) => setRequest(snoozeRequestForIncident(i, seconds))}
        onDismiss={() => setActive((s) => ({ ...s, banner: null }))}
      />
      <HistoryCard
        week={week}
        load={history}
        copied={copied}
        onCopy={(f) => void copy(f)}
        onExport={() => history.data && downloadTextFile(historyCsvFilename(history.data.week), buildHistoryCsv(history.data))}
        onDismiss={() => setHistory((s) => ({ ...s, banner: null }))}
      />
      <SnoozesCard load={snoozes} now={now} onEnd={(s) => setRequest(endSnoozeRequest(s))} onDismiss={() => setSnoozes((s) => ({ ...s, banner: null }))} />
      <PreviewApplyDrawer request={request} onClose={() => setRequest(null)} onApplied={onApplied} />
    </div>
  );
}
