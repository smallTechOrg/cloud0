'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import { AnnotationField } from '@/components/AnnotationField';
import { CellInspector, type InspectorTarget } from '@/components/CellInspector';
import { CostTable } from '@/components/CostTable';
import { EmptyState } from '@/components/EmptyState';
import { ErrorBanner, bannerFromError, type BannerData } from '@/components/ErrorBanner';
import { NavTabs } from '@/components/NavTabs';
import { RunLogDrawer } from '@/components/RunLogDrawer';
import { scopeLabel } from '@/components/ScopeSelect';
import { StubPanel } from '@/components/StubPanel';
import { ThresholdsEditor } from '@/components/ThresholdsEditor';
import { TopBar, type Connection } from '@/components/TopBar';
import type { WeekOption } from '@/components/WeekPicker';
import {
  ApiError,
  getAnnotation,
  getConfig,
  getCostTable,
  getHealth,
  getRuns,
  getSettings,
  postRefresh,
  putAnnotation,
  putSettings,
  toApiError,
} from '@/lib/api';
import { buildCsv, csvFilename, downloadTextFile } from '@/lib/csv';
import { FALLBACK_COLUMNS } from '@/lib/fallbackColumns';
import { formatBytes, formatDurationMs, formatInteger, formatTimeZ } from '@/lib/format';
import { TABS, tabById, tabFromHash, type TabId } from '@/lib/tabs';
import {
  SCOPE_ALL,
  type Annotation,
  type AppConfig,
  type Column,
  type CostRow,
  type CostTableResponse,
  type Health,
  type RefreshRequest,
  type RefreshResponse,
  type Run,
  type RunListItem,
  type Scope,
  type Settings,
  type WeekID,
} from '@/lib/types';
import { statusText, weekBounds, weekOptionLabel, weekRange, weekStatus } from '@/lib/week';
import styles from './page.module.css';

const RUNS_LIMIT = 50;

/** Rows shown when GET /cost/table is unavailable: every week, "not fetched". */
function fallbackRows(config: AppConfig): CostRow[] {
  const now = new Date(config.now);
  return weekRange(config.first_week, config.current_week).map((week) => {
    const b = weekBounds(week);
    return {
      week,
      week_start: b.start,
      week_end: b.end,
      week_status: weekStatus(week, now, config.settle_hours),
      snapshot: null,
      cells: null,
      pct_change: null,
      flagged: false,
      annotation: null,
    };
  });
}

function sumBytesBilled(run: Run): number | null {
  let total: number | null = null;
  for (const s of run.sources) if (s.bytes_billed !== null) total = (total ?? 0) + s.bytes_billed;
  return total;
}

function sumRows(run: Run): number | null {
  let total: number | null = null;
  for (const s of run.sources) if (s.rows_returned !== null) total = (total ?? 0) + s.rows_returned;
  return total;
}

