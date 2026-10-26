import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, toApiError } from '@/lib/api';
import {
  aliasesToRows,
  attributionCsvFilename,
  buildLabelSuggestionsCsv,
  buildUnattributedCsv,
  coverageSegments,
  formatCoveragePct,
  nextPriority,
  parseInvalidRuleIndex,
  rowsToAliases,
  ruleFromUnattributed,
  rulesEqual,
  toWireRule,
  validateRule,
  type AliasRow,
} from '@/lib/attributionFormat';
import {
  applySeed,
  explainResource,
  getAttributionRules,
  getCostByProduct,
  getLabelSuggestions,
  getUnattributed,
  previewSeed,
  proposeRules,
  putAttributionRules,
} from '@/lib/attributionApi';
import type {
  AllocateMode,
  AttributionRule,
  BreakdownResponse,
  LabelSuggestionsResponse,
  RulesResponse,
  SeedPreview as SeedPreviewData,
  UnattributedItem,
  UnattributedResponse,
} from '@/lib/attributionTypes';
import { downloadTextFile } from '@/lib/csv';
import { useCurrency } from '@/lib/currency';
import { formatMoney } from '@/lib/format';
import type { Coverage } from '@/lib/attributionTypes';
import ExplainDrawer, { type ExplainState } from './ExplainDrawer';
import LabelSuggestions from './LabelSuggestions';
import ProposeDialog, { type ProposeState } from './ProposeDialog';
import RulesEditor, { type EditorRule } from './RulesEditor';
import SeedPreview from './SeedPreview';
import UnattributedList from './UnattributedList';
import { ErrorBanner, bannerFromError, type BannerData } from './ErrorBanner';
import styles from './AttributionPanel.module.css';

function errText(e: ApiError): string {
  return `${e.code} — ${e.message}${e.hint ? ` ${e.hint}` : ''}`;
}

// ---- Coverage header (pure view) ---------------------------------------------

export interface CoverageHeaderProps {
  coverage: Coverage | null;
  loading: boolean;
  error: string | null;
  ruleCount: number | null;
  enabledCount: number | null;
  /** True when the week has no snapshot. */
  noSnapshot: boolean;
  week: string;
}

export function CoverageHeader({ coverage, loading, error, ruleCount, enabledCount, noSnapshot, week }: CoverageHeaderProps) {
  const currency = useCurrency();
  return (
    <section className={styles.section} data-testid="coverage-header" aria-labelledby="coverage-h">
      <div className={styles.sectionHead}>
        <h3 id="coverage-h">Coverage — {week}</h3>
        <span className={styles.muted}>share of net cost with a product</span>
      </div>
      {error ? (
        <p className={styles.err} role="alert" data-testid="coverage-error">
          {error}
        </p>
      ) : null}
      {coverage === null ? (
        loading ? (
          <div className={styles.skeleton} aria-busy="true" aria-label="loading coverage" />
        ) : (
          <div className={styles.notice} data-testid="coverage-unavailable">
            Coverage could not be computed. Check the error above.
          </div>
        )
      ) : noSnapshot ? (
        <div className={styles.notice} data-testid="coverage-nosnapshot">
          No snapshot for {week}: nothing to attribute yet. Use Refresh to fetch billing rows.
        </div>
      ) : (
        <>
          <div className={styles.stats}>
            <div className={styles.stat}>
              <span className={styles.statValue} data-testid="coverage-pct">
                {formatCoveragePct(coverage.attributed_pct)}
              </span>
              <span className={styles.statLabel}>attributed</span>
            </div>
            <div className={styles.stat}>
              <span className={styles.statValue} data-testid="coverage-rules">
                {ruleCount ?? '—'}
              </span>
              <span className={styles.statLabel}>rules{enabledCount !== null ? ` (${enabledCount} enabled)` : ''}</span>
            </div>
            <div className={styles.stat}>
              <span className={styles.statValue} data-testid="coverage-unattributed">
                {formatMoney(coverage.unattributed_net, currency)}
              </span>
              <span className={styles.statLabel}>unattributed net ({formatCoveragePct(coverage.unattributed_pct)})</span>
            </div>
            <div className={styles.stat}>
              <span className={styles.statValue} data-testid="coverage-shared">
                {formatMoney(coverage.shared_net, currency)}
              </span>
              <span className={styles.statLabel}>shared net ({formatCoveragePct(coverage.shared_pct)})</span>
            </div>
            <div className={styles.stat}>
              <span className={styles.statValue}>{formatMoney(coverage.total_net, currency)}</span>
              <span className={styles.statLabel}>total net</span>
            </div>
          </div>
          <CoverageBar coverage={coverage} />
        </>
      )}
    </section>
  );
}

