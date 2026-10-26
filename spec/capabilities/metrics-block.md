# Capability: Metrics block (Phase 2)

## What It Does
Reproduces the sheet's Metrics tab for a week: one row per resource with utilisation and usage figures from Cloud Monitoring, stored as a per-week metric snapshot, with explicit gaps where no data exists.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Week | `WeekID` | picker | yes |
| Projects | `CLOUD0_GCP_PROJECTS` | env | yes |
| Metric time series | Cloud Monitoring `ListTimeSeries` with aggregation over the week window | yes |

## Outputs
| Output | Type | Destination |
|---|---|---|
| `metric_snapshots`, `metric_rows` (migration 0002) | store | `GET /api/v1/metrics/table?week=&scope=` |
| Metrics tab block | table | UI |
| `run_sources` rows (`monitoring_<family>`) with the exact filter + aggregation JSON | store | run log, provenance |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| Cloud Monitoring | `ListTimeSeries` per metric family, `ALIGN_MEAN`/`ALIGN_SUM`/`ALIGN_DELTA` as appropriate, alignment = week | source `failed` with hint (permission → `roles/monitoring.viewer`; API disabled → enable `monitoring.googleapis.com`); other families still run; the block shows "source failed" for the affected rows |

## Business Rules
- Row groups and metrics (UTC week window):
  - VMs: CPU % `compute.googleapis.com/instance/cpu/utilization` (mean); Memory % `agent.googleapis.com/memory/percent_used` (mean, state=used); Disk % `agent.googleapis.com/disk/percent_used` (mean, state=used, root device), falling back to `agent.googleapis.com/disk/bytes_used` ÷ total when `percent_used` has no series. Instance identity read from `metric.labels.instance_name` for `compute.*` and from `resource.labels.instance_id` for `agent.*` (they differ — both parsed).
  - Cloud SQL: CPU and disk: `cloudsql.googleapis.com/database/{cpu,disk}/utilization` (mean). Memory: `database/memory/total_usage` ÷ `database/memory/quota` (the `memory/utilization` metric reads 100% on shared-core tiers).
  - Networking: LB hours (forwarding rules × hours in week, from billing usage), outbound / inbound GB from billing usage SKUs for the week (billing is the source of record for egress volume).
  - Cloud Run: `run.googleapis.com/container/memory/allocation_time` (GiB-s, sum) = **Memory total**, `request_count` (sum), `container/cpu/allocation_time` (vCPU-s, sum) = **CPU total**, intercontinental egress from billing usage. Per service, billing adds **Active** (`Services Memory|CPU (Request-based billing)`) and **Idle (min-instance)** (`Services Min Instance Memory|CPU (Request-based billing)`) columns, joined to the Monitoring row by service name. The Monitoring total is correct and equals active + idle: the sheet's ~24k memory figure was the active part only, while almost all idle memory comes from one service that keeps a minimum instance. The Cloud Run free tier (360,000 GiB-s/month memory, 180,000 vCPU-s CPU) is consumed by the total, so Limits evaluate on the total columns.
  - Every group ends in a **Total** row (sum of the resource rows for counts, GB, GiB-s, vCPU-s, ops, storage, hours; `—` for % utilisation columns), assembled server-side so API and CSV consumers get it (`is_total: true`).
  - Cloud Storage: `storage.googleapis.com/api/request_count` summed into Class A / Class B by the `method` label, `network/sent_bytes_count` (GB), `storage/total_bytes` (GB-month mean).
  - Cloud Logging: `logging.googleapis.com/billing/bytes_ingested` (GB, sum).
- A VM without the Ops Agent (no `agent.*` series in the window) shows **"no agent"** in Memory/Disk — never 0 — with the hint "install the Ops Agent".
- Values match the sheet's semantics (weekly mean % for utilisation, weekly sums for counts/volumes); each cell's provenance is the exact filter string + aggregation + window.
- Metrics refresh runs inside the same Refresh action as billing (one run, several sources).

## Success Criteria
- [ ] Real Monitoring: for `CLOUD0_SMOKE_WEEK`, every VM in scope has a numeric CPU % in (0, 100); VMs without the agent show `gap: "no agent"`; Cloud SQL has three numeric utilisations; Cloud Run has `request_count > 0` for at least one service; Logging GB ≥ 0.
- [ ] Every group with rows has a Total row; for the smoke week the Cloud Run Monitoring memory total reconciles with billing active + idle within 5%.
- [ ] Cell provenance shows the Monitoring filter text and window; the run log has one `monitoring_*` source per family with duration.
- [ ] A revoked `roles/monitoring.viewer` (unit test with injected 403) → source `permission_denied` with the role in the hint; the billing part of the run still succeeds (`run.status partial`).
- [ ] e2e: Metrics tab renders the block for the smoke week with the group headings in the sheet's order and "no agent" text present.
