// One realistic example response per Alerts endpoint (spec/api.md → "Alerts
// additions"), with placeholder names only (public repo). Not used by
// production code paths.

import type {
  AdoptPreview,
  AlertHistoryResponse,
  AlertPreview,
  AlertsStatus,
  ApplyResult,
  BudgetSuggestionsResponse,
  BudgetsResponse,
  ChangeHistoryResponse,
  ChannelsResponse,
  CoverageResponse,
  IncidentsResponse,
  PoliciesResponse,
  RoutingResponse,
  SnoozesResponse,
  TemplatesResponse,
  UptimeChecksResponse,
} from './types';

const chanName = 'projects/proj-a/notificationChannels/111';
const polName = 'projects/proj-a/alertPolicies/333';

export const alertsStatusFixture: AlertsStatus = {
  alerts_configured: true,
  billing_account_configured: true,
  projects: ['proj-a'],
  generated_at: '2026-10-27T09:00:00Z',
};

export const channelsFixture: ChannelsResponse = {
  channels: [
    {
      name: chanName,
      project: 'proj-a',
      type: 'email',
      display_name: 'Alert',
      enabled: true,
      verification_status: 'UNVERIFIED',
      address: 'a***@example.com',
      warnings: [
        'Email channel is not verified: notifications may not be delivered. Open the channel in the console and verify the address.',
      ],
    },
  ],
  descriptors: [
    {
      type: 'email',
      display_name: 'Email',
      creatable: true,
      hint: '',
      labels: [{ key: 'email_address', description: 'Email address', required: true, sensitive: false }],
    },
    {
      type: 'slack',
      display_name: 'Slack',
      creatable: false,
      hint: 'Slack channels need console OAuth: create it in Cloud Console → Monitoring → Notification channels, then pick it here.',
      labels: [],
    },
  ],
  generated_at: '2026-10-27T09:00:00Z',
};

export const policiesFixture: PoliciesResponse = {
  policies: [
    {
      name: 'projects/proj-a/alertPolicies/222',
      project: 'proj-a',
      display_name: 'Zero-Backend CPU Spike',
      enabled: true,
      severity: '',
      managed: false,
      template_id: '',
      instance: '',
      conditions: [
        {
          display_name: 'CPU > 80%',
          type: 'threshold',
          summary: 'CPU utilization > 80 % for 5 min on vm-a only',
          filter: 'metric.type="compute.googleapis.com/instance/cpu/utilization" resource.type="gce_instance"',
          comparison: 'COMPARISON_GT',
          threshold: 0.8,
          duration_seconds: 300,
        },
      ],
      channels: [{ name: chanName, display_name: 'Alert', type: 'email', verification_status: 'UNVERIFIED' }],
      adopt_shape: 'zero_cpu_spike',
      warnings: ['Covers one VM only; new VMs are not alerted.'],
      updated_at: null,
      raw_json: { name: 'projects/proj-a/alertPolicies/222', displayName: 'Zero-Backend CPU Spike' },
    },
  ],
  projects_failed: [],
  generated_at: '2026-10-27T09:00:00Z',
};

export const templatesFixture: TemplatesResponse = {
  templates: [
    {
      id: 'vm_cpu_high',
      kind: 'metric_threshold',
      title: 'VM CPU high',
      description: 'CPU utilisation above the threshold on any Compute Engine VM, including future ones.',
      severity: 'WARNING',
      default_enabled: true,
      param_specs: [
        { key: 'threshold', label: 'CPU %', type: 'number', unit: '%', min: 1, max: 100, options: [], help: '' },
        { key: 'duration_seconds', label: 'For', type: 'int', unit: 'seconds', min: 60, max: 86400, options: [], help: '' },
        { key: 'severity', label: 'Severity', type: 'enum', unit: '', min: null, max: null, options: ['CRITICAL', 'ERROR', 'WARNING'], help: '' },
      ],
      defaults: { threshold: 80, duration_seconds: 300, severity: 'WARNING', enabled: true },
      params: null,
      installed: [],
      covered_resources: [{ project: 'proj-a', resource_type: 'vm', name: 'vm-a', covered: true, note: '' }],
    },
  ],
  generated_at: '2026-10-27T09:00:00Z',
};

