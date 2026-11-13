import { useState } from 'react';
import { conditionText, filterPolicies, parseRawPolicy, policyConsoleUrl, prettyJson, type PolicyFilter } from '@/lib/alertsFormat';
import { formatTimestamp } from '@/lib/format';
import type { PolicyView, PreviewRequest } from '@/lib/types';
import { SeverityChip } from './TemplateCard';
import styles from './AlertsPanel.module.css';

export function ManagedBadge({ managed }: { managed: boolean }) {
  return (
    <span
      className={`${styles.chip} ${managed ? styles.managed : styles.unmanaged}`}
      data-testid="managed-badge"
      data-managed={managed}
      title={managed ? 'created from a cloud0 template' : 'not created by cloud0: untouched until you edit it'}
    >
      {managed ? 'managed' : 'unmanaged'}
    </span>
  );
}

export interface PolicyTableProps {
  policies: PolicyView[];
  onPreview: (req: PreviewRequest) => void;
  /** Static-render hook: start with the raw editor open for this policy. */
  initialRawFor?: string | null;
}

export default function PolicyTable({ policies, onPreview, initialRawFor = null }: PolicyTableProps) {
  const [filter, setFilter] = useState<PolicyFilter>('all');
  const [rawFor, setRawFor] = useState<string | null>(initialRawFor);
  const [rawText, setRawText] = useState<string>(() => {
    const p = policies.find((x) => x.name === initialRawFor);
    return p ? prettyJson(p.raw_json) : '';
  });
  const [rawError, setRawError] = useState<string | null>(null);

  const rows = filterPolicies(policies, filter);

  function openRaw(p: PolicyView) {
    setRawFor(p.name);
    setRawText(prettyJson(p.raw_json));
    setRawError(null);
  }

  function previewRaw(p: PolicyView) {
    const parsed = parseRawPolicy(rawText);
    if (!parsed.ok) {
      setRawError(parsed.error);
      return;
    }
    onPreview({ kind: 'policy', action: 'update', target: p.name, project: p.project, desired: parsed.value });
  }

  return (
    <section className={styles.section} aria-labelledby="policies-h" data-testid="policies-section">
      <div className={styles.sectionHead}>
        <h3 id="policies-h" className={styles.h3}>
          Policies
        </h3>
        <label className={styles.inlineLabel}>
          Show{' '}
          <select data-testid="policies-filter" value={filter} onChange={(e) => setFilter(e.target.value as PolicyFilter)}>
            <option value="all">all</option>
            <option value="managed">managed</option>
            <option value="unmanaged">unmanaged</option>
          </select>
        </label>
      </div>
      {rows.length === 0 ? (
        <p className={styles.muted} data-testid="policies-empty">
          {policies.length === 0 ? 'No alert policies in the configured projects yet. Install a template below.' : 'No policies match this filter.'}
        </p>
      ) : (
        <div className={styles.scroller}>
          <table className={styles.table} data-testid="policies-table">
            <thead>
              <tr>
                <th scope="col">Policy</th>
                <th scope="col">Origin</th>
                <th scope="col">Enabled</th>
                <th scope="col">Severity</th>
                <th scope="col">Condition</th>
                <th scope="col">Channels</th>
                <th scope="col">Actions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => (
                <PolicyRows
                  key={p.name}
                  p={p}
                  rawOpen={rawFor === p.name}
                  rawText={rawText}
                  rawError={rawFor === p.name ? rawError : null}
                  onOpenRaw={() => openRaw(p)}
                  onCloseRaw={() => setRawFor(null)}
                  onRawText={(t) => {
                    setRawText(t);
                    setRawError(null);
                  }}
                  onPreviewRaw={() => previewRaw(p)}
                  onPreview={onPreview}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

interface RowProps {
  p: PolicyView;
  rawOpen: boolean;
  rawText: string;
  rawError: string | null;
  onOpenRaw: () => void;
  onCloseRaw: () => void;
  onRawText: (t: string) => void;
  onPreviewRaw: () => void;
  onPreview: (req: PreviewRequest) => void;
}

function PolicyRows({ p, rawOpen, rawText, rawError, onOpenRaw, onCloseRaw, onRawText, onPreviewRaw, onPreview }: RowProps) {
  return (
    <>
      <tr data-testid={`policy-row-${p.name}`} data-managed={p.managed} data-enabled={p.enabled}>
        <th scope="row" className={styles.rowHead}>
          <div>{p.display_name}</div>
          <div className={styles.muted}>
            {p.project}
            {p.updated_at ? ` · updated ${formatTimestamp(p.updated_at)}` : ''}
          </div>
          {p.warnings.map((w) => (
            <div key={w} className={styles.warnText} data-testid="policy-warning">
              {w}
            </div>
          ))}
        </th>
        <td>
          <ManagedBadge managed={p.managed} />
        </td>
        <td data-testid="policy-enabled">{p.enabled ? 'yes' : 'no'}</td>
        <td>{p.severity === '' ? <span className={styles.muted}>unset</span> : <SeverityChip severity={p.severity} />}</td>
        <td className={styles.condCell}>{conditionText(p)}</td>
        <td>
          {p.channels.length === 0 ? (
            <span className={styles.warnText}>none</span>
          ) : (
            p.channels.map((c) => (
              <span key={c.name} className={styles.channelTag} title={c.verification_status}>
                {c.display_name}
              </span>
            ))
          )}
        </td>
        <td className={styles.actionsCell}>
          <a className={styles.linkA} href={policyConsoleUrl(p)} target="_blank" rel="noreferrer">
            Console
          </a>
          {!p.managed ? (
            <button type="button" className={styles.link} data-testid={`policy-edit-raw-${p.name}`} onClick={rawOpen ? onCloseRaw : onOpenRaw}>
              {rawOpen ? 'Close editor' : 'Edit raw'}
            </button>
          ) : null}
          <button
            type="button"
            className={styles.link}
            data-testid={`policy-toggle-${p.name}`}
            onClick={() => onPreview({ kind: 'policy', action: p.enabled ? 'disable' : 'enable', target: p.name, project: p.project })}
          >
            {p.enabled ? 'Disable' : 'Enable'}
          </button>
          <button
            type="button"
            className={`${styles.link} ${styles.dangerLink}`}
            data-testid={`policy-delete-${p.name}`}
            onClick={() => onPreview({ kind: 'policy', action: 'delete', target: p.name, project: p.project })}
          >
            Delete…
          </button>
        </td>
      </tr>
      {rawOpen ? (
        <tr data-testid={`policy-raw-row-${p.name}`}>
          <td colSpan={7}>
            <label className={styles.field}>
              <span>Raw AlertPolicy JSON (Cloud Monitoring REST shape). cloud0 does not template unmanaged policies; your edit is sent as-is to the preview.</span>
              <textarea
                className={styles.textarea}
                data-testid="policy-raw-text"
                spellCheck={false}
                rows={14}
                value={rawText}
                onChange={(e) => onRawText(e.target.value)}
              />
            </label>
            {rawError ? (
              <p className={styles.error} role="alert" data-testid="policy-raw-error">
                {rawError}
              </p>
            ) : null}
            <div className={styles.cardFoot}>
              <button type="button" className={styles.primary} data-testid="policy-raw-preview" onClick={onPreviewRaw}>
                Preview change
              </button>
              <button type="button" className={styles.secondary} onClick={onCloseRaw}>
                Cancel
              </button>
            </div>
          </td>
        </tr>
      ) : null}
    </>
  );
}
