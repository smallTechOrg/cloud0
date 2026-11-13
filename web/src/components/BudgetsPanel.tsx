import { useCallback, useEffect, useMemo, useState } from 'react';
import { getAlertsStatus, getBudgetSuggestions, getBudgets, getChannels, getHistory, getMonth, getRouting, toApiError } from '@/lib/api';
import {
  buildHistoryCsv,
  createRequest,
  deleteRequest,
  formFromBudget,
  historyCsvFilename,
  initialForm,
  monthScopeFor,
  updateRequest,
  type FormState,
} from '@/lib/budgetsFormat';
import { downloadTextFile } from '@/lib/csv';
import { useCurrency } from '@/lib/currency';
import type {
  AlertChannel,
  AlertsStatus,
  ApplyResult,
  BudgetDraft,
  BudgetSuggestion,
  BudgetView,
  BudgetsResponse,
  ChangeRecord,
  HistoryKind,
  PreviewRequest,
  RoutingResponse,
} from '@/lib/types';
import PreviewApplyDrawer from '@/components/PreviewApplyDrawer';
import { BudgetCard } from './BudgetCard';
import { BudgetForm, type BudgetFormMode } from './BudgetForm';
import styles from './BudgetsPanel.module.css';
import { ChangeHistoryTable } from './ChangeHistoryTable';
import { EmptyState } from './EmptyState';
import { ErrorBanner, bannerFromError, type BannerData } from './ErrorBanner';
import { SuggestionList } from './SuggestionList';

export const SETUP_HINT = 'Set CLOUD0_ALERTS_CREDENTIALS to the cloud0-alerts service-account key path and restart — see README → Alerts setup.';

/** Plain setup hint shown when budgets cannot be read (503 alerts_not_configured). */
export function BudgetsSetupHint({ hint = SETUP_HINT }: { hint?: string }) {
  return (
    <div className={styles.setup} role="status" data-testid="budgets-not-configured">
      <strong>Budgets are not set up yet</strong>
      <p>
        Budgets use the write-capable cloud0-alerts service account. {hint}
      </p>
    </div>
  );
}

interface FormSession {
  mode: BudgetFormMode;
  initial: FormState;
  /** Resource name for update; '' otherwise. */
  target: string;
  unmanagedEdit: boolean;
  /** Changes when the form must be re-created from a new source. */
  key: string;
}

interface Props {
  refreshToken: number;
}

