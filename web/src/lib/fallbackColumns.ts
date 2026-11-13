// The Cost table renders whatever `columns` GET /cost/table returns. When that
// endpoint is unavailable (server error, or the route is not built yet) the UI
// still shows one "not fetched — Refresh" row per week under the sheet's fixed
// header (spec/capabilities/cost-table.md → Column order) so the page is never
// blank. Extra service columns are unknown without the API, so none are listed.

import type { Column } from './types';

export const FALLBACK_COLUMNS: readonly Column[] = [
  { key: 'week', label: 'Week', kind: 'week' },
  { key: 'compute_engine', label: 'Compute Engine', kind: 'service', services: ['Compute Engine'] },
  { key: 'networking', label: 'Networking', kind: 'service', services: ['Networking'] },
  { key: 'cloud_sql', label: 'CloudSQL', kind: 'service', services: ['Cloud SQL'] },
  { key: 'gemini_api', label: 'Gemini API', kind: 'service', services: ['Gemini API', 'Generative Language API'] },
  { key: 'cloud_run', label: 'Cloud Run', kind: 'service', services: ['Cloud Run'] },
  { key: 'gcs', label: 'GCS', kind: 'service', services: ['Cloud Storage'] },
  { key: 'vm_manager', label: 'VM manager', kind: 'service', services: ['VM Manager'] },
  { key: 'cloud_build', label: 'Cloud Build', kind: 'service', services: ['Cloud Build'] },
  { key: 'cloud_logging', label: 'Cloud Logging', kind: 'service', services: ['Cloud Logging'] },
  { key: 'secret_manager', label: 'Secret Manager', kind: 'service', services: ['Secret Manager'] },
  { key: 'artifact_registry', label: 'Artifact Registry', kind: 'service', services: ['Artifact Registry'] },
  { key: 'total', label: 'Total GCP', kind: 'total' },
  { key: 'pct_change', label: '% change', kind: 'pct_change' },
  { key: 'stub:groq', label: 'AI Cost (Groq)', kind: 'stub', phase: 4 },
  { key: 'stub:claude', label: 'claude (usage)', kind: 'stub', phase: 4 },
];
