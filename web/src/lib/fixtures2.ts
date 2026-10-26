// One realistic example response per Phase 2 endpoint (spec/api.md → "Phase 2
// additions"), with placeholder names, so frontend work can render before the
// API exists. Not used by production code paths.

import type {
  ErrorsTableResponse,
  LimitsResponse,
  MetricCell,
  MetricsTableResponse,
  MonthTableResponse,
  Provenance,
  RefreshResponse,
} from './types';

const prov = (source: string, query: string): Provenance => ({
  source,
  query,
  aggregation: '{"align":"ALIGN_MEAN","period":"604800s"}',
  window: { start: '2026-10-19T00:00:00Z', end: '2026-10-26T00:00:00Z' },
  fetched_at: '2026-10-26T09:05:00Z',
});

const vmProv = prov('monitoring_vm', 'metric.type="compute.googleapis.com/instance/cpu/utilization" AND metric.labels.instance_name="vm-a"');

const val = (value: number, unit: string, p: Provenance, limit_level: MetricCell['limit_level'] = null): MetricCell => ({
  value,
  unit,
  gap: null,
  hint: null,
  provenance: p,
  limit_level,
});

const gap = (gapText: string, hint: string | null, unit: string, p: Provenance): MetricCell => ({
  value: null,
  unit,
  gap: gapText,
  hint,
  provenance: p,
  limit_level: null,
});

const notCollected = (unit: string): MetricCell => gap('not collected', null, unit, prov('', ''));

export const metricsTableFixture: MetricsTableResponse = {
  week: '2026-W43',
  week_start: '2026-10-19',
  week_end: '2026-10-25',
  week_status: 'settling',
  scope: 'all',
  generated_at: '2026-10-26T09:00:00Z',
  snapshot: { id: 5, fetched_at: '2026-10-26T09:05:00Z', status_at_fetch: 'settling', run_id: 8, row_count: 5, stale: false },
  groups: [
    {
      group: 'vm',
      label: 'VM',
      columns: [
        { key: 'cpu_pct', label: 'CPU %', unit: '%' },
        { key: 'memory_pct', label: 'Memory %', unit: '%' },
        { key: 'disk_pct', label: 'Disk %', unit: '%' },
      ],
      rows: [
        {
          project: 'project-a',
          resource: 'vm-a',
          cells: {
            cpu_pct: val(12.5, '%', vmProv),
            memory_pct: gap('no agent', 'install the Ops Agent', '%', prov('monitoring_vm', 'metric.type="agent.googleapis.com/memory/percent_used"')),
            disk_pct: gap('no agent', 'install the Ops Agent', '%', prov('monitoring_vm', 'metric.type="agent.googleapis.com/disk/percent_used"')),
          },
        },
      ],
    },
    {
      group: 'cloudsql',
      label: 'Cloud SQL',
      columns: [
        { key: 'cpu_pct', label: 'CPU %', unit: '%' },
        { key: 'memory_pct', label: 'Memory %', unit: '%' },
        { key: 'disk_pct', label: 'Disk %', unit: '%' },
      ],
      rows: [],
    },
    {
      group: 'networking',
      label: 'Networking',
      columns: [
        { key: 'lb_hours', label: 'LB hours', unit: 'hours' },
        { key: 'outbound_gb', label: 'Outbound GB', unit: 'GB' },
        { key: 'inbound_gb', label: 'Inbound GB', unit: 'GB' },
      ],
      rows: [
        {
          project: 'project-a',
          resource: '',
          cells: {
            lb_hours: val(168, 'hours', prov('bigquery_usage', 'SELECT …')),
            outbound_gb: val(41.2, 'GB', prov('bigquery_usage', 'SELECT …'), 'near'),
            inbound_gb: notCollected('GB'),
          },
        },
      ],
    },
    {
      group: 'cloudrun',
      label: 'Cloud Run',
      columns: [
        { key: 'service_memory_gib_s', label: 'Memory (GiB-s)', unit: 'GiB-s' },
        { key: 'requests', label: 'Requests', unit: 'count' },
        { key: 'service_cpu_sec', label: 'CPU (vCPU-s)', unit: 'vCPU-s' },
        { key: 'egress_intercontinental_gb', label: 'Egress intercontinental GB', unit: 'GB' },
      ],
      rows: [
        {
          project: 'project-a',
          resource: 'svc-a',
          cells: {
            service_memory_gib_s: val(9200, 'GiB-s', prov('monitoring_cloudrun', 'metric.type="run.googleapis.com/container/memory/allocation_time"')),
            requests: val(15300, 'count', prov('monitoring_cloudrun', 'metric.type="run.googleapis.com/request_count"'), 'ok'),
            service_cpu_sec: val(4100, 'vCPU-s', prov('monitoring_cloudrun', 'metric.type="run.googleapis.com/container/cpu/allocation_time"')),
            egress_intercontinental_gb: notCollected('GB'),
          },
        },
      ],
    },
    {
      group: 'gcs',
      label: 'Cloud Storage',
      columns: [
        { key: 'class_a_ops', label: 'Class A ops', unit: 'count' },
        { key: 'class_b_ops', label: 'Class B ops', unit: 'count' },
        { key: 'download_apac_gb', label: 'Download APAC GB', unit: 'GB' },
        { key: 'sent_gb', label: 'Sent GB', unit: 'GB' },
        { key: 'storage_gb_month', label: 'Storage GB-month', unit: 'GB-month' },
      ],
      rows: [],
    },
    {
      group: 'logging',
      label: 'Cloud Logging',
      columns: [{ key: 'logging_gb', label: 'Ingested GB', unit: 'GB' }],
      rows: [
        {
          project: 'project-a',
          resource: '',
          cells: { logging_gb: gap('source failed', 'grant the service account roles/monitoring.viewer', 'GB', prov('monitoring_logging', 'metric.type="logging.googleapis.com/billing/bytes_ingested"')) },
        },
      ],
    },
    {
      group: 'errors',
      label: 'Errors',
      columns: [
        { key: 'c4xx', label: '4xx', unit: 'count' },
        { key: 'c5xx', label: '5xx', unit: 'count' },
      ],
      rows: [
        {
          project: 'project-a',
          resource: 'svc-a',
          cells: {
            c4xx: val(12, 'count', prov('errors_cloudrun', 'metric.type="run.googleapis.com/request_count"')),
            c5xx: val(0, 'count', prov('errors_cloudrun', 'metric.type="run.googleapis.com/request_count"')),
          },
        },
      ],
    },
  ],
};

