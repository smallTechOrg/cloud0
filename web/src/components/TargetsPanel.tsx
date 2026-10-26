import { useCallback, useEffect, useMemo, useState } from 'react';
import { getTargets, putTargets } from '@/lib/attributionApi';
import type { TargetsResponse } from '@/lib/attributionTypes';
import { ApiError, toApiError } from '@/lib/api';
import { downloadTextFile } from '@/lib/csv';
import { useCurrency } from '@/lib/currency';
import { TARGETS_NOTE, buildTargetsCsv, buildTargetsUpdate, targetsCsvFilename, targetsEmpty, targetsSummary } from '@/lib/dimensionFormat';
import { formatMoney } from '@/lib/format';
import { currentMonthId, isMonthId } from '@/lib/monthFormat';
import { EmptyState } from './EmptyState';
import { ErrorBanner, bannerFromError, type BannerData } from './ErrorBanner';
import { TargetRowItem } from './TargetRow';
import tableStyles from './CostTable.module.css';
import styles from './TargetsPanel.module.css';

export interface TargetsViewProps {
  data: TargetsResponse;
  drafts: Record<string, string>;
  errors: Record<string, string>;
  saving: boolean;
  dirty: boolean;
  saveError: string | null;
  notice: string | null;
  onDraft: (product: string, text: string) => void;
  onSave: () => void;
  onDiscard: () => void;
}

export function TargetsView(p: TargetsViewProps) {
  const currency = useCurrency();
  const { data } = p;
  const noData = data.account.status === 'no_data' && data.share_weeks.length === 0;
  return (
    <>
      <p className={styles.summary} data-testid="targets-summary">
        {targetsSummary(data, currency)}
        {data.covered_until ? ` · products covered until ${data.covered_until}` : ''}
      </p>
      {data.account.status === 'no_data' ? (
        <EmptyState title={`No data for ${data.month} yet`} testId="targets-no-data">
          Refresh the month on the Month tab to fetch its cost.
        </EmptyState>
      ) : null}
      {targetsEmpty(data) ? (
        <EmptyState title="No products to show yet" testId="targets-empty">
          Add rules in the Attribution tab first{noData ? ', then Refresh a week of this month' : ''}; products appear here once cost is attributed to them.
        </EmptyState>
      ) : null}
      <div className={tableStyles.scroller}>
        <table className={tableStyles.table} data-testid="targets-table">
          <caption className={tableStyles.caption}>
            Monthly targets · {currency}, net of credits · a target applies from {data.month} until changed.
          </caption>
          <thead>
            <tr>
              <th scope="col" className={tableStyles.weekHead}>Name</th>
              <th scope="col" className={tableStyles.numHead}>Target</th>
              <th scope="col" className={tableStyles.numHead}>Net to date</th>
              <th scope="col" className={tableStyles.numHead}>Projected</th>
              <th scope="col" className={tableStyles.numHead}>Remaining</th>
              <th scope="col" className={tableStyles.weekHead}>Status</th>
              <th scope="col" className={tableStyles.weekHead}>Progress</th>
            </tr>
          </thead>
          <tbody>
            <TargetRowItem row={data.account} draft={p.drafts['*']} error={p.errors['*']} disabled={p.saving} onDraft={p.onDraft} />
            {data.products.map((r) => (
              <TargetRowItem key={r.product} row={r} draft={p.drafts[r.product]} error={p.errors[r.product]} disabled={p.saving} onDraft={p.onDraft} />
            ))}
            <tr className={`${tableStyles.row} ${styles.special}`} data-testid="target-row-shared">
              <th scope="row" className={tableStyles.weekCell}>
                (shared)
                <div className={styles.hint}>{data.shared.share_pct.toFixed(1)} % of the month · no target</div>
              </th>
              <td className={tableStyles.num}>—</td>
              <td className={tableStyles.num}>{formatMoney(data.shared.net, currency)}</td>
              <td className={tableStyles.num}>{formatMoney(data.shared.projected, currency)}</td>
              <td colSpan={3} className={styles.hint}>Infrastructure not tied to one product.</td>
            </tr>
            <tr className={`${tableStyles.row} ${styles.special} ${styles.unattr}`} data-testid="target-row-unattributed">
              <th scope="row" className={tableStyles.weekCell}>
                (unattributed)
                <div className={styles.hint}>{data.unattributed.share_pct.toFixed(1)} % of the month · no target</div>
              </th>
              <td className={tableStyles.num}>—</td>
              <td className={tableStyles.num}>{formatMoney(data.unattributed.net, currency)}</td>
              <td className={tableStyles.num}>{formatMoney(data.unattributed.projected, currency)}</td>
              <td colSpan={3} className={styles.hint}>Not matched by any rule; add rules in the Attribution tab.</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p className={styles.note} data-testid="targets-note">{TARGETS_NOTE}</p>
      {p.saveError ? (
        <p className={styles.saveError} role="alert" data-testid="targets-save-error">
          <code>invalid_target</code> — {p.saveError}
        </p>
      ) : null}
      <div className={styles.saveRow}>
        <button type="button" className={styles.primary} data-testid="targets-save" disabled={!p.dirty || p.saving} aria-busy={p.saving || undefined} onClick={p.onSave}>
          {p.saving ? 'Saving…' : 'Save targets'}
        </button>
        <button type="button" className={styles.secondary} data-testid="targets-discard" disabled={!p.dirty || p.saving} onClick={p.onDiscard}>
          Discard changes
        </button>
        {p.notice ? (
          <span className={styles.notice} role="status" data-testid="targets-notice">
            {p.notice}
          </span>
        ) : null}
      </div>
    </>
  );
}

