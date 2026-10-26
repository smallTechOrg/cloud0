import { useCallback, useEffect, useState } from 'react';
import {
  getAlertsStatus,
  getChannels,
  getCoverage,
  getPolicies,
  getRouting,
  getTemplates,
  toApiError,
} from '@/lib/api';
import {
  NOT_CONFIGURED_TEXT,
  buildChannelDraft,
  channelWarningLines,
  creatableDescriptors,
  groupTemplates,
  installedPolicies,
  slackGuidance,
  verificationLabel,
} from '@/lib/alertsFormat';
import type {
  AlertChannel,
  AlertsStatus,
  ApplyResult,
  ChannelDescriptor,
  ChannelsResponse,
  CoverageResponse,
  PoliciesResponse,
  PreviewRequest,
  RoutingResponse,
  TemplatesResponse,
} from '@/lib/types';
import AdoptBanner, { type DrawerSpec } from './AdoptBanner';
import { EmptyState } from './EmptyState';
import { ErrorBanner, bannerFromError, type BannerData } from './ErrorBanner';
import PolicyTable from './PolicyTable';
import PreviewApplyDrawer from './PreviewApplyDrawer';
import RoutingEditor from './RoutingEditor';
import TemplateCard from './TemplateCard';
import styles from './AlertsPanel.module.css';

interface Source<T> {
  data: T | null;
  loading: boolean;
  banner: BannerData | null;
}

