import { useEffect, useMemo, useState } from 'react';
import { getRightsizing } from '@/lib/attributionApi';
import type { RightsizingResponse } from '@/lib/attributionTypes';
import { toApiError } from '@/lib/api';
import { downloadTextFile } from '@/lib/csv';
import { useCurrency } from '@/lib/currency';
import {
  buildRightsizingCsv,
  emptyHint,
  filterCandidates,
  footnoteText,
  headlineCaveat,
  headlineText,
  kindCounts,
  kindLabel,
  problemSources,
  projectsOf,
  rightsizingCsvFilename,
  sourceLabel,
  thresholdsHint,
  totalSaving,
} from '@/lib/rightsizingFormat';
import type { WeekID } from '@/lib/types';
import { CandidateCard } from './CandidateCard';
import { EmptyState } from './EmptyState';
import { ErrorBanner, bannerFromError, type BannerData } from './ErrorBanner';
import styles from './RightsizingPanel.module.css';

export interface RightsizingViewProps {
  data: RightsizingResponse;
  currency: string;
  kind: string;
  project: string;
  onKind: (k: string) => void;
  onProject: (p: string) => void;
  onExport: () => void;
}

/** Pure view of a loaded response (render-tested with react-dom/server). */
export function RightsizingView({ data, currency, kind, project, onKind, onProject, onExport }: RightsizingViewProps) {
  const all = data.candidates;
  const shown = filterCandidates(all, kind, project);
  const sum = totalSaving(shown);
  const problems = problemSources(data.sources);
  const kinds = kindCounts(all);
  const projects = projectsOf(all);
  const filtered = kind !== '' || project !== '';

  return (
    <div className={styles.view}>
      <section className={styles.headline} data-testid="rightsizing-headline">
        <div className={styles.headlineMain} data-testid="rightsizing-total">
          {headlineText(sum, currency)}
        </div>
        <p className={styles.caveat} data-testid="rightsizing-caveat">
          {headlineCaveat(sum)}
        </p>
        {filtered ? (
          <p className={styles.hint}>
            Filtered view: {sum.count} of {all.length} candidates.
          </p>
        ) : null}
      </section>

      {problems.length > 0 ? (
        <ul className={styles.sources} data-testid="rightsizing-sources" aria-label="Source status">
          {problems.map((s) => (
            <li key={s.source} className={s.status === 'failed' ? styles.sourceFailed : styles.sourceSkipped} data-testid="rightsizing-source" data-status={s.status}>
              <strong>{sourceLabel(s.source)}</strong> {s.status === 'failed' ? 'failed' : 'was skipped'}
              {s.error_code ? <code className={styles.code}>{s.error_code}</code> : null}
              {s.message ? <span> — {s.message}</span> : null}
              {s.fix_hint ? <div className={styles.fixHint}>{s.fix_hint}</div> : null}
              <div className={styles.hint}>Hints that need this source are missing from the list; everything below did load.</div>
            </li>
          ))}
        </ul>
      ) : null}

      <div className={styles.toolbar}>
        <label className={styles.field}>
          <span>Kind</span>
          <select className={styles.select} value={kind} onChange={(e) => onKind(e.target.value)} data-testid="rightsizing-kind">
            <option value="">All kinds ({all.length})</option>
            {kinds.map((k) => (
              <option key={k.kind} value={k.kind}>
                {kindLabel(k.kind)} ({k.count})
              </option>
            ))}
          </select>
        </label>
        <label className={styles.field}>
          <span>Project</span>
          <select className={styles.select} value={project} onChange={(e) => onProject(e.target.value)} data-testid="rightsizing-project">
            <option value="">All projects</option>
            {projects.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </label>
        <span className={styles.spacer} />
        <button type="button" className={styles.button} onClick={onExport} disabled={shown.length === 0} data-testid="rightsizing-csv">
          Export CSV
        </button>
      </div>

      <p className={styles.hint} data-testid="rightsizing-thresholds">
        {thresholdsHint(data.thresholds)} These thresholds are settings (<code>rightsizing.*</code>); there is no editor yet, so the defaults apply.
      </p>

      {all.length === 0 ? (
        <EmptyState title="Nothing to right-size with the data we have" testId="rightsizing-empty">
          {emptyHint(data.week, data.thresholds, true)}
        </EmptyState>
      ) : shown.length === 0 ? (
        <EmptyState title="No candidates match these filters" testId="rightsizing-filter-empty">
          Clear the kind or project filter to see all {all.length} candidates.
        </EmptyState>
      ) : (
        <div className={styles.cards} data-testid="rightsizing-list">
          {shown.map((c) => (
            <CandidateCard key={c.id} candidate={c} thresholds={data.thresholds} currency={currency} />
          ))}
        </div>
      )}

      <p className={styles.footnote} data-testid="rightsizing-footnote">
        {footnoteText(data.monthly_factor)} cloud0 never changes resources.
      </p>
    </div>
  );
}

interface Props {
  week: string;
  refreshToken: number;
}

export default function RightsizingPanel({ week, refreshToken }: Props) {
  const currency = useCurrency();
  const [data, setData] = useState<RightsizingResponse | null>(null);
  const [banner, setBanner] = useState<BannerData | null>(null);
  const [loading, setLoading] = useState(true);
  const [kind, setKind] = useState('');
  const [project, setProject] = useState('');

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getRightsizing({ week: week as WeekID })
      .then((r) => {
        if (cancelled) return;
        setData(r);
        setBanner(null);
        setLoading(false);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setBanner(bannerFromError('rightsizing', 'Rightsizing', toApiError(err)));
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [week, refreshToken]);

  // A filter that no longer exists in the new data must not hide everything.
  const kinds = useMemo(() => new Set(data?.candidates.map((c) => c.kind) ?? []), [data]);
  const projects = useMemo(() => new Set(data?.candidates.map((c) => c.project_id) ?? []), [data]);
  const effKind = kind !== '' && kinds.has(kind as never) ? kind : '';
  const effProject = project !== '' && projects.has(project) ? project : '';

  function exportCsv() {
    if (!data) return;
    downloadTextFile(rightsizingCsvFilename(data.scope, data.week, currency), buildRightsizingCsv(filterCandidates(data.candidates, effKind, effProject)));
  }

  return (
    <div className={styles.panel} data-testid="rightsizing-panel">
      <div className={styles.titleRow}>
        <h3 className={styles.title}>Rightsizing</h3>
        <span className={styles.hint}>Advisory hints for {week}. Nothing here changes your resources.</span>
      </div>
      {banner ? <ErrorBanner banner={banner} onDismiss={() => setBanner(null)} /> : null}
      {loading && !data ? (
        <p className={styles.hint} role="status" data-testid="rightsizing-loading">
          Loading rightsizing hints…
        </p>
      ) : null}
      {data ? (
        <div aria-busy={loading} className={loading ? styles.stale : undefined}>
          <RightsizingView data={data} currency={currency} kind={effKind} project={effProject} onKind={setKind} onProject={setProject} onExport={exportCsv} />
        </div>
      ) : null}
    </div>
  );
}
