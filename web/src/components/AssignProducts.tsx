import { useCallback, useEffect, useMemo, useState } from 'react';
import { toApiError } from '@/lib/api';
import { applyLabels, attributionExportUrl, getAttributionRules, getResources, previewLabels, putAttributionRules } from '@/lib/attributionApi';
import { toWireRule } from '@/lib/attributionFormat';
import type { AttributionRule, LabelItemIn, LabelPlan, ResourceGroup, ResourcesResponse } from '@/lib/attributionTypes';
import { useCurrency } from '@/lib/currency';
import { formatMoney } from '@/lib/format';
import styles from './AttributionPanel.module.css';
import assign from './AssignProducts.module.css';

/** Assignments are stored as user rules in this priority tier so they win over seeded guesses. */
export const ASSIGN_PRIORITY = 50;
export const ASSIGN_NOTE = 'Assigned in Assign products';

/** Lower-case a name to the label/rule character set ([a-z0-9_-]). */
export function labelValue(s: string): string {
  return s
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40);
}

export interface Decision {
  product: string;
  environment: string;
  shared: boolean;
}

/** Writes the decision for every group into the rule list (replacing an earlier assignment for the same match). */
export function applyDecision(rules: AttributionRule[], groups: ResourceGroup[], d: Decision): AttributionRule[] {
  const out = rules.slice();
  for (const g of groups) {
    const action: AttributionRule['action'] = {};
    if (d.shared) action.shared = true;
    else if (d.product) action.product = d.product;
    if (d.environment) action.environment = d.environment;
    const idx = out.findIndex(
      (r) => r.source === 'user' && r.priority === ASSIGN_PRIORITY && r.match.field === g.match.field && r.match.op === g.match.op && r.match.value === g.match.value,
    );
    const rule: AttributionRule = {
      id: idx >= 0 ? out[idx]!.id : null,
      priority: ASSIGN_PRIORITY,
      match: { field: g.match.field, op: g.match.op, value: g.match.value },
      action,
      enabled: true,
      source: 'user',
      note: ASSIGN_NOTE,
    };
    if (idx >= 0) out[idx] = rule;
    else out.push(rule);
  }
  return out;
}

/** The GCP label writes that make a decision authoritative at the source. */
export function labelItems(groups: ResourceGroup[], d: Decision): LabelItemIn[] {
  const labels: Record<string, string> = {};
  if (d.shared) labels.product = 'shared';
  else if (d.product) labels.product = d.product;
  if (d.environment) labels.env = d.environment;
  if (Object.keys(labels).length === 0) return [];
  const items: LabelItemIn[] = [];
  const seen = new Set<string>();
  for (const g of groups) {
    for (const t of g.targets) {
      if (!t.writable) continue;
      const k = `${t.project}|${t.type}|${t.location ?? ''}|${t.name}`;
      if (seen.has(k)) continue;
      seen.add(k);
      items.push({ target: { project: t.project, type: t.type, name: t.name, ...(t.location ? { location: t.location } : {}) }, labels });
    }
  }
  return items;
}

const SOURCE_TEXT: Record<string, string> = { label: 'GCP label', rule: 'cloud0 rule', shared: 'shared', none: 'unassigned' };
const SYSTEM_LABEL = /^(goog-|gke-|k8s-)/;

type PlanState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'ready' | 'applying' | 'done'; plan: LabelPlan; items: LabelItemIn[] }
  | { status: 'error'; message: string };

interface Props {
  week: string;
  scope: string;
  refreshToken: number;
  /** Called after rules or labels changed so coverage and breakdowns reload. */
  onChanged: () => void;
}

