import { useCallback, useEffect, useRef, useState } from 'react';
import { applyAlert, previewAlert, toApiError } from '@/lib/api';
import {
  NOT_CONFIGURED_TEXT,
  STALE_TEXT,
  classifyAlertError,
  formatDiffValue,
  parseUnifiedDiff,
  prettyJson,
  type ErrorClass,
} from '@/lib/alertsFormat';
import { formatTimestamp } from '@/lib/format';
import type { AlertPreview, ApplyResult, PreviewRequest } from '@/lib/types';
import styles from './PreviewApplyDrawer.module.css';

export interface DrawerError {
  cls: ErrorClass;
  code: string;
  message: string;
  hint: string | null;
}

export type DrawerPhase = 'loading' | 'preview' | 'error' | 'applied';

export interface PreviewApplyViewProps {
  phase: DrawerPhase;
  preview: AlertPreview | null;
  result: ApplyResult | null;
  /** Preview-phase failure (phase "error") or apply-phase failure (phase "preview"). */
  error: DrawerError | null;
  applying: boolean;
  confirm: boolean;
  copied?: boolean;
  /** When set, Apply is disabled and this explains why (read-only mode). */
  applyDisabledReason?: string;
  title?: string;
  onConfirm: (v: boolean) => void;
  onApply: () => void;
  onRepreview: () => void;
  onClose: () => void;
  onCopy?: () => void;
}

export function toDrawerError(err: unknown): DrawerError {
  const e = toApiError(err);
  return { cls: classifyAlertError(e.code), code: e.code, message: e.message, hint: e.hint };
}

function ErrorBlock({ error, onRepreview }: { error: DrawerError; onRepreview: () => void }) {
  return (
    <div className={styles.error} role="alert" data-testid="preview-error" data-code={error.code} data-class={error.cls}>
      {error.cls === 'stale' ? (
        <>
          <strong>{STALE_TEXT}</strong>
          <div>
            <button type="button" className={styles.secondary} data-testid="preview-repreview" onClick={onRepreview}>
              Preview again
            </button>
          </div>
        </>
      ) : error.cls === 'not_configured' ? (
        <>
          <strong>Alerts write access is not configured.</strong>
          <div data-testid="preview-not-configured">{NOT_CONFIGURED_TEXT}</div>
          {error.hint ? <div className={styles.hint}>{error.hint}</div> : null}
        </>
      ) : error.cls === 'permission' ? (
        <>
          <strong>Permission denied by Google Cloud.</strong>
          <div>{error.message}</div>
          {error.hint ? (
            <div className={styles.hint} data-testid="preview-permission-hint">
              {error.hint}
            </div>
          ) : null}
        </>
      ) : (
        <>
          <div>
            <code>{error.code}</code> — {error.message}
          </div>
          {error.hint ? <div className={styles.hint}>{error.hint}</div> : null}
        </>
      )}
    </div>
  );
}

