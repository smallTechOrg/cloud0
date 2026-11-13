# Capability: Cell provenance

## What It Does
Clicking any number reveals exactly how it was produced: the executed SQL and parameters, the BigQuery job statistics, fetch time, status at fetch vs now, snapshot id, the raw services the column aggregates, and the scope filter applied.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Snapshot id | int | clicked row | yes |
| Column definition | `columns[]` entry | table response | yes |
| Scope | `Scope` | picker | yes |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Provenance record | `GET /api/v1/snapshots/{id}` JSON | Cell Inspector → Provenance section |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| SQLite | read `snapshots` + its `run_sources` row | 500 `internal_error`; inspector shows the error |

## Business Rules
- `sql` is `run_sources.sql_text` byte-for-byte — the text that was executed, not a reconstruction; `params` are the values sent, by name.
- Shown fields: snapshot id, `fetched_at`, `status_at_fetch`, current `week_status`, `stale`, run id, job id, bytes processed, bytes billed, cache hit, duration, row count, totals, distinct projects in the snapshot, the formula line.
- Client-side additions (no extra endpoint): "Column = services: …" from the column definition; "Scope filter: all projects | project.id = X (applied to stored rows)".
- For the `% change` cell: also the previous week's snapshot id and its total (from the table response).
- Copy button copies the SQL with the params appended as a comment block, so it can be pasted into the BigQuery console.
- Read-only; no BigQuery call.

## Success Criteria
- [ ] API: `GET /snapshots/{id}.source.sql` contains `DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end` and the configured table name; `params` has exactly the four keys with the week's bounds; `job_id` non-empty; `bytes_billed > 0` (or `cache_hit == true`).
- [ ] API: `stale == true` when `status_at_fetch == "partial"` and the week is now `final` (unit test with injected clock).
- [ ] e2e: after clicking a Compute Engine cell the inspector shows "Snapshot #<id>", the status badge, a `<pre>` containing `_PARTITIONTIME`, a params table with `usage_start`, and the line "Column = services: Compute Engine"; with scope = one project the line "Scope filter: project.id = …" appears.