function CoverageBar({ coverage }: { coverage: Coverage }) {
  const s = coverageSegments(coverage);
  return (
    <>
      <div
        className={styles.cover}
        role="img"
        aria-label={`Attributed ${formatCoveragePct(coverage.attributed_pct)}, shared ${formatCoveragePct(coverage.shared_pct)}, unattributed ${formatCoveragePct(coverage.unattributed_pct)}`}
        data-testid="coverage-bar"
      >
        <div className={styles.coverAttributed} style={{ width: `${s.attributed}%` }} />
        <div className={styles.coverShared} style={{ width: `${s.shared}%` }} />
        <div className={styles.coverUnattributed} style={{ width: `${s.unattributed}%` }} />
      </div>
      <div className={styles.legend}>
        <span>
          <span className={styles.dot} style={{ background: 'var(--ok)' }} />
          attributed
        </span>
        <span>
          <span className={styles.dot} style={{ background: '#60a5fa' }} />
          shared
        </span>
        <span>
          <span className={styles.dot} style={{ background: '#f59e0b' }} />
          unattributed
        </span>
      </div>
    </>
  );
}

// ---- Data loading hook ------------------------------------------------------------

interface Loaded<T> {
  data: T | null;
  error: ApiError | null;
  loading: boolean;
  reload: () => void;
}