export default function BudgetsPanel({ refreshToken }: Props) {
  const currency = useCurrency();
  const [reload, setReload] = useState(0);
  const [status, setStatus] = useState<AlertsStatus | null>(null);
  const [budgets, setBudgets] = useState<BudgetsResponse | null>(null);
  const [suggestions, setSuggestions] = useState<BudgetSuggestion[]>([]);
  const [channels, setChannels] = useState<AlertChannel[]>([]);
  const [routing, setRouting] = useState<RoutingResponse | null>(null);
  const [spend, setSpend] = useState<Record<string, number | null>>({});
  const [month, setMonth] = useState<string>('');
  const [loading, setLoading] = useState(true);
  const [notConfigured, setNotConfigured] = useState<string | null>(null);
  const [banners, setBanners] = useState<BannerData[]>([]);

  const [kind, setKind] = useState('');
  const [history, setHistory] = useState<ChangeRecord[]>([]);
  const [historyLoaded, setHistoryLoaded] = useState(false);

  const [form, setForm] = useState<FormSession | null>(null);
  const [request, setRequest] = useState<PreviewRequest | null>(null);
  const [applied, setApplied] = useState<string | null>(null);

  const putBanner = useCallback((b: BannerData) => setBanners((cur) => [...cur.filter((x) => x.key !== b.key), b]), []);
  const dropBanner = useCallback((key: string) => setBanners((cur) => cur.filter((x) => x.key !== key)), []);

  // Budgets, suggestions, channels, routing, status.
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    void (async () => {
      const [st, bu, su, ch, ro] = await Promise.allSettled([getAlertsStatus(), getBudgets(), getBudgetSuggestions(), getChannels(), getRouting()]);
      if (cancelled) return;
      if (st.status === 'fulfilled') setStatus(st.value);
      else putBanner(bannerFromError('alerts-status', 'Alerts status', toApiError(st.reason)));

      if (bu.status === 'fulfilled') {
        setBudgets(bu.value);
        setNotConfigured(null);
        dropBanner('budgets');
      } else {
        const e = toApiError(bu.reason);
        if (e.code === 'alerts_not_configured') {
          setNotConfigured(e.hint ?? SETUP_HINT);
          setBudgets(null);
        } else {
          putBanner(bannerFromError('budgets', 'Budgets', e));
        }
      }
      if (su.status === 'fulfilled') setSuggestions(su.value.suggestions);
      else {
        setSuggestions([]);
        const e = toApiError(su.reason);
        if (e.code !== 'alerts_not_configured') putBanner(bannerFromError('suggestions', 'Suggestions', e));
      }
      if (ch.status === 'fulfilled') setChannels(ch.value.channels);
      if (ro.status === 'fulfilled') setRouting(ro.value);
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [refreshToken, reload, putBanner, dropBanner]);

  // Month-to-date net for each scope a budget can be compared with.
  const scopesKey = useMemo(() => [...new Set((budgets?.budgets ?? []).map((b) => monthScopeFor(b)).filter((s): s is string => s !== null))].sort().join('\n'), [budgets]);
  useEffect(() => {
    if (scopesKey === '') return;
    let cancelled = false;
    void Promise.all(
      scopesKey.split('\n').map(async (scope) => {
        try {
          const r = await getMonth({ scope });
          return [scope, r.summary.has_data ? r.summary.net : null, r.summary.month] as const;
        } catch {
          return [scope, null, ''] as const;
        }
      }),
    ).then((rows) => {
      if (cancelled) return;
      setSpend(Object.fromEntries(rows.map(([s, n]) => [s, n])));
      setMonth(rows.find((r) => r[2] !== '')?.[2] ?? '');
    });
    return () => {
      cancelled = true;
    };
  }, [scopesKey, refreshToken]);

  // Change history.
  useEffect(() => {
    let cancelled = false;
    getHistory({ kind: kind === '' ? undefined : (kind as HistoryKind), limit: 50 })
      .then((r) => {
        if (cancelled) return;
        setHistory(r.entries);
        setHistoryLoaded(true);
        dropBanner('history');
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          putBanner(bannerFromError('history', 'Change history', toApiError(err)));
          setHistoryLoaded(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [kind, refreshToken, reload, putBanner, dropBanner]);

  const serviceNames = useMemo(() => Object.fromEntries(suggestions.filter((s) => s.service_id !== '').map((s) => [s.service_id, s.service_name])), [suggestions]);
  const channelNames = useMemo(() => Object.fromEntries(channels.map((c) => [c.name, c.display_name])), [channels]);
  const canWrite = status?.alerts_configured === true && notConfigured === null;

  function openCreate(s?: BudgetSuggestion) {
    setApplied(null);
    setForm({ mode: 'create', initial: initialForm(s ?? suggestions.find((x) => x.scope === 'account') ?? null), target: '', unmanagedEdit: false, key: `create-${s?.service_id ?? 'new'}-${Date.now()}` });
  }
  function openAdopt(b: BudgetView) {
    setApplied(null);
    setForm({ mode: 'adopt', initial: formFromBudget(b, true), target: '', unmanagedEdit: false, key: `adopt-${b.name}` });
  }
  function openEdit(b: BudgetView) {
    setApplied(null);
    setForm({ mode: 'edit', initial: formFromBudget(b, false), target: b.name, unmanagedEdit: !b.managed, key: `edit-${b.name}` });
  }
  function submit(draft: BudgetDraft) {
    if (!form) return;
    setRequest(form.mode === 'edit' ? updateRequest(form.target, draft) : createRequest(draft));
  }
  function onApplied(r: ApplyResult) {
    setApplied(`${r.action} ${r.kind} applied${r.target ? ` (${r.target})` : ''} — recorded in the change history as #${r.change_history_id}.`);
    setForm(null);
    setReload((n) => n + 1);
  }

  const list = budgets?.budgets ?? [];
  return (
    <div className={styles.panel} data-testid="budgets-panel">
      {banners.map((b) => (
        <ErrorBanner key={b.key} banner={b} onDismiss={dropBanner} />
      ))}
      {applied ? (
        <p className={styles.notice} role="status" data-testid="budgets-applied">
          {applied}
        </p>
      ) : null}
      {status && !status.alerts_configured && notConfigured === null ? <BudgetsSetupHint /> : null}

      <section className={styles.section} aria-label="Budgets">
        <div className={styles.sectionHead}>
          <h3>Budgets</h3>
          {budgets ? <span className={styles.muted}>Billing account {budgets.billing_account}</span> : null}
          <span className={styles.spacer} />
          <button
            type="button"
            className={styles.primary}
            data-testid="budget-create"
            disabled={!canWrite}
            title={canWrite ? undefined : SETUP_HINT}
            onClick={() => openCreate()}
          >
            Create budget
          </button>
        </div>
        {notConfigured !== null ? (
          <BudgetsSetupHint hint={notConfigured} />
        ) : loading && budgets === null ? (
          <div className={styles.skeleton} aria-busy="true" aria-label="loading budgets" data-testid="budgets-loading" />
        ) : budgets === null ? (
          <EmptyState title="Budgets unavailable" testId="budgets-unavailable">
            The budgets could not be loaded (see the banner above).
          </EmptyState>
        ) : list.length === 0 ? (
          <EmptyState title="No budgets yet" testId="budgets-empty">
            Create one from a suggestion below, or with the Create budget button.
          </EmptyState>
        ) : (
          <div className={styles.cards} data-testid="budgets-list">
            {list.map((b) => {
              const scope = monthScopeFor(b);
              return (
                <BudgetCard
                  key={b.name}
                  budget={b}
                  serviceNames={serviceNames}
                  channelNames={channelNames}
                  currency={currency}
                  spent={scope === null ? null : (spend[scope] ?? null)}
                  month={month}
                  canWrite={canWrite}
                  onAdopt={openAdopt}
                  onEdit={openEdit}
                  onDelete={(x) => setRequest(deleteRequest(x.name))}
                />
              );
            })}
          </div>
        )}
      </section>

      {form ? (
        <BudgetForm
          key={form.key}
          initial={form.initial}
          mode={form.mode}
          unmanagedEdit={form.unmanagedEdit}
          suggestions={suggestions}
          channels={channels}
          routing={routing}
          projects={status?.projects ?? []}
          currency={currency}
          canWrite={canWrite}
          onSubmit={submit}
          onCancel={() => setForm(null)}
        />
      ) : null}

      {notConfigured === null ? (
        <section className={styles.section} aria-label="Suggestions">
          <div className={styles.sectionHead}>
            <h3>Suggested budgets</h3>
          </div>
          <SuggestionList suggestions={suggestions} currency={currency} canWrite={canWrite} onCreate={openCreate} />
        </section>
      ) : null}

      {historyLoaded ? (
        <ChangeHistoryTable
          entries={history}
          kind={kind}
          onKind={setKind}
          onExport={() => downloadTextFile(historyCsvFilename(kind), buildHistoryCsv(history))}
        />
      ) : (
        <div className={styles.skeleton} aria-busy="true" aria-label="loading change history" />
      )}

      <PreviewApplyDrawer request={request} onClose={() => setRequest(null)} onApplied={onApplied} />
    </div>
  );
}