interface Props {
  /** Bumped by the parent after any refresh; re-fetches. */
  refreshToken: number;
}

export default function TargetsPanel({ refreshToken }: Props) {
  const [month, setMonth] = useState<string>(() => currentMonthId(new Date()));
  const [data, setData] = useState<TargetsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [banner, setBanner] = useState<BannerData | null>(null);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    if (!isMonthId(month)) return;
    let cancelled = false;
    setLoading(true);
    getTargets({ month })
      .then((res) => {
        if (cancelled) return;
        setData(res);
        setBanner(null);
      })
      .catch((err: unknown) => {
        if (!cancelled) setBanner(bannerFromError('targets', 'Targets', toApiError(err)));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [month, refreshToken]);

  const rows = useMemo(() => (data ? [data.account, ...data.products] : []), [data]);
  const dirty = useMemo(() => {
    const r = buildTargetsUpdate(rows, drafts, month);
    return r.update !== null || Object.keys(r.errors).length > 0;
  }, [rows, drafts, month]);

  const onDraft = useCallback((product: string, text: string) => {
    setDrafts((d) => ({ ...d, [product]: text }));
    setErrors((e) => {
      if (!(product in e)) return e;
      const { [product]: _gone, ...rest } = e;
      void _gone;
      return rest;
    });
    setSaveError(null);
    setNotice(null);
  }, []);

  async function save() {
    if (!data) return;
    const { update, errors: clientErrors } = buildTargetsUpdate(rows, drafts, month);
    setErrors(clientErrors);
    if (!update) return;
    setSaving(true);
    setSaveError(null);
    setNotice(null);
    try {
      const res = await putTargets(update, { month });
      setData(res);
      setDrafts({});
      setNotice(`Saved ${update.targets.length} target${update.targets.length === 1 ? '' : 's'} from ${month}.`);
    } catch (err: unknown) {
      const e = toApiError(err);
      if (e instanceof ApiError && e.code === 'invalid_target') {
        setSaveError(e.message);
        const named = Object.fromEntries(update.targets.filter((t) => e.message.includes(t.product)).map((t) => [t.product, e.message]));
        setErrors(named);
      } else {
        setBanner(bannerFromError('targets-save', 'Save targets', e));
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className={styles.panel} data-testid="targets-panel">
      <div className={styles.toolbar}>
        <label className={styles.picker}>
          <span>Month</span>
          <input
            type="month"
            data-testid="targets-month"
            value={month}
            max={currentMonthId(new Date())}
            onChange={(e) => {
              setMonth(e.target.value);
              setDrafts({});
              setErrors({});
              setSaveError(null);
              setNotice(null);
            }}
          />
        </label>
        <button type="button" className={styles.secondary} data-testid="targets-export" disabled={!data} onClick={() => data && downloadTextFile(targetsCsvFilename(data), buildTargetsCsv(data))}>
          Export CSV
        </button>
      </div>
      {!isMonthId(month) ? <p className={styles.muted}>Pick a month as YYYY-MM.</p> : null}
      {banner ? <ErrorBanner banner={banner} onDismiss={() => setBanner(null)} /> : null}
      {data === null ? (
        loading ? (
          <div className={styles.skeleton} aria-busy="true" aria-label="loading targets" data-testid="targets-loading" />
        ) : (
          <EmptyState title="Targets unavailable" testId="targets-unavailable">
            Targets could not be loaded (see the banner above).
          </EmptyState>
        )
      ) : (
        <TargetsView
          data={data}
          drafts={drafts}
          errors={errors}
          saving={saving}
          dirty={dirty}
          saveError={saveError}
          notice={notice}
          onDraft={onDraft}
          onSave={() => void save()}
          onDiscard={() => {
            setDrafts({});
            setErrors({});
            setSaveError(null);
          }}
        />
      )}
    </div>
  );
}