const errProv = prov('errors_cloudrun', 'metric.type="run.googleapis.com/request_count" AND resource.labels.service_name="svc-a"');

export const errorsTableFixture: ErrorsTableResponse = {
  week: '2026-W43',
  scope: 'all',
  generated_at: '2026-10-26T09:00:00Z',
  snapshot: { id: 8, fetched_at: '2026-10-26T09:05:00Z', status_at_fetch: 'settling', run_id: 8, row_count: 4, stale: false },
  rows: [
    { source: 'cloudrun', project: 'project-a', service: 'svc-a', c2xx: 1000, c3xx: 0, c4xx: 12, c5xx: 1, groups: null, occurrences: null, gap: null, hint: null, provenance: errProv },
    { source: 'error_reporting', project: 'project-a', service: 'svc-a', c2xx: null, c3xx: null, c4xx: null, c5xx: null, groups: 3, occurrences: 40, gap: null, hint: null, provenance: prov('errors_error_reporting', 'groupStats serviceFilter.service=svc-a') },
    {
      source: 'load_balancer',
      project: 'project-a',
      service: '',
      c2xx: null,
      c3xx: null,
      c4xx: null,
      c5xx: null,
      groups: null,
      occurrences: null,
      gap: 'logging disabled',
      hint: 'HTTP(S) load-balancer backend logging is disabled on every backend service - enable logging on the backend service (sample rate >= 0.1) to see 4xx/5xx here',
      provenance: prov('errors_load_balancer', 'metric.type="loadbalancing.googleapis.com/https/request_count"'),
    },
    {
      source: 'nginx',
      project: 'project-a',
      service: 'vm-a',
      c2xx: null,
      c3xx: null,
      c4xx: null,
      c5xx: null,
      groups: null,
      occurrences: null,
      gap: 'not collected',
      hint: 'nginx access logs are not collected - add the Ops Agent nginx receiver',
      provenance: prov('errors_nginx', 'log entries count'),
    },
  ],
  totals: { c2xx: 1000, c3xx: 0, c4xx: 12, c5xx: 1, groups: 3, occurrences: 40 },
};

