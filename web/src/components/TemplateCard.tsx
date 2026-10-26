import { useState } from 'react';
import {
  buildTemplateParams,
  coverageSummary,
  initialTemplateForm,
  paramsSummary,
  severityLabel,
  severityTone,
  skippedLabel,
  toggleName,
  uptimeInstanceDefaults,
  type TemplateForm,
} from '@/lib/alertsFormat';
import type { AlertChannel, AlertTemplate, ParamSpec, PolicyView, PreviewRequest } from '@/lib/types';
import styles from './AlertsPanel.module.css';

export function SeverityChip({ severity }: { severity: PolicyView['severity'] }) {
  return (
    <span className={`${styles.chip} ${styles[`sev_${severityTone(severity)}`] ?? ''}`} data-testid="severity-chip" data-severity={severity}>
      {severityLabel(severity)}
    </span>
  );
}

export interface TemplateCardProps {
  template: AlertTemplate;
  /** Managed policies created from this template. */
  installed: PolicyView[];
  projects: string[];
  channels: AlertChannel[];
  /** Opens the preview/apply drawer. */
  onPreview: (req: PreviewRequest) => void;
  /** Static-render hook: start with the form open. */
  initialForm?: { mode: 'install' } | { mode: 'edit'; policyName: string } | null;
}