export const previewFixture: AlertPreview = {
  preview_token: 'pv_3f9a0c1e',
  expires_at: '2026-10-27T09:10:00Z',
  kind: 'policy',
  action: 'create',
  target: '',
  current: null,
  desired: {
    displayName: 'cloud0: VM CPU high',
    severity: 'WARNING',
    enabled: true,
    combiner: 'OR',
    userLabels: { 'managed-by': 'cloud0', 'cloud0-template': 'vm_cpu_high' },
    notificationChannels: [chanName],
  },
  diff: {
    changes: [{ path: 'displayName', op: 'add', before: null, after: 'cloud0: VM CPU high' }],
    unified: '+ displayName: cloud0: VM CPU high',
  },
  gcloud: 'gcloud alpha monitoring policies create --project=proj-a --policy-from-file=policy.json',
  warnings: [],
  requires_confirm: false,
};

export const applyFixture: ApplyResult = {
  ok: true,
  kind: 'policy',
  action: 'create',
  target: polName,
  resource: { name: polName, displayName: 'cloud0: VM CPU high' },
  change_history_id: 12,
  applied_at: '2026-10-27T09:02:00Z',
  warnings: [],
};

export const adoptFixture: AdoptPreview = {
  shape: 'zero_cpu_spike',
  replaces: ['projects/proj-a/alertPolicies/222'],
  previews: [previewFixture],
  follow_up: [
    {
      action: 'disable',
      kind: 'policy',
      target: 'projects/proj-a/alertPolicies/222',
      note: 'Disable the old policy only after the replacement is applied; cloud0 never deletes it for you.',
    },
  ],
  notes: ['Replacement covers every VM, not just vm-a.'],
};

export const uptimeChecksFixture: UptimeChecksResponse = {
  checks: [
    {
      name: 'projects/proj-a/uptimeCheckConfigs/abc',
      project: 'proj-a',
      display_name: 'api health',
      host: 'api.example.com',
      path: '/health',
      port: 443,
      use_ssl: true,
      period_seconds: 60,
      timeout_seconds: 10,
      content_match: '',
      managed: false,
      raw_json: { name: 'projects/proj-a/uptimeCheckConfigs/abc', displayName: 'api health' },
    },
  ],
  generated_at: '2026-10-27T09:00:00Z',
};

export const snoozesFixture: SnoozesResponse = {
  snoozes: [
    {
      name: 'projects/proj-a/snoozes/s1',
      project: 'proj-a',
      display_name: 'deploy window',
      policy_names: [polName],
      start_time: '2026-10-27T08:00:00Z',
      end_time: '2026-10-27T10:00:00Z',
      state: 'active',
    },
  ],
  generated_at: '2026-10-27T09:00:00Z',
};

export const incidentsFixture: IncidentsResponse = {
  incidents: [
    {
      id: '0abc',
      project: 'proj-a',
      policy_name: polName,
      policy_display_name: 'cloud0: VM CPU high',
      severity: 'WARNING',
      state: 'open',
      opened_at: '2026-10-27T07:40:00Z',
      closed_at: null,
      resource_type: 'gce_instance',
      resource_name: 'vm-a',
      summary: 'CPU utilization above 80 % for 5 min',
      url: 'https://console.cloud.google.com/monitoring/alerting/alerts/0abc?project=proj-a',
    },
  ],
  total: 1,
  projects_failed: [],
  generated_at: '2026-10-27T09:00:00Z',
};

