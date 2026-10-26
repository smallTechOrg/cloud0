'use client';

import { useEffect, useState } from 'react';
import { ApiError, getConfig, getHealth, type AppConfig, type Health } from '@/lib/api';

type Connection =
  | { state: 'connecting' }
  | { state: 'connected'; health: Health; config: AppConfig }
  | { state: 'error'; message: string; hint: string | null };

const PLANNED_TABS: { label: string; phase: number }[] = [
  { label: 'Metrics', phase: 2 },
  { label: 'Errors', phase: 2 },
  { label: 'Attribution', phase: 3 },
  { label: 'Month', phase: 2 },
  { label: 'Budgets & Alerts', phase: 4 },
  { label: 'Vendors', phase: 4 },
  { label: 'Limits', phase: 2 },
  { label: 'Rightsizing', phase: 3 },
  { label: 'Targets', phase: 3 },
];

export default function Page() {
  const [conn, setConn] = useState<Connection>({ state: 'connecting' });

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [health, config] = await Promise.all([getHealth(), getConfig()]);
        if (!cancelled) setConn({ state: 'connected', health, config });
      } catch (err) {
        if (cancelled) return;
        if (err instanceof ApiError) {
          setConn({ state: 'error', message: `${err.code} — ${err.message}`, hint: err.hint });
        } else {
          setConn({
            state: 'error',
            message: 'cloud0 server unreachable at this origin',
            hint: `Expected the API at ${typeof window === 'undefined' ? '/' : window.location.origin}/api/v1 — is ./bin/cloud0 serve running?`,
          });
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <div className="shell">
      <header className="topbar">
        <h1 className="brand">cloud0</h1>
        <p className="tagline">Weekly GCP cost review · USD, net of credits</p>
        <StatusLine conn={conn} />
      </header>

      <div className="body">
        <nav className="navtabs" aria-label="Sections">
          <a href="#cost" className="tab tab-active" aria-current="page">
            Cost
          </a>
          {PLANNED_TABS.map((t) => (
            <span key={t.label} className="tab tab-stub" title={`Arrives in Phase ${t.phase}`}>
              {t.label} <span className="phase-badge">P{t.phase}</span>
            </span>
          ))}
        </nav>

        <main id="cost" className="panel">
          <section className="stub" data-testid="cost-placeholder" aria-labelledby="cost-heading">
            <span className="badge badge-stub">Not functional yet</span>
            <h2 id="cost-heading">Cost table arrives in Phase 1</h2>
            <p>
              This build is the scaffold: the Go server, the embedded UI shell and the database schema. Phase 1
              adds the week picker, project scope, Refresh from BigQuery, the Cost table in the sheet&apos;s
              column order, drill-down, provenance, CSV export, anomaly flags, run log and week notes.
            </p>
            <p className="muted">Nothing on this tab is interactive in the current build.</p>
          </section>
          <ConfigSummary conn={conn} />
        </main>
      </div>
    </div>
  );
}

function StatusLine({ conn }: { conn: Connection }) {
  if (conn.state === 'connecting') {
    return (
      <p className="status status-connecting" data-testid="api-status" role="status">
        Connecting to the cloud0 API…
      </p>
    );
  }
  if (conn.state === 'error') {
    return (
      <div className="status status-error" data-testid="api-status" role="alert">
        <strong>Not connected:</strong> {conn.message}
        {conn.hint ? <div className="hint">{conn.hint}</div> : null}
      </div>
    );
  }
  const { health } = conn;
  return (
    <p className="status status-ok" data-testid="api-status" role="status">
      <span className="dot" aria-hidden="true" /> connected · v{health.version} · db {health.db} · migrations{' '}
      {health.migrations} · ui {health.ui} · bigquery {health.bigquery ? health.bigquery.status : 'not checked (Phase 1)'}
    </p>
  );
}

function ConfigSummary({ conn }: { conn: Connection }) {
  if (conn.state !== 'connected') {
    return (
      <section className="card" data-testid="config-summary" aria-live="polite">
        <h3>Configuration</h3>
        <p className="muted">{conn.state === 'connecting' ? 'Loading /api/v1/config…' : 'Unavailable until the API is reachable.'}</p>
      </section>
    );
  }
  const c = conn.config;
  return (
    <section className="card" data-testid="config-summary">
      <h3>Configuration</h3>
      <dl className="kv">
        <dt>Billed projects</dt>
        <dd>{c.projects.length === 0 ? <em>none configured</em> : c.projects.join(', ')}</dd>
        <dt>Weeks shown</dt>
        <dd>
          {c.first_week} → {c.current_week}
        </dd>
        <dt>Default week</dt>
        <dd>{c.default_week} (last complete week)</dd>
        <dt>Settle window</dt>
        <dd>{c.settle_hours} h after a week ends before it is final</dd>
        <dt>Server time</dt>
        <dd>{c.now}</dd>
      </dl>
    </section>
  );
}