/** Pure presentation of the drawer; PreviewApplyDrawer owns the state machine. */
export function PreviewApplyView(props: PreviewApplyViewProps) {
  const { phase, preview, result, error, applying, confirm, copied = false, applyDisabledReason, title } = props;
  const stale = error?.cls === 'stale';
  const needsConfirm = preview?.requires_confirm === true;
  const applyBlocked = applying || stale || applyDisabledReason !== undefined || (needsConfirm && !confirm);
  const heading = title ?? (preview ? `${preview.action} ${preview.kind.replace('_', ' ')}` : 'Preview change');

  return (
    <>
      <header className={styles.header}>
        <h2 id="preview-drawer-title" className={styles.title}>
          {heading}
        </h2>
        <button type="button" className={styles.close} aria-label="Close" data-testid="preview-close" onClick={props.onClose} disabled={applying}>
          ×
        </button>
      </header>
      <div className={styles.body}>
        {phase === 'loading' ? (
          <div className={styles.skeleton} aria-busy="true" aria-label="loading preview" data-testid="preview-loading" />
        ) : null}

        {phase === 'error' && error ? <ErrorBlock error={error} onRepreview={props.onRepreview} /> : null}
        {phase === 'error' && error && error.cls !== 'stale' ? (
          <div>
            <button type="button" className={styles.secondary} data-testid="preview-retry" onClick={props.onRepreview}>
              Retry preview
            </button>
          </div>
        ) : null}

        {phase === 'applied' && result ? (
          <div className={styles.success} role="status" data-testid="preview-success">
            <strong>Applied.</strong> {result.action} {result.kind.replace('_', ' ')}
            {result.target ? <code className={styles.target}>{result.target}</code> : null}
            <div>
              Recorded in change history as <strong data-testid="preview-history-id">#{result.change_history_id}</strong> · {formatTimestamp(result.applied_at)}
            </div>
            {result.warnings.map((w) => (
              <div key={w} className={styles.warning}>
                {w}
              </div>
            ))}
          </div>
        ) : null}

        {phase === 'preview' && preview ? (
          <>
            <p className={styles.lede}>
              Nothing changes in Google Cloud until you press Apply. Target: <code>{preview.target || '(new resource)'}</code>
            </p>

            {preview.warnings.length > 0 ? (
              <ul className={styles.warnings} data-testid="preview-warnings">
                {preview.warnings.map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            ) : null}

            <section aria-label="Changed paths">
              <h3 className={styles.h3}>Changes</h3>
              {preview.diff.changes.length === 0 ? (
                <p className={styles.muted} data-testid="preview-no-changes">
                  No differences from the live resource.
                </p>
              ) : (
                <ul className={styles.changes} data-testid="preview-changes">
                  {preview.diff.changes.map((c) => (
                    <li key={`${c.op}:${c.path}`} data-op={c.op}>
                      <span className={`${styles.op} ${styles[`op_${c.op}`] ?? ''}`}>{c.op}</span> <code>{c.path}</code>
                      {c.op === 'change' ? (
                        <span className={styles.muted}>
                          {' '}
                          {formatDiffValue(c.before)} → {formatDiffValue(c.after)}
                        </span>
                      ) : c.op === 'add' ? (
                        <span className={styles.muted}> {formatDiffValue(c.after)}</span>
                      ) : (
                        <span className={styles.muted}> {formatDiffValue(c.before)}</span>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </section>

            <section aria-label="Diff">
              <h3 className={styles.h3}>Diff vs live</h3>
              <pre className={styles.pre} data-testid="preview-diff">
                {parseUnifiedDiff(preview.diff.unified).map((l, i) => (
                  <span key={i} className={`${styles.line} ${styles[`line_${l.kind}`] ?? ''}`}>
                    {l.text}
                    {'\n'}
                  </span>
                ))}
              </pre>
            </section>

            <section aria-label="Desired JSON">
              <h3 className={styles.h3}>Desired JSON</h3>
              <pre className={styles.pre} data-testid="preview-desired">
                {preview.desired === null ? 'null (resource will be removed)' : prettyJson(preview.desired)}
              </pre>
            </section>

            <section aria-label="gcloud command">
              <h3 className={styles.h3}>Equivalent gcloud command</h3>
              <pre className={styles.pre} data-testid="preview-gcloud">
                {preview.gcloud}
              </pre>
              <button type="button" className={styles.secondary} data-testid="preview-gcloud-copy" onClick={props.onCopy}>
                {copied ? 'Copied' : 'Copy command'}
              </button>
            </section>

            <p className={styles.muted}>Preview expires {formatTimestamp(preview.expires_at)}.</p>

            {needsConfirm ? (
              <label className={styles.confirm}>
                <input type="checkbox" data-testid="preview-confirm" checked={confirm} onChange={(e) => props.onConfirm(e.target.checked)} />
                <span>I understand this permanently deletes the resource.</span>
              </label>
            ) : null}

            {error ? <ErrorBlock error={error} onRepreview={props.onRepreview} /> : null}
            {applyDisabledReason ? (
              <p className={styles.muted} data-testid="preview-apply-disabled-reason">
                {applyDisabledReason}
              </p>
            ) : null}
          </>
        ) : null}
      </div>
      <footer className={styles.footer}>
        {phase === 'preview' && preview ? (
          <button
            type="button"
            className={needsConfirm ? styles.danger : styles.primary}
            data-testid="preview-apply"
            disabled={applyBlocked}
            onClick={props.onApply}
          >
            {applying ? 'Applying…' : needsConfirm ? 'Apply delete' : 'Apply'}
          </button>
        ) : null}
        <button type="button" className={styles.secondary} data-testid="preview-done" onClick={props.onClose} disabled={applying}>
          {phase === 'applied' ? 'Done' : 'Cancel'}
        </button>
      </footer>
    </>
  );
}

export interface PreviewApplyDrawerProps {
  request: PreviewRequest | null;
  onClose: () => void;
  onApplied: (r: ApplyResult) => void;
  /** Already-fetched preview (e.g. from adopt/preview); shown without a new call. */
  initialPreview?: AlertPreview | null;
  /** Replaces previewAlert(request) for (re)previews, e.g. adopt previews. */
  loadPreview?: () => Promise<AlertPreview>;
  applyDisabledReason?: string;
  title?: string;
}

const FOCUSABLE = 'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])';

/**
 * Preview → Apply drawer shared by Alerts, Incidents and Budgets. Never closes on
 * error; Esc/Close are user actions. Focus is trapped while open.
 */
export default function PreviewApplyDrawer({ request, onClose, onApplied, initialPreview = null, loadPreview, applyDisabledReason, title }: PreviewApplyDrawerProps) {
  const open = request !== null || initialPreview !== null || loadPreview !== undefined;
  const [phase, setPhase] = useState<DrawerPhase>('loading');
  const [preview, setPreview] = useState<AlertPreview | null>(null);
  const [result, setResult] = useState<ApplyResult | null>(null);
  const [error, setError] = useState<DrawerError | null>(null);
  const [applying, setApplying] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [copied, setCopied] = useState(false);
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const seq = useRef(0);

  const runPreview = useCallback(
    async (useInitial: boolean) => {
      const my = ++seq.current;
      setError(null);
      setResult(null);
      setConfirm(false);
      if (useInitial && initialPreview) {
        setPreview(initialPreview);
        setPhase('preview');
        return;
      }
      setPhase('loading');
      setPreview(null);
      try {
        const p = loadPreview ? await loadPreview() : request ? await previewAlert(request) : null;
        if (my !== seq.current) return;
        if (!p) throw new Error('nothing to preview');
        setPreview(p);
        setPhase('preview');
      } catch (err: unknown) {
        if (my !== seq.current) return;
        setError(toDrawerError(err));
        setPhase('error');
      }
    },
    [request, loadPreview, initialPreview],
  );

  // (Re)start whenever a different request/preview is opened.
  useEffect(() => {
    if (!open) return;
    void runPreview(true);
    return () => {
      seq.current++;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [request, initialPreview]);

  // Focus management: remember the opener, focus the dialog, restore on close.
  useEffect(() => {
    if (!open) return;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialogRef.current?.focus();
    return () => opener?.focus();
  }, [open]);

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') {
      e.stopPropagation();
      if (!applying) onClose();
      return;
    }
    if (e.key !== 'Tab') return;
    const nodes = Array.from(dialogRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? []);
    if (nodes.length === 0) {
      e.preventDefault();
      return;
    }
    const first = nodes[0]!;
    const last = nodes[nodes.length - 1]!;
    const active = document.activeElement;
    if (e.shiftKey && (active === first || active === dialogRef.current)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  };

  const apply = async () => {
    if (!preview) return;
    setApplying(true);
    setError(null);
    try {
      const res = await applyAlert({ preview_token: preview.preview_token, confirm_delete: preview.requires_confirm ? confirm : undefined });
      setResult(res);
      setPhase('applied');
      onApplied(res);
    } catch (err: unknown) {
      setError(toDrawerError(err));
    } finally {
      setApplying(false);
    }
  };

  const copy = () => {
    if (!preview) return;
    const done = () => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    };
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(preview.gcloud).then(done, () => undefined);
    }
  };

  if (!open) return null;

  return (
    <div className={styles.backdrop} data-testid="preview-backdrop">
      <div
        ref={dialogRef}
        className={styles.drawer}
        role="dialog"
        aria-modal="true"
        aria-labelledby="preview-drawer-title"
        tabIndex={-1}
        data-testid="preview-drawer"
        data-phase={phase}
        onKeyDown={onKeyDown}
      >
        <PreviewApplyView
          phase={phase}
          preview={preview}
          result={result}
          error={error}
          applying={applying}
          confirm={confirm}
          copied={copied}
          applyDisabledReason={applyDisabledReason}
          title={title}
          onConfirm={setConfirm}
          onApply={() => void apply()}
          onRepreview={() => void runPreview(false)}
          onClose={onClose}
          onCopy={copy}
        />
      </div>
    </div>
  );
}
