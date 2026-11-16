# Capability: Run log

## What It Does
Records every refresh — when, what range, which sources succeeded or failed with error detail and a fix hint, duration, rows and bytes billed — and shows it in a drawer.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Run lifecycle events | start / per-source result / finish | `refresh-snapshot.md` | yes |
| BigQuery job statistics | job id, bytes processed/billed, cache hit, row count | billing source | when the job ran |
| Errors | provider error | billing source | on failure |

## Outputs
| Output | Type | Destination |
|---|---|---|
| `runs`, `run_sources` rows | store | `GET /api/v1/runs`, `GET /api/v1/runs/{id}` |
| Run list | JSON | RunLogDrawer |
| Log lines | slog JSON | stdout |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| SQLite | insert/update runs and sources | run marked `failed` with `error`; if the DB itself is unavailable the API returns 500 with the request id |

## Business Rules
- A run row is created before the first source call (`running`) and finalised after (`ok` / `failed`; `partial` becomes possible in Phase 2 with several sources).
- Each source gets its own `run_sources` row with the exact SQL/filter text and params, so the run log doubles as provenance.
- Error classification (`billing/hints.go`) — `error_code` → fix hint:

| `error_code` | Detect | `fix_hint` |
|---|---|---|
| `credentials_missing` | ADC lookup fails / key file unreadable | "Set GOOGLE_APPLICATION_CREDENTIALS in .env to the absolute path of the service-account key (outside the repo), or run on a host with an attached service account." |
| `permission_denied` | 403 `accessDenied` / `PERMISSION_DENIED` | "Grant roles/bigquery.jobUser on CLOUD0_BIGQUERY_PROJECT and BigQuery Data Viewer on the export dataset to the service account." |
| `api_disabled` | 403 with `SERVICE_DISABLED` / "has not been used in project" | "Enable bigquery.googleapis.com on CLOUD0_BIGQUERY_PROJECT." |
| `table_not_found` | 404 `notFound` for the table/dataset | "Check CLOUD0_BILLING_EXPORT_TABLE (project.dataset.table) — the detailed (resource-level) billing export table." |
| `quota_exceeded` | 403 `quotaExceeded` / `rateLimitExceeded` | "BigQuery quota exceeded; wait and retry. Refresh one week instead of all weeks." |
| `timeout` | context deadline (120 s) | "The query exceeded 120 s; retry, or refresh a single week." |
| `invalid_query` | 400 `invalidQuery` | "The generated SQL was rejected — this is a cloud0 bug; copy the SQL from the run log into an issue." |
| `unknown` | anything else | "See error_message; check server logs with the request id." |

- `error_message` is the provider's message trimmed to 2000 chars; never includes credential material.
- The drawer lists newest first with status chips; failed sources show the hint prominently; "Show SQL" fetches the detail endpoint.
- Every run emits `refresh.start`, `source.done` (per source) and `refresh.done` log lines with the fields in `spec/architecture.md → Logging`.

## Success Criteria
- [ ] API: after one successful refresh, `GET /runs` has a run with `status ok`, one source with `job_id`, `rows_returned > 0`, `bytes_billed` set; `GET /runs/{id}` adds `sql` and `params`.
- [ ] Unit (`hints.Classify`): a googleapi 403 with reason `accessDenied` → `permission_denied`; 404 `notFound` → `table_not_found`; `context.DeadlineExceeded` → `timeout`; 403 `SERVICE_DISABLED` → `api_disabled`.
- [ ] Integration: refresh against a non-existent table name → run `failed`, source `table_not_found` with the hint text containing `CLOUD0_BILLING_EXPORT_TABLE`.
- [ ] e2e: the drawer shows the run with duration and bytes billed after Refresh; expanding it shows the job id.
- [ ] Smoke: stdout contains one `"msg":"refresh.done"` line per refresh.