export default function Page() {
  // ---- navigation ----------------------------------------------------------
  const [tab, setTab] = useState<TabId>('cost');
  useEffect(() => {
    const apply = () => setTab(tabFromHash(window.location.hash));
    apply();
    window.addEventListener('hashchange', apply);
    return () => window.removeEventListener('hashchange', apply);
  }, []);

  // ---- server connection ---------------------------------------------------
  const [config, setConfig] = useState<AppConfig | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [connection, setConnection] = useState<Connection>('connecting');
  const [banners, setBanners] = useState<BannerData[]>([]);

  const showBanner = useCallback((banner: BannerData) => {
    setBanners((prev) => [...prev.filter((b) => b.key !== banner.key), banner]);
  }, []);
  const clearBanner = useCallback((key: string) => {
    setBanners((prev) => prev.filter((b) => b.key !== key));
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const [h, c] = await Promise.allSettled([getHealth(), getConfig()]);
      if (cancelled) return;
      if (h.status === 'fulfilled') {
        setHealth(h.value);
        const bq = h.value.bigquery;
        if (bq && bq.status === 'error') {
          showBanner({
            key: 'bigquery',
            context: 'BigQuery check at startup',
            code: bq.error_code ?? 'bigquery_error',
            message: bq.message ?? 'The startup dry-run against the billing export failed.',
            hint: bq.fix_hint,
          });
        }
      } else {
        showBanner(bannerFromError('health', 'Health check', toApiError(h.reason)));
      }
      if (c.status === 'fulfilled') {
        setConfig(c.value);
        setWeek((w) => w ?? c.value.default_week);
        setConnection('connected');
      } else {
        setConnection('error');
        showBanner(bannerFromError('config', 'Configuration', toApiError(c.reason)));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [showBanner]);

  // ---- selection -----------------------------------------------------------
  const [week, setWeek] = useState<WeekID | null>(null);
  const [scope, setScope] = useState<Scope>(SCOPE_ALL);

  // ---- cost table ----------------------------------------------------------
  const [table, setTable] = useState<CostTableResponse | null>(null);
  const [tableLoading, setTableLoading] = useState(false);
  const [tableError, setTableError] = useState<ApiError | null>(null);
  const [tableVersion, setTableVersion] = useState(0);
  const reloadTable = useCallback(() => setTableVersion((v) => v + 1), []);

  useEffect(() => {
    if (!config) return;
    let cancelled = false;
    setTableLoading(true);
    getCostTable({ scope })
      .then((t) => {
        if (cancelled) return;
        setTable(t);
        setTableError(null);
        clearBanner('table');
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const e = toApiError(err);
        setTable(null);
        setTableError(e);
        showBanner(bannerFromError('table', 'Cost table', e));
      })
      .finally(() => {
        if (!cancelled) setTableLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [config, scope, tableVersion, showBanner, clearBanner]);

  const columns: readonly Column[] = table?.columns ?? FALLBACK_COLUMNS;
  const rows: CostRow[] = useMemo(() => {
    if (table) return table.rows;
    if (config && tableError) return fallbackRows(config);
    return [];
  }, [table, config, tableError]);
  const usingFallback = table === null && tableError !== null;

  // ---- settings (thresholds) ----------------------------------------------
  const [settings, setSettings] = useState<Settings | null>(null);
  const [settingsError, setSettingsError] = useState<ApiError | null>(null);
  useEffect(() => {
    if (!config) return;
    let cancelled = false;
    getSettings()
      .then((s) => {
        if (!cancelled) {
          setSettings(s);
          setSettingsError(null);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) setSettingsError(toApiError(err));
      });
    return () => {
      cancelled = true;
    };
  }, [config]);

  const saveThresholds = useCallback(
    async (pct: number, absUsd: number) => {
      const saved = await putSettings({ anomaly_pct_threshold: pct, anomaly_abs_threshold_usd: absUsd });
      setSettings(saved);
      reloadTable();
    },
    [reloadTable],
  );

  // ---- runs ---------------------------------------------------------------
  const [runs, setRuns] = useState<RunListItem[] | null>(null);
  const [runsError, setRunsError] = useState<ApiError | null>(null);
  const [runsLoading, setRunsLoading] = useState(false);
  const [runsVersion, setRunsVersion] = useState(0);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [highlightRun, setHighlightRun] = useState<number | null>(null);

  useEffect(() => {
    if (!config) return;
    let cancelled = false;
    setRunsLoading(true);
    getRuns(RUNS_LIMIT)
      .then((r) => {
        if (!cancelled) {
          setRuns(r.runs);
          setRunsError(null);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) setRunsError(toApiError(err));
      })
      .finally(() => {
        if (!cancelled) setRunsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [config, runsVersion]);

  // ---- refresh ------------------------------------------------------------
  const [running, setRunning] = useState<RefreshRequest | null>(null);
  const [lastRefresh, setLastRefresh] = useState<RefreshResponse | null>(null);

  const openRun = useCallback((id: number) => {
    setHighlightRun(id);
    setDrawerOpen(true);
  }, []);

  const refresh = useCallback(
    async (req: RefreshRequest) => {
      if (running) return;
      setRunning(req);
      clearBanner('refresh');
      try {
        const res = await postRefresh(req);
        setLastRefresh(res);
        if (res.run.status !== 'ok') {
          const failed = res.run.sources.find((s) => s.status === 'failed');
          showBanner({
            key: 'refresh',
            context: `Refresh run #${res.run.id} ${res.run.status}`,
            code: failed?.error_code ?? (res.run.error ? 'run_failed' : 'unknown'),
            message: failed?.error_message ?? res.run.error ?? 'The refresh did not complete.',
            hint: failed?.fix_hint ?? null,
            action: { label: 'View run', onClick: () => openRun(res.run.id) },
          });
        }
      } catch (err) {
        const e = toApiError(err);
        if (e.code === 'refresh_in_progress') {
          showBanner({ key: 'refresh', context: 'Refresh', code: e.code, message: 'A refresh is already running — wait for it to finish.', hint: e.hint });
        } else {
          showBanner(bannerFromError('refresh', 'Refresh', e));
        }
      } finally {
        setRunning(null);
        reloadTable();
        setRunsVersion((v) => v + 1);
      }
    },
    [running, clearBanner, showBanner, openRun, reloadTable],
  );

  const refreshWeek = useCallback(
    (w: WeekID | null) => {
      if (w) void refresh({ mode: 'week', week: w });
    },
    [refresh],
  );

  const refreshAll = useCallback(() => {
    if (!config) return;
    const ok = window.confirm(
      `Fetch ${config.first_week} → ${config.current_week} in one BigQuery job (about 1 GB scanned, 10–30 s)?`,
    );
    if (ok) void refresh({ mode: 'all' });
  }, [config, refresh]);

  // ---- inspector ----------------------------------------------------------
  const [inspector, setInspector] = useState<InspectorTarget | null>(null);
  const closeInspector = useCallback(() => setInspector(null), []);

  const openInspector = useCallback(
    (row: CostRow, column: Column) => {
      if (!row.snapshot) return;
      const idx = rows.findIndex((r) => r.week === row.week);
      setInspector({ row, column, prevRow: idx > 0 ? (rows[idx - 1] ?? null) : null });
    },
    [rows],
  );

  // Keep the inspector pointed at the latest snapshot of its week after a re-fetch.
  useEffect(() => {
    if (!inspector) return;
    const idx = rows.findIndex((r) => r.week === inspector.row.week);
    const fresh = idx >= 0 ? rows[idx] : undefined;
    if (!fresh || !fresh.snapshot) {
      setInspector(null);
      return;
    }
    if (fresh !== inspector.row) {
      setInspector({ row: fresh, column: inspector.column, prevRow: idx > 0 ? (rows[idx - 1] ?? null) : null });
    }
  }, [rows, inspector]);

  // ---- annotation ---------------------------------------------------------
  const selectedRow = useMemo(() => rows.find((r) => r.week === week) ?? null, [rows, week]);
  const [fallbackNote, setFallbackNote] = useState<{ week: WeekID; body: string | null; error: ApiError | null } | null>(null);

  useEffect(() => {
    // Only needed when the table (which carries row.annotation) is unavailable.
    if (!week || table !== null || !config) return;
    let cancelled = false;
    getAnnotation(week)
      .then((a) => {
        if (!cancelled) setFallbackNote({ week, body: a.body, error: null });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const e = toApiError(err);
        setFallbackNote({ week, body: null, error: e.code === 'not_found' ? null : e });
      });
    return () => {
      cancelled = true;
    };
  }, [week, table, config]);

  const saveAnnotation = useCallback(
    async (body: string): Promise<Annotation> => {
      if (!week) throw new ApiError(0, 'client_error', 'No week selected.', null);
      const saved = await putAnnotation(week, body);
      const stored = saved.body.length > 0 ? saved.body : null;
      setTable((t) => (t ? { ...t, rows: t.rows.map((r) => (r.week === week ? { ...r, annotation: stored } : r)) } : t));
      setFallbackNote((n) => (n && n.week === week ? { ...n, body: stored } : n));
      return saved;
    },
    [week],
  );

  // ---- export -------------------------------------------------------------
  const exportCsv = useCallback(() => {
    if (!config) return;
    const from = table?.from ?? config.first_week;
    const to = table?.to ?? config.current_week;
    downloadTextFile(csvFilename(scope, from, to), buildCsv([...columns], rows));
  }, [config, table, scope, columns, rows]);

  // ---- derived view data --------------------------------------------------
  const weekOptions: WeekOption[] = useMemo(() => {
    if (!config) return [];
    const byWeek = new Map(rows.map((r) => [r.week, r]));
    return weekRange(config.first_week, config.current_week)
      .reverse()
      .map((w) => {
        const r = byWeek.get(w);
        const label = r?.snapshot ? statusText(r.week_status, r.snapshot.stale) : 'not fetched';
        return { id: w, label: weekOptionLabel(w, label) };
      });
  }, [config, rows]);

  const refreshStatusText = useMemo(() => {
    if (running) return running.mode === 'week' ? `Refreshing ${running.week}…` : 'Refreshing all weeks…';
    if (lastRefresh) {
      const r = lastRefresh.run;
      const parts = [`Run #${r.id} ${r.status}`];
      const n = sumRows(r);
      if (n !== null) parts.push(`${formatInteger(n)} rows`);
      const b = sumBytesBilled(r);
      if (b !== null) parts.push(`${formatBytes(b)} billed`);
      if (r.duration_ms !== null) parts.push(formatDurationMs(r.duration_ms));
      return parts.join(' · ');
    }
    const latest = runs?.[0];
    if (latest) {
      const b = sumBytesBilled(latest);
      return `Last run ${formatTimeZ(latest.started_at)} ${latest.status}${b !== null ? ` · ${formatBytes(b)} billed` : ''}`;
    }
    return runsError ? 'Run history unavailable' : 'No runs yet';
  }, [running, lastRefresh, runs, runsError]);

  const noSnapshots = table !== null && table.rows.every((r) => r.snapshot === null);
  const activeTab = tabById(tab);

  return (
    <div className={styles.shell}>
      <TopBar
        config={config}
        health={health}
        connection={connection}
        weekOptions={weekOptions}
        week={week}
        onWeekChange={setWeek}
        scope={scope}
        onScopeChange={setScope}
        running={running}
        onRefreshWeek={() => refreshWeek(week)}
        onRefreshAll={refreshAll}
        statusText={refreshStatusText}
      />

      {banners.length > 0 ? (
        <div className={styles.banners} data-testid="banners">
          {banners.map((b) => (
            <ErrorBanner key={b.key} banner={b} onDismiss={clearBanner} />
          ))}
        </div>
      ) : null}

      <div className={styles.body}>
        <NavTabs active={tab} />

        <main className={styles.panel} id={tab} data-testid="active-panel" data-tab={tab}>
          {tab !== 'cost' ? (
            <StubPanel tab={activeTab.label} phase={activeTab.phase} description={activeTab.description} />
          ) : (
            <>
              <div className={styles.toolbar} data-testid="cost-toolbar">
                <div className={styles.toolbarTitle}>
                  Cost by service — USD, net of credits
                  {config ? (
                    <span className={styles.toolbarMeta}>
                      {' '}
                      · weeks {table?.from ?? config.first_week} → {table?.to ?? config.current_week} · scope: {scopeLabel(scope)}
                    </span>
                  ) : null}
                </div>
                <div className={styles.toolbarActions}>
                  <ThresholdsEditor settings={settings} loadError={settingsError} onSave={saveThresholds} />
                  <button
                    type="button"
                    className={styles.exportButton}
                    data-testid="export-csv"
                    onClick={exportCsv}
                    disabled={!config || rows.length === 0}
                    title="Download the visible columns and rows as CSV"
                  >
                    Export CSV
                  </button>
                </div>
              </div>

              {noSnapshots && config ? (
                <EmptyState title="No weeks fetched yet.">
                  Click <strong>Refresh all weeks</strong> to load {config.first_week} → {config.current_week} from BigQuery in one query
                  (≈10–30 s), or <strong>Refresh</strong> for the selected week only.
                </EmptyState>
              ) : null}

              {connection === 'error' && !config ? (
                <EmptyState title="Waiting for the cloud0 API." testId="no-config">
                  The week list comes from <code>/api/v1/config</code>; see the banner above for the error and its fix.
                </EmptyState>
              ) : (
                <CostTable
                  columns={columns}
                  rows={rows}
                  selectedWeek={week}
                  thresholds={table?.thresholds ?? null}
                  loading={tableLoading || (config !== null && table === null && tableError === null)}
                  fallback={usingFallback}
                  refreshDisabled={running !== null}
                  onCellClick={openInspector}
                  onRefreshWeek={(w) => refreshWeek(w)}
                />
              )}

              {week ? (
                <AnnotationField
                  key={week}
                  week={week}
                  initialBody={table ? (selectedRow?.annotation ?? null) : fallbackNote?.week === week ? fallbackNote.body : null}
                  loading={table === null && (fallbackNote === null || fallbackNote.week !== week)}
                  loadError={table === null && fallbackNote?.week === week ? fallbackNote.error : null}
                  onSave={saveAnnotation}
                />
              ) : null}
            </>
          )}
        </main>

        {inspector && tab === 'cost' ? (
          <CellInspector target={inspector} scope={scope} columns={columns} onClose={closeInspector} />
        ) : null}
      </div>

      <RunLogDrawer
        runs={runs}
        error={runsError}
        loading={runsLoading}
        open={drawerOpen}
        onToggle={() => setDrawerOpen((v) => !v)}
        highlightRunId={highlightRun}
      />

      <footer className={styles.footer}>
        {TABS.filter((t) => t.phase > 1).length} tabs and the two vendor columns are labelled stubs until their phase lands.
      </footer>
    </div>
  );
}
