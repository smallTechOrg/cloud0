// The Cost table renders whatever `columns` GET /cost/table returns. When that
// endpoint is unavailable (server error, or the route is not built yet) the UI
// still shows one "not fetched — Refresh" row per week so the page is never
// blank. Service columns depend on the deployment's billing data and on
// CLOUD0_COST_COLUMNS, which are unknown without the API, so only the
// structural columns are listed.

import type { Column } from './types';

export const FALLBACK_COLUMNS: readonly Column[] = [
  { key: 'week', label: 'Week', kind: 'week' },
  { key: 'total', label: 'Total GCP', kind: 'total' },
  { key: 'pct_change', label: '% change', kind: 'pct_change' },
];
