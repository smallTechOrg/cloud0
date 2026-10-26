import { Fragment, useEffect, useMemo, useState } from 'react';
import { getCostByEnvironment, getCostByProduct } from '@/lib/attributionApi';
import type { AllocateMode, BreakdownGroup, BreakdownResponse } from '@/lib/attributionTypes';
import { getRun, toApiError } from '@/lib/api';
import { downloadTextFile } from '@/lib/csv';
import { useCurrency } from '@/lib/currency';
import {
  breakdownCsvFilename,
  buildBreakdownCsv,
  dimensionTitle,
  groupHint,
  isFilteredView,
  isSpecialGroup,
  orderGroups,
  reconciliationText,
  serviceRows,
} from '@/lib/dimensionFormat';
import { formatInteger, formatMoney, formatSignedMoney, formatTimestamp } from '@/lib/format';
import type { Run } from '@/lib/types';
import { AllocateSelect } from './AllocateSelect';
import { EmptyState } from './EmptyState';
import { ErrorBanner, bannerFromError, type BannerData } from './ErrorBanner';
import { StatusBadge } from './StatusBadge';
import tableStyles from './CostTable.module.css';
import styles from './CostByDimension.module.css';

// ---- presentational view (also used by render tests) ---------------------------

export interface CostByDimensionViewProps {
  data: BreakdownResponse;
  allocate: AllocateMode;
  onAllocate: (mode: AllocateMode) => void;
  expanded: ReadonlySet<string>;
  onToggle: (key: string) => void;
  onExport: () => void;
  provenanceOpen: boolean;
  onProvenance: () => void;
  run: Run | null;
  runError: BannerData | null;
  loading?: boolean;
}

export function CostByDimensionView(p: CostByDimensionViewProps) {
  const currency = useCurrency();
  const { data } = p;
  const filtered = isFilteredView(data);
  const recon = reconciliationText(data.reconciliation, currency, filtered);
  const snap = data.snapshot;
  const groups = orderGroups(data.groups);
  const shared = data.groups.find((g) => g.kind === 'shared');
  const totalNet = data.total.cells.total?.net ?? 0;
  const dimWord = data.dimension === 'product' ? 'product' : 'environment';

  return (
    <div className={styles.panel} data-testid="dimension-panel" data-dimension={data.dimension}>
      <div className={styles.toolbar}>
        <AllocateSelect value={p.allocate} onChange={p.onAllocate} disabled={p.loading} name={`allocate-${data.dimension}`} />
        <div className={styles.actions}>
          {snap ? (
            <span className={styles.fresh} data-testid="dimension-freshness">
              Snapshot <StatusBadge status={snap.status_at_fetch} stale={snap.stale} testId="dimension-status" />
            </span>
          ) : null}
          <button type="button" className={styles.secondary} data-testid="dimension-provenance-toggle" disabled={!snap} aria-expanded={p.provenanceOpen} onClick={p.onProvenance}>
            Provenance
          </button>
          <button type="button" className={styles.secondary} data-testid="dimension-export" disabled={!snap} onClick={p.onExport}>
            Export CSV
          </button>
        </div>
      </div>

      {snap === null ? (
        <EmptyState title={`No snapshot for ${data.week}`} testId="dimension-empty">
          Refresh this week to fetch its billing rows, then the {dimWord} breakdown appears here.
        </EmptyState>
      ) : (
        <>
          <div className={styles.badgeRow}>
            <span className={recon.ok ? styles.reconOk : styles.reconBad} data-testid="dimension-recon" data-ok={recon.ok} title={recon.detail}>
              {recon.ok ? '✓ ' : '✗ '}
              {recon.label}
            </span>
            <span className={styles.muted}>{recon.detail}</span>
          </div>
          {!data.labels_captured ? (
            <p className={styles.warn} data-testid="dimension-no-labels">
              Labels were not captured for this week; refresh it to enable label rules.
            </p>
          ) : null}
          {data.notes.map((n) => (
            <p key={n} className={styles.muted} data-testid="dimension-note">
              {n}
            </p>
          ))}
          {data.groups.every((g) => g.kind !== data.dimension) ? (
            <EmptyState title={`No ${dimWord} groups yet`} testId="dimension-no-groups">
              Add rules in the Attribution tab first; until then everything is {data.dimension === 'product' ? '(unattributed)' : '(no environment)'}.
            </EmptyState>
          ) : null}
          {shared && p.allocate === 'none' && shared.net !== 0 ? (
            <p className={styles.sharedNote} data-testid="dimension-shared-note">
              Shared bucket: {formatMoney(shared.net, currency)} ({shared.share_pct.toFixed(1)} %) is infrastructure not tied to one {dimWord}. It is shown separately and not included in any {dimWord} row.
            </p>
          ) : null}
          <div className={tableStyles.scroller}>
            <table className={tableStyles.table} data-testid="dimension-table">
              <caption className={tableStyles.caption}>
                {dimensionTitle(data.dimension)} · {data.week} ({data.week_start} … {data.week_end}) · {currency}, net of credits · click a row to expand services.
              </caption>
              <thead>
                <tr>
                  <th scope="col" className={tableStyles.weekHead}>
                    {data.dimension === 'product' ? 'Product' : 'Environment'}
                  </th>
                  <th scope="col" className={tableStyles.numHead}>
                    Direct
                  </th>
                  <th scope="col" className={tableStyles.numHead}>
                    Allocated
                  </th>
                  <th scope="col" className={tableStyles.numHead}>
                    Net
                  </th>
                  <th scope="col" className={tableStyles.numHead}>
                    Share
                  </th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => (
                  <GroupRows key={g.key} data={data} group={g} allocate={p.allocate} open={p.expanded.has(g.key)} onToggle={p.onToggle} />
                ))}
              </tbody>
              <tfoot>
                <tr className={styles.totalRow} data-testid="dimension-total">
                  <th scope="row" className={tableStyles.weekCell}>
                    Total GCP
                  </th>
                  <td className={tableStyles.num} />
                  <td className={tableStyles.num} />
                  <td className={tableStyles.num} data-testid="dimension-total-net">
                    {formatMoney(totalNet, currency)}
                  </td>
                  <td className={tableStyles.num} />
                </tr>
              </tfoot>
            </table>
          </div>
          {p.provenanceOpen ? <DimensionProvenance data={data} run={p.run} error={p.runError} /> : null}
        </>
      )}
    </div>
  );
}

