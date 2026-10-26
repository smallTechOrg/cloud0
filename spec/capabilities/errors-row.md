# Capability: Errors row (Phase 2)

## What It Does
Fills the sheet's always-empty "Errors (4xx, 5xx)" row with real weekly counts per Cloud Run service and Error Reporting group counts, and shows load-balancer and nginx error sources as explicit gaps with the fix to enable them.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Week | `WeekID` | picker | yes |
| Cloud Run request counts | `run.googleapis.com/request_count` by `response_code_class`, per `service_name` | Monitoring | yes |
| Error groups | Error Reporting `groupStats` per service, week window | Error Reporting API | yes |
| LB request counts | `loadbalancing.googleapis.com/https/request_count` by `response_code_class` | Monitoring | when available |
| LB / nginx logs | Logging | Logging API | gap until enabled |

## Outputs
| Output | Type | Destination |
|---|---|---|
| `error_rows` (migration 0002) | store | `GET /api/v1/errors/table?week=` |
| Errors tab + Errors row inside the Metrics block | UI | |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| Cloud Monitoring | `ListTimeSeries` (sum over week) | source failed + hint |
| Error Reporting | `ListGroupStats` with `timeRange` = week, grouped by `serviceFilter.service` | source failed + hint (`roles/errorreporting.viewer`, enable `clouderrorreporting.googleapis.com`) |
| Cloud Logging | detect whether LB backend-service logging and nginx access logs exist (count of entries in window) | treated as a gap, not a failure |

## Business Rules
- Per Cloud Run service: 2xx, 3xx, 4xx, 5xx counts for the week; totals row.
- Per service: Error Reporting group count and total occurrences.
- LB row: counts when the metric has data; otherwise a gap card "HTTP(S) load-balancer backend logging is disabled on every backend service — enable logging on the backend service (sample rate ≥ 0.1) to see 4xx/5xx here".
- VM nginx row: gap card "nginx access logs are not collected — add the Ops Agent nginx receiver" until log entries appear.
- "Groq errors" row remains a stub until Phase 4 (vendor usage).
- Gaps are stored as rows with `gap_reason` and `fix_hint`, so the history shows when they were closed.

## Success Criteria
- [ ] Real APIs: for `CLOUD0_SMOKE_WEEK` at least one Cloud Run service has `4xx > 0` and numeric `5xx`; Error Reporting returns ≥ 0 groups without error; the LB row is a gap with the hint text containing "backend service".
- [ ] Sum over code classes equals `request_count` without the class grouping (± 0 — same source).
- [ ] e2e: Errors tab shows the per-service table and two gap cards with fix hints.
