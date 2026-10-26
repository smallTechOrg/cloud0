// Nav tabs (spec/ui.md → Nav tabs). Tabs are client-side via location.hash so
// the static export stays a single index.html. Tabs whose phase is greater than 1 render a labelled
// StubPanel; the description is the exact paragraph the stub shows.

export type TabId =
  | 'cost'
  | 'metrics'
  | 'errors'
  | 'attribution'
  | 'month'
  | 'budgets'
  | 'vendors'
  | 'limits'
  | 'rightsizing'
  | 'targets';

export interface TabDef {
  id: TabId;
  label: string;
  /** Phase in which the tab becomes real; 1 = real now. */
  phase: 1 | 2 | 3 | 4;
  description: string;
}

export const TABS: readonly TabDef[] = [
  { id: 'cost', label: 'Cost', phase: 1, description: 'Cost by service per ISO week, net of credits.' },
  {
    id: 'metrics',
    label: 'Metrics',
    phase: 1,
    description:
      "Per-week block from Cloud Monitoring: VM CPU/Memory/Disk % (with 'no agent' gaps), Cloud SQL CPU/Mem/Disk %, Networking LB hours and egress GB, Cloud Run requests / CPU-s / GiB-s / egress, Cloud Storage ops and GB-month, Cloud Logging GB.",
  },
  {
    id: 'errors',
    label: 'Errors',
    phase: 1,
    description:
      '4xx/5xx per Cloud Run service from request counts, Error Reporting groups per service, load-balancer and nginx rows shown as gaps with fix hints until their logging is enabled.',
  },
  {
    id: 'attribution',
    label: 'Attribution',
    phase: 3,
    description:
      'Product and environment breakdown from GCP labels, project and resource-name rules; rules editor; unattributed list; label suggestions.',
  },
  {
    id: 'month',
    label: 'Month',
    phase: 1,
    description: 'Calendar-month roll-up of net cost per service and total vs the monthly target.',
  },
  {
    id: 'budgets',
    label: 'Budgets & Alerts',
    phase: 4,
    description:
      'View/create/edit Billing budgets and Monitoring alert policies with notification channels; change history.',
  },
  {
    id: 'vendors',
    label: 'Vendors',
    phase: 4,
    description: 'Groq usage via API and Claude usage via CLI, feeding the two vendor columns of the Cost table.',
  },
  {
    id: 'limits',
    label: 'Limits',
    phase: 1,
    description:
      'Usage vs free-tier allowances (GCS, Cloud Run, Cloud Logging, disk throughput, Groq rate limits) with proximity warnings.',
  },
  {
    id: 'rightsizing',
    label: 'Rightsizing',
    phase: 3,
    description: 'VMs under ~15% CPU for weeks, idle disks and unused static IPs, with the evidence window.',
  },
  {
    id: 'targets',
    label: 'Targets',
    phase: 3,
    description: 'Per-product monthly targets and progress; GCP budgets mirrored as targets in Phase 4.',
  },
];

const TAB_IDS = new Set<string>(TABS.map((t) => t.id));

/** `#metrics` → `metrics`; anything unknown (or empty) → `cost`. */
export function tabFromHash(hash: string): TabId {
  const id = hash.replace(/^#/, '');
  return TAB_IDS.has(id) ? (id as TabId) : 'cost';
}

export function tabById(id: TabId): TabDef {
  return TABS.find((t) => t.id === id) ?? TABS[0]!;
}
