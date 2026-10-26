import { useCallback, useEffect, useState } from 'react';
import { getLimits, putLimits, toApiError } from '@/lib/api';
import { downloadTextFile } from '@/lib/csv';
import { formatTimestamp } from '@/lib/format';
import { barWidth, buildLimitsCsv, formatLimitPct, formatQuantity, levelLabel, limitsCsvFilename, levelTone, parseAllowanceInput, parseWarnPctInput } from '@/lib/limitsFormat';
import type { LimitStatus, LimitsResponse, LimitsUpdate, WeekID } from '@/lib/types';
import { EmptyState } from './EmptyState';
import { ErrorBanner, bannerFromError, type BannerData } from './ErrorBanner';
import tableStyles from './CostTable.module.css';
import styles from './LimitsPanel.module.css';

/** Server/validation error shown inline next to the field that caused it. */
export interface InlineError {
  code: string;
  message: string;
  hint: string | null;
}

function errorText(e: InlineError): string {
  return `${e.code} — ${e.message}${e.hint ? ` ${e.hint}` : ''}`;
}

export function LevelBadge({ level }: { level: LimitStatus['level'] }) {
  const tone = levelTone(level);
  return (
    <span className={`${styles.badge} ${styles[tone] ?? ''}`} data-testid="limit-level" data-level={level}>
      {levelLabel(level)}
    </span>
  );
}

export interface LimitsViewProps {
  data: LimitsResponse;
  /** Persists one change; rejects with an ApiError (400 invalid_limit shown inline). */
  onSave: (update: LimitsUpdate) => Promise<void>;
  /** Initial edit state (tests render the editing/error markup statically). */
  initialEdit?: { id: string; draft: string; error: InlineError | null } | null;
  initialWarn?: { draft: string; error: InlineError | null } | null;
}