function SpecField({ spec, value, onChange }: { spec: ParamSpec; value: string | boolean | string[] | undefined; onChange: (v: string | boolean) => void }) {
  const id = `tf-${spec.key}`;
  const str = typeof value === 'string' ? value : '';
  return (
    <label className={styles.field} htmlFor={id}>
      <span>
        {spec.label}
        {spec.unit ? <span className={styles.muted}> ({spec.unit})</span> : null}
      </span>
      {spec.type === 'enum' ? (
        <select id={id} data-testid={`param-${spec.key}`} value={str} onChange={(e) => onChange(e.target.value)}>
          {spec.options.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      ) : spec.type === 'bool' ? (
        <input id={id} data-testid={`param-${spec.key}`} type="checkbox" checked={value === true} onChange={(e) => onChange(e.target.checked)} />
      ) : (
        <input
          id={id}
          data-testid={`param-${spec.key}`}
          type="number"
          inputMode="decimal"
          step={spec.type === 'int' ? 1 : 'any'}
          min={spec.min ?? undefined}
          max={spec.max ?? undefined}
          value={str}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {spec.help ? <span className={styles.muted}>{spec.help}</span> : null}
    </label>
  );
}

export default function TemplateCard({ template: t, installed, projects, channels, onPreview, initialForm = null }: TemplateCardProps) {
  const [mode, setMode] = useState<TemplateCardProps['initialForm']>(initialForm);
  const [form, setForm] = useState<TemplateForm>(() => initialTemplateForm(t));
  const [project, setProject] = useState<string>(projects[0] ?? '');
  const [error, setError] = useState<string | null>(null);

  const cov = coverageSummary(t);
  const isUptime = t.id === 'uptime_down';
  const takenInstances = new Set(installed.map((p) => p.instance));
  const freeInstances = ['warning', 'critical'].filter((i) => !takenInstances.has(i));
  const canInstall = isUptime ? freeInstances.length > 0 : installed.length === 0;
  const current = t.params ?? t.defaults;
  const editingPolicy = mode?.mode === 'edit' ? installed.find((p) => p.name === mode.policyName) : undefined;
  const formProject = editingPolicy?.project ?? project;
  const projectChannels = channels.filter((c) => c.project === formProject);

  function setField(key: string, v: string | boolean | string[]) {
    setForm((f) => ({ ...f, [key]: v }));
    setError(null);
  }

  function openInstall() {
    const f = initialTemplateForm(t);
    if (isUptime) {
      const inst = freeInstances[0] ?? 'warning';
      const d = uptimeInstanceDefaults(inst);
      f.instance = inst;
      f.duration_seconds = String(d.duration_seconds);
      f.severity = d.severity;
    }
    setForm(f);
    setError(null);
    setMode({ mode: 'install' });
  }

  function openEdit(p: PolicyView) {
    const f = initialTemplateForm(t);
    if (isUptime) {
      const dur = p.conditions[0]?.duration_seconds;
      if (dur) f.duration_seconds = String(dur);
      if (p.severity) f.severity = p.severity;
      f.instance = p.instance;
    }
    f.enabled = p.enabled;
    setForm(f);
    setError(null);
    setMode({ mode: 'edit', policyName: p.name });
  }

  function onInstanceChange(inst: string) {
    const d = uptimeInstanceDefaults(inst);
    setForm((f) => ({ ...f, instance: inst, duration_seconds: String(d.duration_seconds), severity: d.severity }));
  }

  function submit() {
    const built = buildTemplateParams(t, form);
    if (!built.ok) {
      setError(built.error);
      return;
    }
    if (editingPolicy) {
      onPreview({ kind: 'policy', action: 'update', target: editingPolicy.name, project: editingPolicy.project, template_id: t.id, params: built.value });
    } else {
      if (project === '') {
        setError('No project is configured.');
        return;
      }
      onPreview({ kind: 'policy', action: 'create', target: '', project, template_id: t.id, params: built.value });
    }
  }

  const hasSeveritySpec = t.param_specs.some((s) => s.key === 'severity');

  return (
    <article className={styles.card} data-testid={`template-card-${t.id}`} data-installed={installed.length > 0 || undefined}>
      <header className={styles.cardHead}>
        <h4 className={styles.cardTitle}>{t.title}</h4>
        <SeverityChip severity={t.severity} />
        {installed.length > 0 ? <span className={`${styles.chip} ${styles.installed}`}>installed</span> : null}
        {!t.default_enabled ? <span className={`${styles.chip} ${styles.optIn}`}>opt-in, created disabled</span> : null}
      </header>
      <p className={styles.cardDesc}>{t.description}</p>
      <dl className={styles.params}>
        <dt>Default</dt>
        <dd data-testid={`template-default-${t.id}`}>{paramsSummary(t, t.defaults) || '—'}</dd>
        <dt>Current</dt>
        <dd data-testid={`template-current-${t.id}`}>{t.params ? paramsSummary(t, current) || '—' : 'not installed'}</dd>
        <dt>Covers</dt>
        <dd data-testid={`template-coverage-${t.id}`}>
          {cov.total === 0 ? 'no matching resources found' : `${cov.covered} of ${cov.total} resource${cov.total === 1 ? '' : 's'}`}
          {t.covered_resources.length > 0 ? (
            <span className={styles.muted}> ({t.covered_resources.filter((r) => r.covered).map((r) => r.name).join(', ') || 'none'})</span>
          ) : null}
        </dd>
      </dl>
      {cov.skipped.length > 0 ? (
        <ul className={styles.skipped} data-testid={`template-skipped-${t.id}`}>
          {cov.skipped.map((r) => (
            <li key={`${r.project}/${r.name}`}>{skippedLabel(r)}</li>
          ))}
        </ul>
      ) : null}

      {installed.length > 0 ? (
        <ul className={styles.installedList}>
          {installed.map((p) => (
            <li key={p.name} data-testid={`template-installed-${p.name}`}>
              <span>
                {p.display_name} <span className={styles.muted}>{p.enabled ? 'enabled' : 'disabled'}</span>
                {p.warnings.map((w) => (
                  <span key={w} className={styles.warnText}>
                    {' '}
                    · {w}
                  </span>
                ))}
              </span>
              <span className={styles.actions}>
                <button type="button" className={styles.link} data-testid={`template-edit-${p.name}`} onClick={() => openEdit(p)}>
                  Edit
                </button>
                <button
                  type="button"
                  className={styles.link}
                  data-testid={`template-toggle-${p.name}`}
                  onClick={() => onPreview({ kind: 'policy', action: p.enabled ? 'disable' : 'enable', target: p.name, project: p.project })}
                >
                  {p.enabled ? 'Disable' : 'Enable'}
                </button>
                <button
                  type="button"
                  className={`${styles.link} ${styles.dangerLink}`}
                  data-testid={`template-delete-${p.name}`}
                  onClick={() => onPreview({ kind: 'policy', action: 'delete', target: p.name, project: p.project })}
                >
                  Delete…
                </button>
              </span>
            </li>
          ))}
        </ul>
      ) : null}

      {mode === null ? (
        canInstall ? (
          <div className={styles.cardFoot}>
            <button type="button" className={styles.primary} data-testid={`template-install-${t.id}`} onClick={openInstall}>
              {isUptime && installed.length > 0 ? 'Add instance' : 'Install'}
            </button>
          </div>
        ) : null
      ) : (
        <form
          className={styles.form}
          data-testid={`template-form-${t.id}`}
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <h5 className={styles.formTitle}>{editingPolicy ? `Edit ${editingPolicy.display_name}` : `Install ${t.title}`}</h5>
          {!editingPolicy && projects.length > 1 ? (
            <label className={styles.field}>
              <span>Project</span>
              <select data-testid="param-project" value={project} onChange={(e) => setProject(e.target.value)}>
                {projects.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
          {isUptime ? (
            <label className={styles.field}>
              <span>Escalation step</span>
              <select
                data-testid="param-instance"
                value={typeof form.instance === 'string' ? form.instance : 'warning'}
                disabled={Boolean(editingPolicy)}
                onChange={(e) => onInstanceChange(e.target.value)}
              >
                {(editingPolicy ? ['warning', 'critical'] : freeInstances).map((i) => (
                  <option key={i} value={i}>
                    {i}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
          {t.param_specs.map((s) => (
            <SpecField key={s.key} spec={s} value={form[s.key]} onChange={(v) => setField(s.key, v)} />
          ))}
          {!hasSeveritySpec ? (
            <label className={styles.field}>
              <span>Severity</span>
              <select data-testid="param-severity" value={typeof form.severity === 'string' ? form.severity : t.severity} onChange={(e) => setField('severity', e.target.value)}>
                {['CRITICAL', 'ERROR', 'WARNING'].map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
          <label className={styles.fieldInline}>
            <input type="checkbox" data-testid="param-enabled" checked={form.enabled !== false} onChange={(e) => setField('enabled', e.target.checked)} />
            <span>Enabled</span>
          </label>
          <fieldset className={styles.fieldset}>
            <legend>Notify channels</legend>
            <p className={styles.muted}>Leave all unchecked to use the severity routing.</p>
            {projectChannels.length === 0 ? (
              <p className={styles.muted}>No notification channels in {formProject || 'this project'}.</p>
            ) : (
              projectChannels.map((c) => (
                <label key={c.name} className={styles.fieldInline}>
                  <input
                    type="checkbox"
                    data-testid={`param-channel-${c.display_name}`}
                    checked={Array.isArray(form.channel_override) && form.channel_override.includes(c.name)}
                    onChange={() => setField('channel_override', toggleName(Array.isArray(form.channel_override) ? form.channel_override : [], c.name))}
                  />
                  <span>
                    {c.display_name} <span className={styles.muted}>({c.type})</span>
                  </span>
                </label>
              ))
            )}
          </fieldset>
          {error ? (
            <p className={styles.error} role="alert" data-testid="template-form-error">
              {error}
            </p>
          ) : null}
          <div className={styles.cardFoot}>
            <button type="submit" className={styles.primary} data-testid="template-preview">
              Preview change
            </button>
            <button type="button" className={styles.secondary} onClick={() => setMode(null)}>
              Cancel
            </button>
          </div>
        </form>
      )}
    </article>
  );
}