function GroupRows({ data, group, allocate, open, onToggle }: { data: BreakdownResponse; group: BreakdownGroup; allocate: AllocateMode; open: boolean; onToggle: (k: string) => void }) {
  const currency = useCurrency();
  const special = isSpecialGroup(group);
  const hint = groupHint(group, allocate);
  const services = serviceRows(data, group);
  const slug = group.key.replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '');
  return (
    <>
      <tr
        className={`${tableStyles.row} ${special ? styles.special : ''} ${group.kind === 'unattributed' ? styles.unattr : ''}`}
        data-testid={`dimension-row-${slug}`}
        data-kind={group.kind}
      >
        <th scope="row" className={tableStyles.weekCell}>
          <button
            type="button"
            className={styles.expander}
            aria-expanded={open}
            aria-label={`${open ? 'Collapse' : 'Expand'} ${group.label}`}
            disabled={services.length === 0}
            data-testid={`dimension-expand-${slug}`}
            onClick={() => onToggle(group.key)}
          >
            <span aria-hidden="true">{services.length === 0 ? '·' : open ? '▾' : '▸'}</span> {group.label}
          </button>
          {group.kind === 'shared' ? <span className={styles.kindTag}>shared</span> : null}
          {group.kind === 'unattributed' ? <span className={styles.kindTag}>unattributed</span> : null}
          {hint ? <div className={styles.hint}>{hint}</div> : null}
        </th>
        <td className={tableStyles.num}>{formatMoney(group.direct_net, currency)}</td>
        <td className={tableStyles.num}>{group.allocated_net === 0 ? '—' : formatSignedMoney(group.allocated_net, currency)}</td>
        <td className={`${tableStyles.num} ${styles.netCell}`} data-testid={`dimension-net-${slug}`}>
          {formatMoney(group.net, currency)}
        </td>
        <td className={tableStyles.num}>{group.share_pct.toFixed(1)} %</td>
      </tr>
      {open
        ? services.map((s) => (
            <tr key={`${group.key}/${s.key}`} className={styles.serviceRow} data-testid={`dimension-service-${slug}-${s.key}`}>
              <th scope="row" className={styles.serviceName}>
                {s.label}
              </th>
              <td className={tableStyles.num} />
              <td className={tableStyles.num} />
              <td className={tableStyles.num}>{formatMoney(s.net, currency)}</td>
              <td className={tableStyles.num} />
            </tr>
          ))
        : null}
    </>
  );
}