function useSource<T>(key: string, context: string, fetcher: () => Promise<T>, token: number): Source<T> & { set: (v: T) => void } {
  const [state, setState] = useState<Source<T>>({ data: null, loading: true, banner: null });
  useEffect(() => {
    let cancelled = false;
    setState((s) => ({ ...s, loading: true }));
    fetcher()
      .then((data) => {
        if (!cancelled) setState({ data, loading: false, banner: null });
      })
      .catch((err: unknown) => {
        if (!cancelled) setState((s) => ({ data: s.data, loading: false, banner: bannerFromError(key, context, toApiError(err)) }));
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token]);
  const set = useCallback((v: T) => setState({ data: v, loading: false, banner: null }), []);
  return { ...state, set };
}

export function StatusStrip({
  status,
  channels,
  policies,
}: {
  status: AlertsStatus | null;
  channels: AlertChannel[];
  policies: PoliciesResponse | null;
}) {
  const writable = status?.alerts_configured === true;
  const warnings = channelWarningLines(channels);
  return (
    <div className={styles.strip} data-testid="alerts-status">
      {status === null ? (
        <span className={styles.muted}>Checking alerts access…</span>
      ) : (
        <>
          <span className={`${styles.chip} ${writable ? styles.managed : styles.unmanaged}`} data-testid="alerts-mode" data-writable={writable}>
            {writable ? 'writable' : 'read-only'}
          </span>
          <span className={styles.muted}>
            {status.projects.length} project{status.projects.length === 1 ? '' : 's'}: {status.projects.join(', ') || 'none configured'}
          </span>
          {!writable ? (
            <span className={styles.warnText} data-testid="alerts-readonly-note">
              Read-only: {NOT_CONFIGURED_TEXT}
            </span>
          ) : null}
        </>
      )}
      {warnings.map((w) => (
        <span key={w} className={styles.warnText} data-testid="alerts-channel-warning">
          ⚠ {w}
        </span>
      ))}
      {(policies?.projects_failed ?? []).map((f) => (
        <span key={f.project} className={styles.warnText} data-testid="alerts-project-failed">
          ⚠ {f.project}: {f.code} — {f.hint ?? f.message}
        </span>
      ))}
    </div>
  );
}

export function ChannelsSection({
  data,
  onPreview,
}: {
  data: ChannelsResponse;
  onPreview: (req: PreviewRequest) => void;
}) {
  const creatable = creatableDescriptors(data.descriptors);
  const projects = Array.from(new Set(data.channels.map((c) => c.project)));
  const [adding, setAdding] = useState(false);
  const [type, setType] = useState<string>(creatable[0]?.type ?? '');
  const [project, setProject] = useState<string>(projects[0] ?? '');
  const [name, setName] = useState('');
  const [values, setValues] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);
  const descriptor: ChannelDescriptor | undefined = creatable.find((d) => d.type === type);
  const slack = slackGuidance(data.descriptors);

  function submit() {
    if (!descriptor) {
      setError('Pick a channel type.');
      return;
    }
    if (project === '') {
      setError('Project is required (no existing channel to infer it from; set CLOUD0_GCP_PROJECTS).');
      return;
    }
    const built = buildChannelDraft(project, descriptor, name, values);
    if (!built.ok) {
      setError(built.error);
      return;
    }
    setError(null);
    onPreview({ kind: 'channel', action: 'create', target: '', project, params: built.value });
  }

  return (
    <section className={styles.section} aria-labelledby="channels-h" data-testid="channels-section">
      <div className={styles.sectionHead}>
        <h3 id="channels-h" className={styles.h3}>
          Notification channels
        </h3>
        <button type="button" className={styles.secondary} data-testid="channel-add" onClick={() => setAdding((v) => !v)} aria-expanded={adding}>
          {adding ? 'Close' : 'Add channel'}
        </button>
      </div>
      {data.channels.length === 0 ? (
        <p className={styles.muted} data-testid="channels-empty">
          No notification channels. Alerts created now would notify nobody.
        </p>
      ) : (
        <ul className={styles.channelList} data-testid="channels-list">
          {data.channels.map((c) => (
            <li key={c.name} data-testid={`channel-${c.display_name}`}>
              <strong>{c.display_name}</strong> <span className={styles.muted}>{c.type} · {c.project}</span>
              {c.address ? <code className={styles.addr}>{c.address}</code> : null}
              <span className={c.type === 'email' && c.verification_status !== 'VERIFIED' ? styles.warnText : styles.muted}>{verificationLabel(c)}</span>
              {!c.enabled ? <span className={styles.muted}>disabled</span> : null}
              {c.warnings.map((w) => (
                <div key={w} className={styles.warnText}>
                  {w}
                </div>
              ))}
              <button
                type="button"
                className={`${styles.link} ${styles.dangerLink}`}
                onClick={() => onPreview({ kind: 'channel', action: 'delete', target: c.name, project: c.project })}
              >
                Delete…
              </button>
            </li>
          ))}
        </ul>
      )}
      {slack ? (
        <p className={styles.muted} data-testid="channels-slack-hint">
          Slack: {slack}
        </p>
      ) : null}
      {adding ? (
        <form
          className={styles.form}
          data-testid="channel-form"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          {creatable.length === 0 ? (
            <p className={styles.muted}>No channel types can be created from here; create one in Cloud Console → Monitoring → Notification channels.</p>
          ) : (
            <>
              <label className={styles.field}>
                <span>Type</span>
                <select
                  data-testid="channel-type"
                  value={type}
                  onChange={(e) => {
                    setType(e.target.value);
                    setValues({});
                  }}
                >
                  {creatable.map((d) => (
                    <option key={d.type} value={d.type}>
                      {d.display_name}
                    </option>
                  ))}
                </select>
              </label>
              {projects.length > 1 ? (
                <label className={styles.field}>
                  <span>Project</span>
                  <select value={project} onChange={(e) => setProject(e.target.value)}>
                    {projects.map((p) => (
                      <option key={p}>{p}</option>
                    ))}
                  </select>
                </label>
              ) : null}
              <label className={styles.field}>
                <span>Display name</span>
                <input data-testid="channel-name" value={name} onChange={(e) => setName(e.target.value)} />
              </label>
              {descriptor?.hint ? <p className={styles.muted}>{descriptor.hint}</p> : null}
              {descriptor?.labels.map((l) => (
                <label key={l.key} className={styles.field}>
                  <span>
                    {l.description || l.key}
                    {l.required ? ' *' : ''}
                    {l.sensitive ? <span className={styles.muted}> (secret: sent to Google only, never stored by cloud0)</span> : null}
                  </span>
                  <input
                    type={l.sensitive ? 'password' : 'text'}
                    autoComplete="off"
                    data-testid={`channel-label-${l.key}`}
                    value={values[l.key] ?? ''}
                    onChange={(e) => setValues((v) => ({ ...v, [l.key]: e.target.value }))}
                  />
                </label>
              ))}
              {error ? (
                <p className={styles.error} role="alert" data-testid="channel-form-error">
                  {error}
                </p>
              ) : null}
              <div className={styles.cardFoot}>
                <button type="submit" className={styles.primary} data-testid="channel-preview">
                  Preview change
                </button>
              </div>
            </>
          )}
        </form>
      ) : null}
    </section>
  );
}

export function CoverageHints({ data }: { data: CoverageResponse }) {
  const hints = data.rows.filter((r) => r.hint !== '');
  if (hints.length === 0) return null;
  return (
    <ul className={styles.skipped} data-testid="coverage-hints" aria-label="Coverage gaps">
      {hints.map((r) => (
        <li key={`${r.project}/${r.resource}`}>{r.hint}</li>
      ))}
    </ul>
  );
}

interface Props {
  /** Bumped by the parent after any refresh; re-fetches everything. */
  refreshToken: number;
}

export default function AlertsPanel({ refreshToken }: Props) {
  const [reload, setReload] = useState(0);
  const token = refreshToken * 1000 + reload;
  const status = useSource('alerts-status', 'Alerts status', getAlertsStatus, token);
  const channels = useSource('alerts-channels', 'Channels', getChannels, token);
  const policies = useSource('alerts-policies', 'Policies', getPolicies.bind(null, {}), token);
  const templates = useSource('alerts-templates', 'Templates', getTemplates, token);
  const routing = useSource('alerts-routing', 'Routing', getRouting, token);
  const coverage = useSource('alerts-coverage', 'Coverage', getCoverage, token);
  const [drawer, setDrawer] = useState<DrawerSpec | null>(null);
  const [dismissed, setDismissed] = useState<string[]>([]);

  const writable = status.data?.alerts_configured === true;
  const readOnlyReason = status.data !== null && !writable ? 'Read-only: CLOUD0_ALERTS_CREDENTIALS is not set, so changes can be previewed but not applied.' : undefined;

  const openPreview = useCallback((req: PreviewRequest) => setDrawer({ request: req }), []);
  const onApplied = useCallback(
    (r: ApplyResult) => {
      drawer?.onApplied?.(r);
      setReload((n) => n + 1);
    },
    [drawer],
  );

  const banners = [status, channels, policies, templates, routing, coverage]
    .map((s) => s.banner)
    .filter((b): b is BannerData => b !== null && !dismissed.includes(b.key));
  const policyList = policies.data?.policies ?? [];
  const projects = status.data?.projects ?? [];

  return (
    <div className={styles.panel} data-testid="alerts-panel">
      <StatusStrip status={status.data} channels={channels.data?.channels ?? []} policies={policies.data} />
      {banners.map((b) => (
        <ErrorBanner key={b.key} banner={b} onDismiss={(k) => setDismissed((d) => [...d, k])} />
      ))}
      {coverage.data ? <CoverageHints data={coverage.data} /> : null}

      {policies.data ? <AdoptBanner policies={policyList} onReview={setDrawer} /> : policies.loading ? <div className={styles.skeleton} aria-busy="true" aria-label="loading policies" /> : null}

      <section className={styles.section} aria-labelledby="templates-h" data-testid="templates-section">
        <h3 id="templates-h" className={styles.h3}>
          Templates
        </h3>
        {templates.data === null ? (
          templates.loading ? (
            <div className={styles.skeleton} aria-busy="true" aria-label="loading templates" />
          ) : (
            <EmptyState title="Templates unavailable" testId="templates-unavailable">
              The alert templates could not be loaded (see the banner above).
            </EmptyState>
          )
        ) : (
          groupTemplates((templates.data as TemplatesResponse).templates).map(({ group, templates: ts }) => (
            <div key={group} data-testid={`template-group-${group}`}>
              <h4 className={styles.groupTitle}>{group}</h4>
              <div className={styles.grid}>
                {ts.map((t) => (
                  <TemplateCard
                    key={t.id}
                    template={t}
                    installed={installedPolicies(policyList, t.id)}
                    projects={projects}
                    channels={channels.data?.channels ?? []}
                    onPreview={openPreview}
                  />
                ))}
              </div>
            </div>
          ))
        )}
      </section>

      {policies.data ? (
        <PolicyTable policies={policyList} onPreview={openPreview} />
      ) : policies.loading ? null : (
        <EmptyState title="Policies unavailable" testId="policies-unavailable">
          The alert policies could not be loaded (see the banner above).
        </EmptyState>
      )}

      {routing.data ? (
        <RoutingEditor data={routing.data as RoutingResponse} onSaved={(r) => routing.set(r)} />
      ) : routing.loading ? (
        <div className={styles.skeleton} aria-busy="true" aria-label="loading routing" />
      ) : null}

      {channels.data ? <ChannelsSection data={channels.data} onPreview={openPreview} /> : null}

      {drawer ? (
        <PreviewApplyDrawer
          request={drawer.request ?? null}
          initialPreview={drawer.initialPreview ?? null}
          loadPreview={drawer.loadPreview}
          title={drawer.title}
          applyDisabledReason={readOnlyReason}
          onClose={() => setDrawer(null)}
          onApplied={onApplied}
        />
      ) : null}
    </div>
  );
}