export const alertHistoryFixture: AlertHistoryResponse = {
  week: '2026-W43',
  total_opened: 3,
  still_open: 1,
  rows: [
    {
      project: 'proj-a',
      policy_name: polName,
      policy_display_name: 'cloud0: VM CPU high',
      severity: 'WARNING',
      opened: 3,
      still_open: 1,
      resources: ['vm-a'],
      first_opened_at: '2026-10-20T03:10:00Z',
      last_opened_at: '2026-10-26T07:40:00Z',
    },
  ],
  projects_failed: [],
  generated_at: '2026-10-27T09:00:00Z',
};

export const coverageFixture: CoverageResponse = {
  rows: [
    {
      project: 'proj-a',
      resource_type: 'vm',
      resource: 'vm-a',
      signals: [
        { signal: 'cpu', covered: true, policies: [polName], reason: '' },
        { signal: 'memory', covered: false, policies: [], reason: 'Ops Agent not installed' },
        { signal: 'disk', covered: false, policies: [], reason: 'Ops Agent not installed' },
      ],
      hint: 'VM vm-a has CPU alert but no memory/disk alert coverage: install the Ops Agent.',
    },
  ],
  generated_at: '2026-10-27T09:00:00Z',
};

export const routingFixture: RoutingResponse = {
  routing: { CRITICAL: [chanName], ERROR: [chanName], WARNING: [chanName] },
  defaulted: true,
  updated_at: null,
  warnings: ['Channel "Alert" (email) is not verified.'],
  channels: channelsFixture.channels,
};

const defaultThresholds = [
  { percent: 50, basis: 'current_spend' as const },
  { percent: 80, basis: 'current_spend' as const },
  { percent: 100, basis: 'current_spend' as const },
  { percent: 100, basis: 'forecasted_spend' as const },
];

export const budgetsFixture: BudgetsResponse = {
  billing_account: 'BILLING_ACCOUNT_ID',
  currency: 'INR',
  budgets: [
    {
      name: 'billingAccounts/BILLING_ACCOUNT_ID/budgets/uuid-1',
      display_name: 'Example-budget',
      managed: false,
      amount_kind: 'specified',
      amount: 5000,
      currency: 'INR',
      projects: ['proj-a'],
      service_ids: [],
      thresholds: [
        { percent: 50, basis: 'current_spend' },
        { percent: 90, basis: 'current_spend' },
        { percent: 100, basis: 'current_spend' },
      ],
      channel_names: [],
      default_iam_recipients: true,
      adopt_shape: 'budget_account',
      warnings: ['No monitoring notification channels: alerts go to billing admins by email only.'],
      raw_json: { name: 'billingAccounts/BILLING_ACCOUNT_ID/budgets/uuid-1', displayName: 'Example-budget' },
    },
  ],
  generated_at: '2026-10-27T09:00:00Z',
};

export const budgetSuggestionsFixture: BudgetSuggestionsResponse = {
  currency: 'INR',
  suggestions: [
    {
      scope: 'account',
      service_id: '',
      service_name: 'All services',
      amount: 6480,
      basis: {
        method: 'snapshots_avg',
        weeks_used: 4,
        avg_weekly: 1260,
        multiplier: 1.2,
        explanation: '1.2 × average weekly net cost of the last 4 complete weeks (1,260.00) × 30/7',
      },
      draft: {
        display_name: 'cloud0-account-monthly',
        amount: 6480,
        projects: [],
        service_ids: [],
        thresholds: defaultThresholds,
        severity: 'ERROR',
      },
      exists: false,
    },
  ],
};

export const historyFixture: ChangeHistoryResponse = {
  entries: [
    {
      id: 12,
      at: '2026-10-27T09:02:00Z',
      actor: 'local-user',
      kind: 'policy',
      action: 'create',
      target: polName,
      before: null,
      after: { name: polName, displayName: 'cloud0: VM CPU high' },
      preview_hash: '3f9a0c1e',
    },
  ],
  limit: 50,
};