export default function AssignProducts({ week, scope, refreshToken, onChanged }: Props) {
  const currency = useCurrency();
  const [data, setData] = useState<ResourcesResponse | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [onlyUnassigned, setOnlyUnassigned] = useState(true);
  const [search, setSearch] = useState('');
  const [product, setProduct] = useState('');
  const [environment, setEnvironment] = useState('');
  const [shared, setShared] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [plan, setPlan] = useState<PlanState>({ status: 'idle' });

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getResources({ week, scope })
      .then((d) => {
        if (cancelled) return;
        setData(d);
        setLoadErr(null);
      })
      .catch((e: unknown) => {
        if (cancelled) return;
        const a = toApiError(e);
        setLoadErr(`${a.code} — ${a.message}${a.hint ? ` ${a.hint}` : ''}`);
      })
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [week, scope, refreshToken, tick]);

  const reload = useCallback(() => setTick((t) => t + 1), []);

  const groups = useMemo(() => {
    const q = search.trim().toLowerCase();
    return (data?.groups ?? []).filter((g) => {
      if (onlyUnassigned && g.source !== 'none') return false;
      if (q && !`${g.name} ${g.services.join(' ')} ${g.product} ${g.environment}`.toLowerCase().includes(q)) return false;
      return true;
    });
  }, [data, onlyUnassigned, search]);

  const unassignedCount = (data?.groups ?? []).filter((g) => g.source === 'none').length;
  const chosen = (data?.groups ?? []).filter((g) => selected.has(g.id));
  const decision: Decision = { product: labelValue(product), environment: labelValue(environment), shared };
  const decisionOk = shared || decision.product !== '' || decision.environment !== '';
  const items = labelItems(chosen, decision);
  const writer = data?.label_writer.enabled ?? false;

  function toggle(id: string) {
    setSelected((cur) => {
      const n = new Set(cur);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  }

  async function saveRules(gs: ResourceGroup[], d: Decision): Promise<boolean> {
    try {
      const cur = await getAttributionRules();
      const rules = applyDecision(cur.rules, gs, d);
      await putAttributionRules({ rules: rules.map(toWireRule), allocate: cur.allocate, aliases: cur.aliases });
      return true;
    } catch (e: unknown) {
      const a = toApiError(e);
      setMsg({ kind: 'err', text: `${a.code} — ${a.message}${a.hint ? ` ${a.hint}` : ''} Nothing was saved.` });
      return false;
    }
  }

  async function assignSelected(thenLabels: boolean) {
    if (chosen.length === 0 || !decisionOk) return;
    setBusy(true);
    setMsg(null);
    const d = decision;
    const gs = chosen;
    const ok = await saveRules(gs, d);
    setBusy(false);
    if (!ok) return;
    setMsg({ kind: 'ok', text: `Assigned ${gs.length} resource group${gs.length === 1 ? '' : 's'} (cloud0 rules, effective for every week immediately).` });
    setSelected(new Set());
    reload();
    onChanged();
    if (thenLabels) await openPreview(labelItems(gs, d));
  }

  async function acceptSuggestion(g: ResourceGroup) {
    if (!g.suggested) return;
    setBusy(true);
    setMsg(null);
    const ok = await saveRules([g], { product: labelValue(g.suggested.product), environment: labelValue(g.suggested.environment ?? ''), shared: false });
    setBusy(false);
    if (ok) {
      reload();
      onChanged();
    }
  }

  async function openPreview(its: LabelItemIn[]) {
    if (its.length === 0) {
      setPlan({ status: 'error', message: 'None of the selected resources has a GCP resource cloud0 can label (resource-less billing lines can only be assigned with cloud0 rules).' });
      return;
    }
    setPlan({ status: 'loading' });
    try {
      setPlan({ status: 'ready', plan: await previewLabels(its), items: its });
    } catch (e: unknown) {
      const a = toApiError(e);
      setPlan({ status: 'error', message: `${a.code} — ${a.message}${a.hint ? ` ${a.hint}` : ''}` });
    }
  }

  async function apply() {
    if (plan.status !== 'ready') return;
    const { items: its, plan: p } = plan;
    setPlan({ status: 'applying', plan: p, items: its });
    try {
      const res = await applyLabels(its, p.preview_hash);
      setPlan({ status: 'done', plan: res, items: its });
      onChanged();
    } catch (e: unknown) {
      const a = toApiError(e);
      if (a.code === 'preview_stale') await openPreview(its);
      else setPlan({ status: 'error', message: `${a.code} — ${a.message}${a.hint ? ` ${a.hint}` : ''}` });
    }
  }

  const products = data?.products ?? [];
  const envs = Array.from(new Set(['prod', 'staging', 'dev', ...(data?.environments ?? [])]));

  return (
    <section className={styles.section} data-testid="assign-section" aria-labelledby="assign-h">
      <div className={styles.sectionHead}>
        <h3 id="assign-h">Assign products</h3>
        <span className={styles.muted}>
          Tag each resource with a product and environment. Labels (<span className={styles.mono}>product</span>, <span className={styles.mono}>env</span>) are the source of truth in GCP;
          the cloud0 rule makes it count in every week straight away.
        </span>
        <span className={styles.spacer} />
        <a className={styles.btn} href={attributionExportUrl({ week, scope, format: 'csv' })} data-testid="assign-export-csv">
          Export CSV
        </a>
        <a className={styles.btn} href={attributionExportUrl({ week, scope, format: 'json' })} data-testid="assign-export-json">
          Export JSON
        </a>
      </div>

      {loadErr ? (
        <p className={styles.err} role="alert">
          {loadErr}
        </p>
      ) : null}
      {msg ? (
        <p className={msg.kind === 'ok' ? styles.okNotice : styles.err} role={msg.kind === 'ok' ? 'status' : 'alert'} data-testid="assign-msg">
          {msg.text}
        </p>
      ) : null}

      {data === null ? (
        loading ? (
          <div className={styles.skeleton} aria-busy="true" aria-label="loading resources" />
        ) : null
      ) : data.snapshot === null ? (
        <div className={styles.notice}>No snapshot for {data.week}. Refresh the week to fetch billing rows first.</div>
      ) : (
        <>
          <div className={assign.filters}>
            <label>
              <input type="checkbox" checked={onlyUnassigned} onChange={(e) => setOnlyUnassigned(e.target.checked)} data-testid="assign-only-unassigned" /> Unassigned only ({unassignedCount})
            </label>
            <input className={styles.input} type="search" placeholder="Filter by name, service, product…" aria-label="Filter resources" value={search} onChange={(e) => setSearch(e.target.value)} />
            <span className={styles.muted + ' ' + styles.small}>
              {groups.length} of {data.count} groups{data.truncated ? ' (list truncated)' : ''}
            </span>
          </div>

          <div className={assign.toolbar} data-testid="assign-toolbar">
            <strong>{chosen.length} selected</strong>
            <label>
              Product{' '}
              <input className={styles.input} list="assign-products" value={product} disabled={shared} onChange={(e) => setProduct(e.target.value)} placeholder="existing or new" data-testid="assign-product" />
              <datalist id="assign-products">
                {products.map((p) => (
                  <option key={p.product} value={p.product} />
                ))}
              </datalist>
            </label>
            <label>
              Env{' '}
              <input className={styles.input} list="assign-envs" value={environment} onChange={(e) => setEnvironment(e.target.value)} placeholder="optional" data-testid="assign-env" />
              <datalist id="assign-envs">
                {envs.map((e) => (
                  <option key={e} value={e} />
                ))}
              </datalist>
            </label>
            <label title="Cost that belongs to no single product (split by the allocation mode)">
              <input type="checkbox" checked={shared} onChange={(e) => setShared(e.target.checked)} /> Shared
            </label>
            <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={busy || chosen.length === 0 || !decisionOk} onClick={() => void assignSelected(false)} data-testid="assign-apply">
              Assign
            </button>
            <button
              type="button"
              className={styles.btn}
              disabled={busy || chosen.length === 0 || !decisionOk || !writer || items.length === 0}
              title={writer ? 'Assign, then preview the GCP labels to write' : (data.label_writer.hint ?? 'GCP label writes are not configured')}
              onClick={() => void assignSelected(true)}
              data-testid="assign-and-label"
            >
              Assign + write GCP labels…
            </button>
          </div>

          <div className={styles.scroller}>
            <table className={styles.table} data-testid="assign-table">
              <thead>
                <tr>
                  <th scope="col">
                    <input
                      type="checkbox"
                      aria-label="Select all shown"
                      checked={groups.length > 0 && groups.every((g) => selected.has(g.id))}
                      onChange={(e) => setSelected(e.target.checked ? new Set(groups.map((g) => g.id)) : new Set())}
                    />
                  </th>
                  <th scope="col">Resource</th>
                  <th scope="col" className={styles.num}>
                    Net
                  </th>
                  <th scope="col">Product</th>
                  <th scope="col">Env</th>
                  <th scope="col">GCP labels</th>
                  <th scope="col">Suggestion</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g, i) => (
                  <tr key={g.id} data-testid={`assign-row-${i}`} data-source={g.source}>
                    <td>
                      <input type="checkbox" checked={selected.has(g.id)} onChange={() => toggle(g.id)} aria-label={`Select ${g.name}`} />
                    </td>
                    <td>
                      <div className={styles.mono}>{g.name}</div>
                      <div className={styles.muted + ' ' + styles.small}>
                        {g.project_id} · {g.services.join(', ')}
                        {g.targets.length > 0 ? ` · ${g.targets.map((t) => t.type.replace(/_/g, ' ')).join(' + ')}` : ''}
                      </div>
                    </td>
                    <td className={styles.num}>
                      {formatMoney(g.net, currency)}
                      <div className={styles.muted + ' ' + styles.small}>{g.share_pct.toFixed(1)} %</div>
                    </td>
                    <td>
                      {g.shared ? <span className={styles.chip}>shared</span> : g.product ? <span className={styles.chip}>{g.product}</span> : <span className={styles.muted}>—</span>}
                      <div className={styles.muted + ' ' + styles.small}>{SOURCE_TEXT[g.source]}</div>
                    </td>
                    <td>{g.environment || <span className={styles.muted}>—</span>}</td>
                    <td>
                      {Object.entries(g.labels)
                        .filter(([k]) => !SYSTEM_LABEL.test(k))
                        .map(([k, v]) => (
                          <span key={k} className={styles.chip}>
                            {k}={v}
                          </span>
                        ))}
                    </td>
                    <td>
                      {g.suggested && g.source === 'none' ? (
                        <button type="button" className={styles.btn} disabled={busy} onClick={() => void acceptSuggestion(g)} title={`${g.suggested.confidence} confidence — ${g.suggested.basis}`}>
                          Use “{g.suggested.product || g.suggested.environment}”
                        </button>
                      ) : null}
                    </td>
                  </tr>
                ))}
                {groups.length === 0 ? (
                  <tr>
                    <td colSpan={7} className={styles.muted}>
                      {onlyUnassigned && unassignedCount === 0 ? 'Everything in this week has a product.' : 'No resources match the filter.'}
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </>
      )}

      <LabelPreview state={plan} onApply={() => void apply()} onClose={() => setPlan({ status: 'idle' })} />
    </section>
  );
}