function useLoad<T>(fetcher: () => Promise<T>, deps: unknown[]): Loaded<T> {
  const [state, setState] = useState<{ data: T | null; error: ApiError | null; loading: boolean }>({ data: null, error: null, loading: true });
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let cancelled = false;
    setState((s) => ({ ...s, loading: true }));
    fetcher()
      .then((data) => {
        if (!cancelled) setState({ data, error: null, loading: false });
      })
      .catch((err: unknown) => {
        if (!cancelled) setState((s) => ({ data: s.data, error: toApiError(err), loading: false }));
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);
  return { ...state, reload: useCallback(() => setTick((t) => t + 1), []) };
}

// ---- Panel ----------------------------------------------------------------------------

interface Props {
  week: string;
  scope: string;
  /** Bumped by the parent after any refresh; re-fetches everything. */
  refreshToken: number;
}

export default function AttributionPanel({ week, scope, refreshToken }: Props) {
  const rulesQ = useLoad<RulesResponse>(() => getAttributionRules(), [refreshToken]);
  const coverageQ = useLoad<BreakdownResponse>(() => getCostByProduct({ week, scope }), [week, scope, refreshToken]);
  const unattrQ = useLoad<UnattributedResponse>(() => getUnattributed({ week, scope, limit: 200 }), [week, scope, refreshToken]);
  const labelsQ = useLoad<LabelSuggestionsResponse>(() => getLabelSuggestions({ week, scope, limit: 200 }), [week, scope, refreshToken]);

  const keyRef = useRef(0);
  const newKey = useCallback(() => `r${++keyRef.current}`, []);
  const toDrafts = useCallback((rules: AttributionRule[]): EditorRule[] => rules.map((rule) => ({ key: newKey(), rule })), [newKey]);

  const [server, setServer] = useState<RulesResponse | null>(null);
  const [drafts, setDrafts] = useState<EditorRule[]>([]);
  const [allocate, setAllocate] = useState<AllocateMode>('none');
  const [aliasRows, setAliasRows] = useState<AliasRow[]>([]);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [serverErrors, setServerErrors] = useState<Record<number, string>>({});
  const [savedAt, setSavedAt] = useState<string | null>(null);
  const [banner, setBanner] = useState<BannerData | null>(null);

  const aliasParsed = useMemo(() => rowsToAliases(aliasRows), [aliasRows]);
  const dirty = useMemo(() => {
    if (!server) return false;
    return (
      !rulesEqual(
        server.rules,
        drafts.map((d) => d.rule),
      ) ||
      allocate !== server.allocate ||
      JSON.stringify(aliasParsed.aliases) !== JSON.stringify(Object.fromEntries(Object.entries(server.aliases).sort(([a], [b]) => a.localeCompare(b))))
    );
  }, [server, drafts, allocate, aliasParsed.aliases]);

  const adopt = useCallback(
    (res: RulesResponse) => {
      setServer(res);
      setDrafts(toDrafts(res.rules));
      setAllocate(res.allocate);
      setAliasRows(aliasesToRows(res.aliases));
      setServerErrors({});
      setSaveError(null);
    },
    [toDrafts],
  );

  // Adopt freshly fetched rules unless the user has unsaved edits (never clobber drafts).
  const dirtyRef = useRef(false);
  dirtyRef.current = dirty;
  useEffect(() => {
    if (!rulesQ.data) return;
    if (dirtyRef.current) {
      setServer(rulesQ.data);
      return;
    }
    adopt(rulesQ.data);
  }, [rulesQ.data, adopt]);

  // Unsaved-changes guard: browser unload. (Tab switches unmount the panel; the visible badge warns.)
  useEffect(() => {
    if (!dirty) return;
    const h = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', h);
    return () => window.removeEventListener('beforeunload', h);
  }, [dirty]);

  const reloadWeekData = useCallback(() => {
    coverageQ.reload();
    unattrQ.reload();
    labelsQ.reload();
  }, [coverageQ, unattrQ, labelsQ]);

  async function save() {
    for (const d of drafts) if (validateRule(d.rule).length > 0) return;
    if (aliasParsed.errors.length > 0) return;
    setSaving(true);
    setSaveError(null);
    setServerErrors({});
    try {
      const res = await putAttributionRules({ rules: drafts.map((d) => toWireRule(d.rule)), allocate, aliases: aliasParsed.aliases });
      adopt(res);
      setSavedAt(res.updated_at ?? new Date().toISOString());
      setBanner(null);
      reloadWeekData();
    } catch (err: unknown) {
      const e = toApiError(err);
      const idx = e.code === 'invalid_rule' ? parseInvalidRuleIndex(e.message) : null;
      if (idx !== null && idx >= 0 && idx < drafts.length) setServerErrors({ [idx]: e.message });
      setSaveError(errText(e) + ' Nothing was saved.');
    } finally {
      setSaving(false);
    }
  }

  function discard() {
    if (!server) return;
    if (dirty && typeof window !== 'undefined' && !window.confirm('Discard all unsaved rule changes?')) return;
    adopt(server);
  }

  // ---- Seed -------------------------------------------------------------------------
  const [seedPreview, setSeedPreview] = useState<SeedPreviewData | null>(null);
  const [seedLoading, setSeedLoading] = useState(false);
  const [seedApplying, setSeedApplying] = useState(false);
  const [seedError, setSeedError] = useState<string | null>(null);
  const [seedSelected, setSeedSelected] = useState<Set<number>>(new Set());
  const [sharedTokens, setSharedTokens] = useState('');
  const [seedMsg, setSeedMsg] = useState<string | null>(null);

  const tokens = () =>
    sharedTokens
      .split(',')
      .map((t) => t.trim())
      .filter((t) => t !== '');

  async function runPreview() {
    setSeedLoading(true);
    setSeedError(null);
    setSeedMsg(null);
    try {
      const pv = await previewSeed({ week, scope, ...(tokens().length > 0 ? { shared_tokens: tokens() } : {}) });
      setSeedPreview(pv);
      setSeedSelected(new Set(pv.proposed.map((_, i) => i)));
    } catch (err: unknown) {
      setSeedError(errText(toApiError(err)));
    } finally {
      setSeedLoading(false);
    }
  }

  async function runApply() {
    if (!seedPreview || !server) return;
    setSeedApplying(true);
    setSeedError(null);
    try {
      const all = seedSelected.size === seedPreview.proposed.length;
      let count: number;
      if (all) {
        const res = await applySeed({ week, scope, ...(tokens().length > 0 ? { shared_tokens: tokens() } : {}) });
        count = res.counts.rules;
      } else {
        // Subset: the apply endpoint is all-or-nothing, so replace the stored seed rules with the
        // chosen ones through the normal atomic PUT (user/proposed rules are kept untouched).
        const chosen = seedPreview.proposed.filter((_, i) => seedSelected.has(i)).map((r) => toWireRule(r));
        const kept = server.rules.filter((r) => r.source !== 'seed').map(toWireRule);
        await putAttributionRules({ rules: [...kept, ...chosen] });
        count = chosen.length;
      }
      setSeedMsg(`Applied ${count} seed rule${count === 1 ? '' : 's'}.`);
      setSeedPreview(null);
      rulesQ.reload();
      reloadWeekData();
    } catch (err: unknown) {
      setSeedError(errText(toApiError(err)));
    } finally {
      setSeedApplying(false);
    }
  }

  // ---- Unattributed -> editor ----------------------------------------------------------
  const editorRef = useRef<HTMLDivElement | null>(null);
  function scrollToEditor() {
    editorRef.current?.scrollIntoView?.({ behavior: 'smooth', block: 'start' });
  }
  function addDrafts(rules: AttributionRule[]) {
    setDrafts((cur) => [...cur, ...rules.map((rule) => ({ key: newKey(), rule }))]);
    scrollToEditor();
  }
  function createFromItem(item: UnattributedItem) {
    addDrafts([ruleFromUnattributed(item, nextPriority(drafts.map((d) => d.rule)))]);
  }

  // ---- Explain ------------------------------------------------------------------------------
  const [explainQuery, setExplainQuery] = useState('');
  const [explain, setExplain] = useState<ExplainState>({ loading: false, error: null, data: null });
  async function runExplain(resource: string) {
    const r = resource.trim();
    if (!r) return;
    setExplain({ loading: true, error: null, data: null });
    try {
      const data = await explainResource({ resource: r, week, scope });
      setExplain({ loading: false, error: null, data });
    } catch (err: unknown) {
      const e = toApiError(err);
      setExplain({ loading: false, error: e.status === 404 ? `${errText(e)} (no billed resource matches in ${week}.)` : errText(e), data: null });
    }
  }

  // ---- Label suggestions copy ---------------------------------------------------------------
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  async function copy(key: string, command: string) {
    try {
      await navigator.clipboard.writeText(command);
      setCopiedKey(key);
      setTimeout(() => setCopiedKey((k) => (k === key ? null : k)), 2000);
    } catch {
      setBanner({ key: 'clipboard', context: 'Copy', code: 'clipboard_unavailable', message: 'The browser blocked clipboard access.', hint: 'Select the command text and copy it manually.' });
    }
  }

  // ---- Propose ------------------------------------------------------------------------------------
  const [propose, setPropose] = useState<ProposeState>({ status: 'idle' });
  const [proposeSel, setProposeSel] = useState<Set<number>>(new Set());
  async function runPropose() {
    setPropose({ status: 'loading' });
    try {
      const data = await proposeRules({ week, scope });
      setPropose({ status: 'ready', data });
      setProposeSel(new Set(data.proposals.map((_, i) => i)));
    } catch (err: unknown) {
      const e = toApiError(err);
      if (e.code === 'proposer_not_configured') setPropose({ status: 'disabled', hint: e.hint });
      else setPropose({ status: 'error', code: e.code, message: e.message, hint: e.hint });
    }
  }
  function addProposals() {
    if (propose.status !== 'ready') return;
    const picked = propose.data.proposals.filter((_, i) => proposeSel.has(i)).map((p) => ({ ...p.rule, id: null, enabled: false, source: 'proposed' as const }));
    addDrafts(picked);
    setPropose({ status: 'idle' });
  }

  // ---- Errors from loads -> banners (rules only; per-section errors render in-section) -------------
  const rulesBanner = rulesQ.error ? bannerFromError('attribution-rules', 'Attribution rules', rulesQ.error) : null;

  const noSnapshot = coverageQ.data !== null && coverageQ.data.snapshot === null;
  const enabledCount = server ? server.rules.filter((r) => r.enabled).length : null;

  return (
    <div className={styles.panel} data-testid="attribution-panel">
      {banner ? <ErrorBanner banner={banner} onDismiss={() => setBanner(null)} /> : null}
      {rulesBanner ? <ErrorBanner banner={rulesBanner} /> : null}

      <CoverageHeader
        coverage={coverageQ.data?.coverage ?? null}
        loading={coverageQ.loading}
        error={coverageQ.error ? errText(coverageQ.error) : null}
        ruleCount={server ? server.rules.length : null}
        enabledCount={enabledCount}
        noSnapshot={noSnapshot}
        week={week}
      />

      <div ref={editorRef}>
        {server === null ? (
          rulesQ.loading ? (
            <div className={styles.skeleton} aria-busy="true" aria-label="loading rules" data-testid="rules-loading" />
          ) : (
            <div className={styles.notice} data-testid="rules-unavailable">
              The rules could not be loaded (see the error above). Editing is disabled until they load.
            </div>
          )
        ) : (
          <RulesEditor
            drafts={drafts}
            allocate={allocate}
            aliasRows={aliasRows}
            aliasErrors={aliasParsed.errors}
            serverErrors={serverErrors}
            dirty={dirty}
            saving={saving}
            saveError={saveError}
            savedAt={savedAt}
            newKey={newKey}
            onDrafts={setDrafts}
            onAllocate={setAllocate}
            onAliasRows={setAliasRows}
            onSave={() => void save()}
            onDiscard={discard}
          />
        )}
      </div>

      <SeedPreview
        preview={seedPreview}
        loading={seedLoading}
        applying={seedApplying}
        error={seedError}
        selected={seedSelected}
        sharedTokens={sharedTokens}
        applyBlocked={dirty ? 'Save or discard your rule edits before applying a seed.' : null}
        appliedMessage={seedMsg}
        onSharedTokens={setSharedTokens}
        onPreview={() => void runPreview()}
        onToggle={(i) =>
          setSeedSelected((cur) => {
            const n = new Set(cur);
            if (n.has(i)) n.delete(i);
            else n.add(i);
            return n;
          })
        }
        onToggleAll={(all) => setSeedSelected(all && seedPreview ? new Set(seedPreview.proposed.map((_, i) => i)) : new Set())}
        onApply={() => void runApply()}
      />

      <ProposeDialog
        state={propose}
        configured={server ? server.proposer.enabled : true}
        model={server?.proposer.model ?? null}
        selected={proposeSel}
        onPropose={() => void runPropose()}
        onToggle={(i) =>
          setProposeSel((cur) => {
            const n = new Set(cur);
            if (n.has(i)) n.delete(i);
            else n.add(i);
            return n;
          })
        }
        onAddToEditor={addProposals}
        onDismiss={() => setPropose({ status: 'idle' })}
      />

      <UnattributedList
        data={unattrQ.data}
        loading={unattrQ.loading}
        error={unattrQ.error ? errText(unattrQ.error) : null}
        onCreateRule={createFromItem}
        onExplain={(it) => {
          setExplainQuery(it.resource_name ?? it.key);
          void runExplain(it.resource_name ?? it.key);
        }}
        onExport={() => unattrQ.data && downloadTextFile(attributionCsvFilename('unattributed', week, scope), buildUnattributedCsv(unattrQ.data.items))}
      />

      <ExplainDrawer
        query={explainQuery}
        state={explain}
        onQuery={setExplainQuery}
        onSubmit={() => void runExplain(explainQuery)}
        onClose={() => setExplain({ loading: false, error: null, data: null })}
      />

      <LabelSuggestions
        data={labelsQ.data}
        loading={labelsQ.loading}
        error={labelsQ.error ? errText(labelsQ.error) : null}
        copiedKey={copiedKey}
        onCopy={(k, c) => void copy(k, c)}
        onExport={() => labelsQ.data && downloadTextFile(attributionCsvFilename('label-suggestions', week, scope), buildLabelSuggestionsCsv(labelsQ.data.items))}
      />
    </div>
  );
}