export const limitsFixture: LimitsResponse = {
  week: '2026-W43',
  month: '2026-10',
  month_start: '2026-10-01',
  month_end: '2026-10-31',
  warn_pct: 80,
  updated_at: '2026-10-26T09:00:00Z',
  generated_at: '2026-10-26T09:00:00Z',
  limits: [
    { id: 'logging_gb', service: 'Cloud Logging', label: 'Log ingestion', unit: 'GB', allowance: 50, default_allowance: 50, used: 21.5, projected: 28.3, pct: 56.6, level: 'ok', week_used: 5.1, stub: false, reason: null },
    { id: 'gcs_class_a_ops', service: 'Cloud Storage', label: 'Class A operations', unit: 'count', allowance: 5000, default_allowance: 5000, used: 3900, projected: 5130, pct: 102.6, level: 'over', week_used: 900, stub: false, reason: null },
    { id: 'cloudrun_egress_gb', service: 'Cloud Run', label: 'Egress', unit: 'GB', allowance: 1, default_allowance: 1, used: null, projected: null, pct: null, level: 'no data', week_used: null, stub: false, reason: null },
    { id: 'groq_llama4_scout_rpm', service: 'Groq', label: 'meta-llama/llama-4-scout-17b-16e-instruct requests/minute', unit: 'requests/min', allowance: 30, default_allowance: 30, used: null, projected: null, pct: null, level: 'stub', week_used: null, stub: true, reason: 'Groq usage arrives in Phase 4' },
  ],
};

const cellOf = (net: number, gross: number, credits: number, delta_abs: number | null, delta_pct: number | null, flagged = false) => ({
  net,
  gross,
  credits,
  delta_abs,
  delta_pct,
  flagged,
});

export const monthTableFixture: MonthTableResponse = {
  scope: 'all',
  from: '2026-09',
  to: '2026-10',
  generated_at: '2026-10-26T09:00:00Z',
  thresholds: { pct: 20, abs_usd: 50 },
  columns: [
    { key: 'month', label: 'Month', kind: 'month' },
    { key: 'compute_engine', label: 'Compute Engine', kind: 'service', services: ['Compute Engine'] },
    { key: 'networking', label: 'Networking', kind: 'service', services: ['Networking'] },
    { key: 'total', label: 'Total GCP', kind: 'total' },
    { key: 'pct_change', label: '% change', kind: 'pct_change' },
  ],
  rows: [
    {
      month: '2026-09',
      month_start: '2026-09-01',
      month_end: '2026-09-30',
      month_status: 'final',
      snapshot: { id: 3, fetched_at: '2026-10-26T09:06:00Z', status_at_fetch: 'final', run_id: 8, row_count: 28, stale: false },
      cells: {
        compute_engine: cellOf(1100, 1100, 0, null, null),
        networking: cellOf(300, 400, -100, null, null),
        total: cellOf(1400, 1500, -100, null, null),
      },
      pct_change: null,
      flagged: false,
      invoice_months: ['202609'],
    },
    {
      month: '2026-10',
      month_start: '2026-10-01',
      month_end: '2026-10-31',
      month_status: 'partial',
      snapshot: { id: 4, fetched_at: '2026-10-26T09:06:00Z', status_at_fetch: 'partial', run_id: 8, row_count: 31, stale: false },
      cells: {
        compute_engine: cellOf(780, 780, 0, -320, -29.09, true),
        networking: cellOf(32.4, 40, -7.6, -267.6, -89.2, true),
        total: cellOf(812.4, 820, -7.6, -587.6, -41.97, true),
      },
      pct_change: -41.97,
      flagged: true,
      invoice_months: ['202610'],
    },
  ],
  summary: { month: '2026-10', net: 812.4, target: 2500, projected: 2210.9, status: 'partial', has_data: true },
};

export const refreshAllFixture: RefreshResponse = {
  run: {
    id: 8,
    trigger: 'refresh_all',
    from: '2026-W18',
    to: '2026-W44',
    started_at: '2026-10-26T09:05:00Z',
    finished_at: '2026-10-26T09:05:21Z',
    duration_ms: 21000,
    status: 'partial',
    error: null,
    sources: [
      { id: 20, source: 'bigquery_billing', status: 'ok', duration_ms: 7900, job_id: 'job_abc123', bytes_processed: 41234567, bytes_billed: 41943040, cache_hit: false, rows_returned: 2345, error_code: null, error_message: null, fix_hint: null },
      { id: 21, source: 'monitoring_vm', status: 'failed', duration_ms: 310, job_id: null, bytes_processed: null, bytes_billed: null, cache_hit: null, rows_returned: null, error_code: 'permission_denied', error_message: 'permission denied', fix_hint: 'grant the service account roles/monitoring.viewer' },
    ],
  },
  snapshots: [{ id: 13, week: '2026-W43', status_at_fetch: 'settling', row_count: 2345, net_total: 2012.5 }],
  metric_snapshots: [{ id: 5, week: '2026-W43', status_at_fetch: 'settling', row_count: 14 }],
  month_snapshots: [{ id: 4, month: '2026-10', status_at_fetch: 'partial', row_count: 31, net_total: 812.4 }],
};