// ---- GCP label preview / apply ----------------------------------------------------------

function LabelPreview({ state, onApply, onClose }: { state: PlanState; onApply: () => void; onClose: () => void }) {
  const [copied, setCopied] = useState<string | null>(null);
  if (state.status === 'idle') return null;

  async function copy(key: string, text: string) {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(key);
      setTimeout(() => setCopied((c) => (c === key ? null : c)), 1500);
    } catch {
      /* clipboard blocked: the text is visible to select */
    }
  }

  if (state.status === 'loading') return <div className={styles.notice} aria-busy="true">Reading the live labels from GCP…</div>;
  if (state.status === 'error')
    return (
      <div className={styles.err} role="alert" data-testid="label-error">
        {state.message}{' '}
        <button type="button" className={styles.btn} onClick={onClose}>
          Close
        </button>
      </div>
    );

  const { plan } = state;
  const done = state.status === 'done';
  const tf = plan.items.filter((i) => i.terraform).map((i) => i.terraform).join('\n\n');
  const cmds = plan.items.filter((i) => i.command).map((i) => i.command).join('\n');
  return (
    <div className={assign.preview} data-testid="label-preview">
      <div className={styles.sectionHead}>
        <h4>{done ? `Labels written: ${plan.applied} applied, ${plan.failed} failed` : `Preview — ${plan.changes} change${plan.changes === 1 ? '' : 's'} to write on GCP`}</h4>
        <span className={styles.spacer} />
        <button type="button" className={styles.btn} onClick={() => void copy('tf', tf)} disabled={!tf}>
          {copied === 'tf' ? 'Copied' : 'Copy Terraform'}
        </button>
        <button type="button" className={styles.btn} onClick={() => void copy('gc', cmds)} disabled={!cmds}>
          {copied === 'gc' ? 'Copied' : 'Copy gcloud'}
        </button>
        {!done ? (
          <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={state.status === 'applying' || plan.changes === 0} onClick={onApply} data-testid="label-apply">
            {state.status === 'applying' ? 'Writing…' : `Write ${plan.changes} label change${plan.changes === 1 ? '' : 's'} to GCP`}
          </button>
        ) : null}
        <button type="button" className={styles.btn} onClick={onClose}>
          {done ? 'Done' : 'Cancel'}
        </button>
      </div>
      <ul className={assign.items}>
        {plan.items.map((it) => (
          <li key={`${it.target.type}:${it.target.location ?? ''}:${it.target.name}`} data-testid="label-item">
            <div>
              <span className={styles.mono}>{it.target.name}</span> <span className={styles.muted + ' ' + styles.small}>{it.target.type.replace(/_/g, ' ')}{it.target.location ? ` · ${it.target.location}` : ''}</span>
              {it.applied ? <span className={styles.chip}>written</span> : it.noop && !it.error ? <span className={styles.chip}>already set</span> : null}
            </div>
            {it.error ? (
              <div className={styles.err}>
                {it.error.code}: {it.error.message}
                {it.error.hint ? <div className={styles.small}>{it.error.hint}</div> : null}
                {it.command ? <pre className={styles.command}>{it.command}</pre> : null}
                {it.terraform ? <pre className={styles.command}>{it.terraform}</pre> : null}
              </div>
            ) : (
              <>
                <div>
                  {it.diff.map((d) => (
                    <span key={d.key} className={styles.chip}>
                      {d.key}: {d.before === null ? '—' : d.before} → {d.after}
                    </span>
                  ))}
                </div>
                <details>
                  <summary className={styles.small}>gcloud / Terraform</summary>
                  {it.command ? <pre className={styles.command}>{it.command}</pre> : null}
                  <pre className={styles.command}>{it.terraform}</pre>
                  <div className={styles.muted + ' ' + styles.small}>{it.risk}</div>
                </details>
              </>
            )}
          </li>
        ))}
      </ul>
      <p className={styles.muted + ' ' + styles.small}>{plan.note} If this resource is managed by Terraform, put the labels in the Terraform too, or the next apply will remove them.</p>
    </div>
  );
}