export function LimitsView({ data, onSave, initialEdit = null, initialWarn = null }: LimitsViewProps) {
  const [edit, setEdit] = useState(initialEdit);
  const [warn, setWarn] = useState<{ draft: string; error: InlineError | null }>(initialWarn ?? { draft: String(data.warn_pct), error: null });
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setWarn((w) => (w.error ? w : { draft: String(data.warn_pct), error: null }));
  }, [data.warn_pct]);

  async function save(update: LimitsUpdate, onError: (e: InlineError) => void, onDone: () => void) {
    setSaving(true);
    try {
      await onSave(update);
      onDone();
    } catch (err: unknown) {
      const e = toApiError(err);
      onError({ code: e.code, message: e.message, hint: e.hint });
    } finally {
      setSaving(false);
    }
  }

  function saveAllowance() {
    if (!edit) return;
    const parsed = parseAllowanceInput(edit.draft);
    if (!parsed.ok) {
      setEdit({ ...edit, error: { code: 'invalid_limit', message: parsed.error, hint: null } });
      return;
    }
    const id = edit.id;
    void save({ allowances: { [id]: parsed.value } }, (error) => setEdit((cur) => (cur ? { ...cur, error } : cur)), () => setEdit(null));
  }

  function saveWarn() {
    const parsed = parseWarnPctInput(warn.draft);
    if (!parsed.ok) {
      setWarn({ ...warn, error: { code: 'invalid_limit', message: parsed.error, hint: null } });
      return;
    }
    void save({ warn_pct: parsed.value }, (error) => setWarn((w) => ({ ...w, error })), () => setWarn((w) => ({ ...w, error: null })));
  }

  return (
    <>
      <div className={styles.meta}>
        <span data-testid="limits-month">
          Month to date: <strong>{data.month}</strong> ({data.month_start} … {data.month_end}) · projected linearly to month end
        </span>
        <span className={styles.warn}>
          <label>
            Warn at (%){' '}
            <input
              className={styles.input}
              data-testid="limits-warn-input"
              type="number"
              min={0}
              max={100}
              step="any"
              value={warn.draft}
              onChange={(e) => setWarn({ draft: e.target.value, error: null })}
            />
          </label>
          <button type="button" className={styles.save} data-testid="limits-warn-save" disabled={saving || warn.draft.trim() === String(data.warn_pct)} onClick={saveWarn}>
            Save
          </button>
        </span>
        <span className={styles.muted}>{data.updated_at ? `Allowances updated ${formatTimestamp(data.updated_at)}` : 'Allowances at defaults'}</span>
      </div>
      {warn.error ? (
        <p className={styles.error} role="alert" data-testid="limits-warn-error">
          {errorText(warn.error)}
        </p>
      ) : null}
      <div className={tableStyles.scroller}>
        <table className={tableStyles.table} data-testid="limits-table">
          <caption className={tableStyles.caption}>
            Free-tier allowances vs month-to-date usage · ok &lt; warn %, near ≥ warn %, over ≥ 100 % · weekly figure for {data.week}.
          </caption>
          <thead>
            <tr>
              <th scope="col" className={tableStyles.weekHead}>
                Limit
              </th>
              <th scope="col">Unit</th>
              <th scope="col" className={tableStyles.numHead}>
                Used (MTD)
              </th>
              <th scope="col" className={tableStyles.numHead}>
                Projected
              </th>
              <th scope="col" className={tableStyles.numHead}>
                Allowance
              </th>
              <th scope="col">% of allowance</th>
              <th scope="col">Level</th>
              <th scope="col" className={tableStyles.numHead}>
                This week
              </th>
            </tr>
          </thead>
          <tbody>
            {data.limits.map((l) => {
              const editing = edit?.id === l.id;
              const tone = levelTone(l.level);
              return (
                <tr key={l.id} className={`${tableStyles.row} ${l.stub ? styles.stubRow : ''}`} data-testid={`limit-row-${l.id}`} data-level={l.level} data-stub={l.stub || undefined}>
                  <th scope="row" className={tableStyles.weekCell}>
                    <div className={tableStyles.weekId}>{l.label}</div>
                    <div className={tableStyles.weekMeta}>{l.service}</div>
                    {l.reason ? (
                      <div className={styles.reason} data-testid={`limit-reason-${l.id}`}>
                        {l.reason}
                      </div>
                    ) : null}
                  </th>
                  <td>{l.unit}</td>
                  <td className={tableStyles.num} data-testid={`limit-used-${l.id}`}>
                    {formatQuantity(l.used)}
                  </td>
                  <td className={tableStyles.num}>{formatQuantity(l.projected)}</td>
                  <td className={tableStyles.num} data-testid={`limit-allowance-${l.id}`}>
                    {editing && edit ? (
                      <span className={styles.editor}>
                        <input
                          className={styles.input}
                          data-testid={`limit-input-${l.id}`}
                          aria-label={`Allowance for ${l.label}`}
                          type="number"
                          min={0}
                          step="any"
                          value={edit.draft}
                          onChange={(e) => setEdit({ id: l.id, draft: e.target.value, error: null })}
                        />
                        <button type="button" className={styles.save} data-testid={`limit-save-${l.id}`} disabled={saving} onClick={saveAllowance}>
                          {saving ? 'Saving…' : 'Save'}
                        </button>
                        <button type="button" className={styles.cancel} data-testid={`limit-cancel-${l.id}`} disabled={saving} onClick={() => setEdit(null)}>
                          Cancel
                        </button>
                      </span>
                    ) : (
                      <span className={styles.allowance}>
                        {formatQuantity(l.allowance)}
                        {!l.stub ? (
                          <button
                            type="button"
                            className={tableStyles.link}
                            data-testid={`limit-edit-${l.id}`}
                            onClick={() => setEdit({ id: l.id, draft: String(l.allowance), error: null })}
                            title={`Default ${formatQuantity(l.default_allowance)}`}
                          >
                            Edit
                          </button>
                        ) : null}
                      </span>
                    )}
                    {editing && edit?.error ? (
                      <div className={styles.error} role="alert" data-testid={`limit-error-${l.id}`}>
                        {errorText(edit.error)}
                      </div>
                    ) : null}
                  </td>
                  <td className={styles.barCell}>
                    {l.pct === null ? (
                      <span className={styles.muted}>—</span>
                    ) : (
                      <span className={styles.barWrap} title={`${formatLimitPct(l.pct)} of allowance (projected)`}>
                        <span className={styles.track}>
                          <span className={`${styles.fill} ${styles[`fill_${tone}`] ?? ''}`} style={{ width: `${barWidth(l.pct)}%` }} data-testid={`limit-bar-${l.id}`} />
                        </span>
                        <span className={styles.pct}>{formatLimitPct(l.pct)}</span>
                      </span>
                    )}
                  </td>
                  <td>
                    <LevelBadge level={l.level} />
                  </td>
                  <td className={tableStyles.num}>{formatQuantity(l.week_used)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
}

interface Props {
  week: WeekID;
  /** Bumped by the parent after any refresh; re-fetches the limits. */
  refreshToken: number;
}

export default function LimitsPanel({ week, refreshToken }: Props) {
  const [data, setData] = useState<LimitsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [banner, setBanner] = useState<BannerData | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getLimits(week)
      .then((res) => {
        if (cancelled) return;
        setData(res);
        setBanner(null);
      })
      .catch((err: unknown) => {
        if (!cancelled) setBanner(bannerFromError('limits', 'Limits', toApiError(err)));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [week, refreshToken]);

  const onSave = useCallback(
    async (update: LimitsUpdate) => {
      // Errors (400 invalid_limit) propagate to the field that caused them.
      const res = await putLimits(update, week);
      setData(res);
    },
    [week],
  );

  return (
    <div className={styles.panel} data-testid="limits-panel">
      <div className={styles.toolbar}>
        <button
          type="button"
          className={styles.secondary}
          data-testid="limits-export"
          disabled={!data}
          onClick={() => data && downloadTextFile(limitsCsvFilename(data.week), buildLimitsCsv(data))}
        >
          Export CSV
        </button>
      </div>
      {banner ? <ErrorBanner banner={banner} onDismiss={() => setBanner(null)} /> : null}
      {data === null ? (
        loading ? (
          <div className={styles.skeleton} aria-busy="true" aria-label="loading limits" />
        ) : (
          <EmptyState title="Limits unavailable" testId="limits-unavailable">
            The limits could not be loaded (see the banner above).
          </EmptyState>
        )
      ) : (
        <LimitsView data={data} onSave={onSave} />
      )}
    </div>
  );
}
