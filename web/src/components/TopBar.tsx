import type { AppConfig, Health, RefreshRequest, Scope, WeekID } from '@/lib/types';
import { RefreshButtons } from './RefreshButtons';
import { ScopeSelect } from './ScopeSelect';
import { WeekPicker, type WeekOption } from './WeekPicker';
import styles from './TopBar.module.css';

export type Connection = 'connecting' | 'connected' | 'error';

interface Props {
  config: AppConfig | null;
  health: Health | null;
  connection: Connection;
  weekOptions: WeekOption[];
  week: WeekID | null;
  onWeekChange: (week: WeekID) => void;
  scope: Scope;
  onScopeChange: (scope: Scope) => void;
  running: RefreshRequest | null;
  onRefreshWeek: () => void;
  onRefreshAll: () => void;
  /** "Run #8 ok · 2,345 rows · 41.9 MB billed · 8.1 s" or "Last run 09:05Z ok · …" */
  statusText: string;
}

export function TopBar(p: Props) {
  const ready = p.config !== null;
  return (
    <header className={styles.topbar}>
      <div className={styles.row}>
        <h1 className={styles.brand}>cloud0</h1>
        <WeekPicker options={p.weekOptions} value={p.week} onChange={p.onWeekChange} disabled={!ready} />
        <ScopeSelect projects={p.config?.projects ?? []} value={p.scope} onChange={p.onScopeChange} disabled={!ready} />
        <RefreshButtons
          week={p.week}
          running={p.running}
          disabled={!ready}
          onRefreshWeek={p.onRefreshWeek}
          onRefreshAll={p.onRefreshAll}
        />
        <div className={styles.status}>
          <span className={styles.runStatus} data-testid="refresh-status" role="status" aria-live="polite">
            {p.statusText}
          </span>
          <ConnectionStatus connection={p.connection} health={p.health} />
        </div>
      </div>
      {p.running ? (
        <div
          className={styles.progress}
          role="progressbar"
          aria-label={p.running.mode === 'week' ? `Refreshing ${p.running.week}` : 'Refreshing all weeks'}
          data-testid="refresh-progress"
        >
          <div className={styles.progressBar} />
        </div>
      ) : null}
    </header>
  );
}

function ConnectionStatus({ connection, health }: { connection: Connection; health: Health | null }) {
  if (connection === 'connecting') {
    return (
      <span className={`${styles.conn} ${styles.connConnecting}`} data-testid="api-status">
        Connecting to the cloud0 API…
      </span>
    );
  }
  if (connection === 'error' || health === null) {
    return (
      <span className={`${styles.conn} ${styles.connError}`} data-testid="api-status">
        Not connected — see the banner below.
      </span>
    );
  }
  const bq = health.bigquery;
  const bqText = bq === null ? 'bigquery not checked' : `bigquery ${bq.status}`;
  return (
    <span className={`${styles.conn} ${bq?.status === 'error' ? styles.connWarn : styles.connOk}`} data-testid="api-status">
      <span className={styles.dot} aria-hidden="true" /> connected · v{health.version} · {bqText}
    </span>
  );
}