function DimensionProvenance({ data, run, error }: { data: BreakdownResponse; run: Run | null; error: BannerData | null }) {
  const snap = data.snapshot;
  if (!snap) return null;
  const src = run?.sources.find((s) => s.source === 'bigquery_billing') ?? null;
  return (
    <section className={styles.provenance} aria-label="Breakdown provenance" data-testid="dimension-provenance">
      <p className={styles.muted}>
        Attribution is computed at read time from the stored snapshot rows; nothing here queries BigQuery.
      </p>
      <dl className={styles.kv}>
        <dt>Snapshot</dt>
        <dd>#{snap.id}</dd>
        <dt>Fetched</dt>
        <dd>{formatTimestamp(snap.fetched_at)}</dd>
        <dt>Status at fetch</dt>
        <dd>
          <StatusBadge status={snap.status_at_fetch} stale={snap.stale} />
        </dd>
        <dt>Run</dt>
        <dd>#{snap.run_id}</dd>
        <dt>Rows</dt>
        <dd>{formatInteger(snap.row_count)}</dd>
        <dt>Allocation</dt>
        <dd>{data.allocate}</dd>
        <dt>Generated</dt>
        <dd>{formatTimestamp(data.generated_at)}</dd>
      </dl>
      {error ? <ErrorBanner banner={error} /> : null}
      {!error && run === null ? <p className={styles.muted}>Loading query…</p> : null}
      {run !== null && src === null ? <p className={styles.muted}>Run #{run.id} recorded no weekly billing query.</p> : null}
      {src ? (
        <>
          <h4 className={styles.sub}>Source query (run #{run?.id})</h4>
          <pre className={styles.sql} data-testid="dimension-sql">
            {src.sql ?? '(not recorded)'}
          </pre>
        </>
      ) : null}
    </section>
  );
}

// ---- the panel -------------------------------------------------------------------

interface Props {
  week: string;
  scope: string;
  dimension: 'product' | 'environment';
  /** Bumped by the parent after any refresh; re-fetches. */
  refreshToken: number;
}

export default function CostByDimension({ week, scope, dimension, refreshToken }: Props) {
  const currency = useCurrency();
  const [allocate, setAllocate] = useState<AllocateMode | null>(null);
  const [data, setData] = useState<BreakdownResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [banner, setBanner] = useState<BannerData | null>(null);
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const [provOpen, setProvOpen] = useState(false);
  const [run, setRun] = useState<Run | null>(null);
  const [runError, setRunError] = useState<BannerData | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    const params = { week, scope, allocate: allocate ?? undefined };
    (dimension === 'product' ? getCostByProduct(params) : getCostByEnvironment(params))
      .then((res) => {
        if (cancelled) return;
        setData(res);
        setBanner(null);
      })
      .catch((err: unknown) => {
        if (!cancelled) setBanner(bannerFromError('dimension', dimensionTitle(dimension), toApiError(err)));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [week, scope, dimension, allocate, refreshToken]);

  const runId = provOpen ? (data?.snapshot?.run_id ?? null) : null;
  useEffect(() => {
    setRun(null);
    setRunError(null);
    if (runId === null) return;
    let cancelled = false;
    getRun(runId)
      .then((r) => {
        if (!cancelled) setRun(r);
      })
      .catch((err: unknown) => {
        if (!cancelled) setRunError(bannerFromError('dimension-run', 'Source query', toApiError(err)));
      });
    return () => {
      cancelled = true;
    };
  }, [runId]);

  const effective: AllocateMode = allocate ?? data?.allocate ?? 'none';
  const toggle = useMemo(
    () => (key: string) =>
      setExpanded((prev) => {
        const next = new Set(prev);
        if (next.has(key)) next.delete(key);
        else next.add(key);
        return next;
      }),
    [],
  );

  return (
    <Fragment>
      {banner ? <ErrorBanner banner={banner} onDismiss={() => setBanner(null)} /> : null}
      {data === null ? (
        loading ? (
          <div className={styles.skeleton} aria-busy="true" aria-label={`loading ${dimensionTitle(dimension)}`} data-testid="dimension-loading" />
        ) : (
          <EmptyState title={`${dimensionTitle(dimension)} unavailable`} testId="dimension-unavailable">
            The breakdown could not be loaded (see the banner above).
          </EmptyState>
        )
      ) : (
        <CostByDimensionView
          data={data}
          allocate={effective}
          onAllocate={setAllocate}
          expanded={expanded}
          onToggle={toggle}
          onExport={() => downloadTextFile(breakdownCsvFilename(data, currency), buildBreakdownCsv(data))}
          provenanceOpen={provOpen}
          onProvenance={() => setProvOpen((v) => !v)}
          run={run}
          runError={runError}
          loading={loading}
        />
      )}
    </Fragment>
  );
}
