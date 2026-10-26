# Capability: Refresh → snapshot

## What It Does
On demand, fetches net-of-credits billing for one ISO week (or every week first→current in one job) from the detailed billing export and stores the result as a new immutable snapshot, recording the run.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Mode | `week \| all` | user (Refresh / Refresh all weeks) | yes |
| Week | `WeekID` | picker | when mode = week |
| Billing export table | string | `CLOUD0_BILLING_EXPORT_TABLE` | yes |
| Job project | string | `CLOUD0_BIGQUERY_PROJECT` | yes |
| Partition margin, settle hours, first week | ints / WeekID | env (defaults) | no |
| Now | timestamp | server clock (injectable) | yes |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Snapshot(s) + rows | `snapshots`, `snapshot_rows` | store (`spec/data.md`) |
| Run + source record | `runs`, `run_sources` | store; `run-log.md`; `provenance.md` |
| Run result | JSON | `POST /api/v1/refresh` response → UI status / banner |
| Log lines | slog JSON | stdout (`refresh.start`, `source.done`, `refresh.done`) |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| BigQuery | one parameterised `SELECT` (`spec/architecture.md → BigQuery query`) over `[usage_start, usage_end)` with `DATE(_PARTITIONTIME)` bounded by the margin | classify → `error_code` + `fix_hint` into `run_sources`; `runs.status = failed`; no snapshot written; 200 response with the failed run; nothing else changes |
| SQLite | transactional insert of run_sources + snapshots + rows | `runs.status = failed`, `runs.error` set; 500 `internal_error` only if even that fails |

## Business Rules
- One snapshot per ISO week per refresh; a snapshot is never updated. `mode=all` produces one snapshot per week in `first_week…current_week` from a single query (grouped by `iso_year, iso_week`), all sharing one `run_source_id`; weeks with no rows get an empty snapshot (`row_count 0`, totals 0) so they read as "fetched, $0".
- Snapshots always cover all billed projects (no project filter in SQL); `project_id` is stored per row so any scope can be derived at read time.
- `net = gross + credits` per row; `gross = SUM(cost)`, `credits = SUM(credits.amount)` summed per line item (correlated subquery, never a join that multiplies cost).
- `status_at_fetch` = week status at the query start time.
- The SQL text and the four parameter values actually sent are stored verbatim on the `run_sources` row.
- At most one refresh runs at a time (process-wide mutex); a concurrent request gets `409 refresh_in_progress`.
- Request handling is synchronous; BigQuery call timeout 120 s → `error_code: timeout`.
- Weeks outside `first_week…current_week` → `400 week_out_of_range`; malformed → `400 invalid_week`.
- Scan hygiene: a single week must bill < 200 MB; `mode=all` < 2 GB. The run log shows `bytes_billed` so the user can see it.

## Success Criteria
- [ ] Real BigQuery: refreshing `CLOUD0_SMOKE_WEEK` yields `run.status == "ok"`, one snapshot with `row_count > 100`, `net_total` equal to the independent single-aggregate query over the same window within 0.005, and a row whose `service == "Compute Engine"` with `net > 0`.
- [ ] `run_sources.sql_text` contains `DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end` and `usage_start_time >= @usage_start`; `params_json` has exactly `part_start`, `part_end`, `usage_start`, `usage_end`, where `usage_start` is the week's Monday 00:00Z, `usage_end` the next Monday, and the partition dates are those dates ∓ `CLOUD0_PARTITION_MARGIN_DAYS`.
- [ ] Refreshing the same week twice creates two snapshots with increasing ids; `GET /cost/table` shows the newer one.
- [ ] `mode=all` creates exactly `(current_week − first_week + 1)` snapshots from one `run_sources` row with one `job_id`; `bytes_billed < 2 GB`.
- [ ] A refresh with `CLOUD0_BILLING_EXPORT_TABLE` pointing at a non-existent table (unit test overriding config) returns 200 with `run.status == "failed"`, `error_code == "table_not_found"`, a `fix_hint` naming `CLOUD0_BILLING_EXPORT_TABLE`, and no new snapshot.
- [ ] Two simultaneous `POST /refresh` → one 200, one 409.
- [ ] stdout contains `"msg":"refresh.done"` with `run_id`, `status`, `duration_ms`.
