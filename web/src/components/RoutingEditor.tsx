import { useEffect, useState } from 'react';
import { putRouting, toApiError } from '@/lib/api';
import { SEVERITIES, routingEqual, severityLabel, toggleName, validateRouting, verificationLabel } from '@/lib/alertsFormat';
import { formatTimestamp } from '@/lib/format';
import type { AlertSeverity, RoutingResponse } from '@/lib/types';
import { SeverityChip } from './TemplateCard';
import styles from './AlertsPanel.module.css';

export interface RoutingEditorProps {
  data: RoutingResponse;
  /** Called with the saved server state. */
  onSaved: (r: RoutingResponse) => void;
  /** Static-render hook. */
  initialError?: string | null;
}

export default function RoutingEditor({ data, onSaved, initialError = null }: RoutingEditorProps) {
  const [draft, setDraft] = useState<Record<AlertSeverity, string[]>>(data.routing);
  const [error, setError] = useState<string | null>(initialError);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setDraft(data.routing);
  }, [data.routing]);

  const dirty = !routingEqual(draft, data.routing);

  async function save() {
    const v = validateRouting(draft);
    if (!v.ok) {
      setError(v.error);
      return;
    }
    setSaving(true);
    try {
      const res = await putRouting({ routing: draft });
      setError(null);
      onSaved(res);
    } catch (err: unknown) {
      const e = toApiError(err);
      setError(`${e.code} — ${e.message}${e.hint ? ` ${e.hint}` : ''}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <section className={styles.section} aria-labelledby="routing-h" data-testid="routing-editor">
      <div className={styles.sectionHead}>
        <h3 id="routing-h" className={styles.h3}>
          Severity routing
        </h3>
        <span className={styles.muted}>
          {data.defaulted ? 'Defaults: every severity goes to the channel named “Alert”.' : `Saved ${data.updated_at ? formatTimestamp(data.updated_at) : ''}`}
        </span>
      </div>
      {data.channels.length === 0 ? (
        <p className={styles.muted} data-testid="routing-no-channels">
          No notification channels exist yet. Add one below, then route severities to it.
        </p>
      ) : (
        <div className={styles.routingGrid}>
          {SEVERITIES.map((sev) => (
            <fieldset key={sev} className={styles.fieldset} data-testid={`routing-${sev}`}>
              <legend>
                <SeverityChip severity={sev} />
              </legend>
              {data.channels.map((c) => (
                <label key={c.name} className={styles.fieldInline}>
                  <input
                    type="checkbox"
                    data-testid={`routing-${sev}-${c.display_name}`}
                    aria-label={`${severityLabel(sev)} → ${c.display_name}`}
                    checked={draft[sev].includes(c.name)}
                    onChange={() => {
                      setDraft((d) => ({ ...d, [sev]: toggleName(d[sev], c.name) }));
                      setError(null);
                    }}
                  />
                  <span>
                    {c.display_name} <span className={styles.muted}>({c.type}, {c.project})</span>{' '}
                    {c.type === 'email' && c.verification_status !== 'VERIFIED' ? (
                      <span className={styles.warnText} data-testid="routing-unverified">
                        {verificationLabel(c)}
                      </span>
                    ) : null}
                  </span>
                </label>
              ))}
            </fieldset>
          ))}
        </div>
      )}
      {data.warnings.map((w) => (
        <p key={w} className={styles.warnText} data-testid="routing-warning">
          {w}
        </p>
      ))}
      {error ? (
        <p className={styles.error} role="alert" data-testid="routing-error">
          {error}
        </p>
      ) : null}
      <div className={styles.cardFoot}>
        <button type="button" className={styles.primary} data-testid="routing-save" disabled={saving || !dirty || data.channels.length === 0} onClick={() => void save()}>
          {saving ? 'Saving…' : 'Save routing'}
        </button>
        <button type="button" className={styles.secondary} disabled={saving || !dirty} onClick={() => setDraft(data.routing)}>
          Reset
        </button>
      </div>
    </section>
  );
}
