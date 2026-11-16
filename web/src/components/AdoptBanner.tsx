import { useState } from 'react';
import { adoptPreview, toApiError } from '@/lib/api';
import { ADOPT_INFO, adoptCandidates } from '@/lib/alertsFormat';
import type { AdoptPreview, AdoptShape, AlertPreview, ApplyResult, PolicyView, PreviewRequest } from '@/lib/types';
import styles from './AlertsPanel.module.css';

/** What the parent opens in PreviewApplyDrawer. */
export interface DrawerSpec {
  title?: string;
  request?: PreviewRequest;
  initialPreview?: AlertPreview;
  loadPreview?: () => Promise<AlertPreview>;
  onApplied?: (r: ApplyResult) => void;
}

type ShapeState =
  | { phase: 'idle' }
  | { phase: 'loading' }
  | { phase: 'error'; text: string }
  | { phase: 'ready'; data: AdoptPreview; applied: number[]; followed: string[] };

export interface AdoptBannerProps {
  policies: PolicyView[];
  onReview: (spec: DrawerSpec) => void;
  /** Static-render hook. */
  initialStates?: Partial<Record<AdoptShape, ShapeState>>;
}

function previewLabel(p: AlertPreview): string {
  const name = typeof p.desired?.displayName === 'string' ? p.desired.displayName : p.target;
  return `${p.action} ${p.kind.replace('_', ' ')} · ${name}`;
}

export default function AdoptBanner({ policies, onReview, initialStates = {} }: AdoptBannerProps) {
  const [states, setStates] = useState<Partial<Record<AdoptShape, ShapeState>>>(initialStates);
  const candidates = adoptCandidates(policies);

  function set(shape: AdoptShape, s: ShapeState) {
    setStates((cur) => ({ ...cur, [shape]: s }));
  }

  async function load(shape: AdoptShape, target: string | undefined) {
    set(shape, { phase: 'loading' });
    try {
      const data = await adoptPreview({ shape, target });
      set(shape, { phase: 'ready', data, applied: [], followed: [] });
    } catch (err: unknown) {
      const e = toApiError(err);
      set(shape, { phase: 'error', text: `${e.code} — ${e.message}${e.hint ? ` ${e.hint}` : ''}` });
    }
  }

  function review(shape: AdoptShape, target: string | undefined, data: AdoptPreview, i: number) {
    const p = data.previews[i];
    if (!p) return;
    onReview({
      title: `Adopt & fix: ${ADOPT_INFO[shape].title}`,
      initialPreview: p,
      // On "preview again" re-run the adopt preview and pick the same slot.
      loadPreview: async () => {
        const fresh = await adoptPreview({ shape, target });
        const next = fresh.previews[i];
        if (!next) throw new Error('adopt preview no longer has this step');
        return next;
      },
      onApplied: () =>
        setStates((cur) => {
          const s = cur[shape];
          if (!s || s.phase !== 'ready') return cur;
          return { ...cur, [shape]: { ...s, applied: [...s.applied, i] } };
        }),
    });
  }

  function followUp(shape: AdoptShape, f: AdoptPreview['follow_up'][number]) {
    onReview({
      title: 'Disable the replaced policy',
      request: { kind: f.kind, action: f.action, target: f.target },
      onApplied: () =>
        setStates((cur) => {
          const s = cur[shape];
          if (!s || s.phase !== 'ready') return cur;
          return { ...cur, [shape]: { ...s, followed: [...s.followed, f.target] } };
        }),
    });
  }

  return (
    <section className={styles.adopt} aria-labelledby="adopt-h" data-testid="adopt-banner">
      <h3 id="adopt-h" className={styles.h3}>
        Adopt &amp; fix existing alerts
      </h3>
      <p className={styles.muted}>
        These policies were not created by cloud0 and stay untouched until you act. Each fix creates a managed replacement you preview first; the old policy is only disabled afterwards, never deleted for you.
      </p>
      {candidates.length === 0 ? (
        <p className={styles.muted} data-testid="adopt-none">
          No known unmanaged alert shapes found.
        </p>
      ) : (
        <ul className={styles.adoptList}>
          {candidates.map(({ info, policies: ps }) => {
            const st: ShapeState = states[info.shape] ?? { phase: 'idle' };
            const target = ps.length === 1 ? ps[0]!.name : undefined;
            return (
              <li key={info.shape} className={styles.adoptItem} data-testid={`adopt-${info.shape}`}>
                <div>
                  <strong>{info.title}</strong>
                  <div className={styles.muted}>{ps.map((p) => p.display_name).join(' + ')}</div>
                  <p className={styles.adoptText}>{info.explain}</p>
                  {ps.flatMap((p) => p.warnings).map((w) => (
                    <div key={w} className={styles.warnText}>
                      {w}
                    </div>
                  ))}
                </div>
                {st.phase === 'idle' || st.phase === 'error' ? (
                  <div>
                    <button type="button" className={styles.primary} data-testid={`adopt-preview-${info.shape}`} onClick={() => void load(info.shape, target)}>
                      Preview fix
                    </button>
                    {st.phase === 'error' ? (
                      <p className={styles.error} role="alert" data-testid={`adopt-error-${info.shape}`}>
                        {st.text}
                      </p>
                    ) : null}
                  </div>
                ) : st.phase === 'loading' ? (
                  <span className={styles.muted} aria-busy="true">
                    Building preview…
                  </span>
                ) : (
                  <div className={styles.adoptSteps} data-testid={`adopt-steps-${info.shape}`}>
                    {st.data.notes.map((n) => (
                      <div key={n} className={styles.muted}>
                        {n}
                      </div>
                    ))}
                    <ol>
                      {st.data.previews.map((p, i) => (
                        <li key={p.preview_token}>
                          <span>{previewLabel(p)}</span>{' '}
                          {st.applied.includes(i) ? (
                            <span className={styles.okText}>applied</span>
                          ) : (
                            <button type="button" className={styles.secondary} data-testid={`adopt-review-${info.shape}-${i}`} onClick={() => review(info.shape, target, st.data, i)}>
                              Review &amp; apply
                            </button>
                          )}
                        </li>
                      ))}
                    </ol>
                    {st.applied.length === st.data.previews.length
                      ? st.data.follow_up.map((f) => (
                          <div key={f.target}>
                            <span className={styles.muted}>{f.note}</span>{' '}
                            {st.followed.includes(f.target) ? (
                              <span className={styles.okText}>disabled</span>
                            ) : (
                              <button type="button" className={styles.secondary} data-testid={`adopt-followup-${f.target}`} onClick={() => followUp(info.shape, f)}>
                                Disable old policy…
                              </button>
                            )}
                          </div>
                        ))
                      : null}
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}
      <p className={styles.muted} data-testid="adopt-budget-note">
        {ADOPT_INFO.budget_account.explain}
      </p>
    </section>
  );
}
