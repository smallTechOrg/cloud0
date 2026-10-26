# Capability: Cost table (+ CSV export)

## What It Does
Renders one row per ISO week (first→current) of net-of-credits cost per service in exactly the reference sheet's column order, from the latest stored snapshot per week, filtered to the chosen scope, with % change and a client-side CSV export of what is on screen.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Latest snapshot per week + rows | store | `snapshots`, `snapshot_rows` | yes (rows may be absent → "not fetched") |
| Scope | `Scope` | picker | default `all` |
| Range | `from`, `to` WeekIDs | defaults `first_week`, `current_week` | no |
| Thresholds | pct, abs (currency units) | `settings` | yes (seeded) |
| Annotations | text per week | `annotations` | no |
| Now | timestamp | server clock | yes |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Ordered columns + rows | `GET /api/v1/cost/table` JSON | UI table, CSV export |
| CSV file | RFC 4180 text, UTF-8, `\r\n` | browser download |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| SQLite | read latest snapshot per week, aggregate rows by service (+ scope filter) | 500 `internal_error`; UI banner |

## Business Rules

### Column order and service mapping
Fixed columns, always present, in this order; a billing `service.description` maps by exact match after trimming and case-folding:

| # | key | Header (as in the sheet) | `service.description` values aggregated |
|---|---|---|---|
| 1 | `week` | Week | — |
| 2 | `compute_engine` | Compute Engine | `Compute Engine` |
| 3 | `networking` | Networking | `Networking` |
| 4 | `cloud_sql` | CloudSQL | `Cloud SQL` |
| 5 | `gemini_api` | Gemini API | `Gemini API`, `Generative Language API` |
| 6 | `cloud_run` | Cloud Run | `Cloud Run` |
| 7 | `gcs` | GCS | `Cloud Storage` |
| 8 | `vm_manager` | VM manager | `VM Manager` |
| 9 | `cloud_build` | Cloud Build | `Cloud Build` |
| 10 | `cloud_logging` | Cloud Logging | `Cloud Logging` |
| 11 | `secret_manager` | Secret Manager | `Secret Manager` |
| 12 | `artifact_registry` | Artifact Registry | `Artifact Registry` |
| 13… | `svc:<description>` | the raw description | any service not listed above, one column each, sorted by label, present only if its net ≠ 0 in some row of the range |
| n−3 | `total` | Total GCP | Σ of every service column (fixed + extra) |
| n−2 | `pct_change` | % change | total vs previous week |
| n−1 | `stub:groq` | AI Cost (Groq) | stub until Phase 4 |
| n | `stub:claude` | claude (usage) | stub until Phase 4 |

The mapping lives in one place on the server (`costtable/columns.go`); the UI renders whatever `columns` the API returns.

### Values
- Cell `net` = Σ `snapshot_rows.net` for rows of the latest snapshot whose service is in the column (and `project_id = scope` when scope ≠ all); `gross`, `credits` likewise. Fixed columns with no rows → `0`.
- `total.net` = Σ all service cells (fixed + extra). The table total therefore equals the snapshot's `net_total` when scope = all.
- Default display is NET; gross and credits are available on hover and in drill-down.
- `delta_abs`, `delta_pct` per cell = this week − previous week's latest snapshot (same scope, same column); `null` if the previous week has no snapshot; `delta_pct` null when previous = 0. The previous week is `week − 1` even when it is before `from`.
- Row `pct_change` = `total.delta_pct`. Flags: `anomaly-flags.md`.
- Rows without a snapshot: `snapshot: null, cells: null` → UI shows "not fetched — Refresh" (link triggers a refresh for that week). Never zeros.
- Status badge per row: `week_status` plus `stale` when the snapshot's `status_at_fetch` is an earlier stage.

### CSV export (client-side)
- Columns = exactly the rendered columns in order (including stub columns); header cells = labels (without the "(stub)" suffix). One row per table row.
- Week cell = `WeekID`; numeric cells formatted with 2 decimals, no thousands separator, `-` sign for negatives; `pct_change` as a number with 2 decimals (e.g. `25.36`) or empty; stub cells empty; not-fetched rows: week id then empty cells.
- RFC 4180 quoting (fields containing `,`, `"`, `\r`, `\n` are quoted; `"` doubled); line ending `\r\n`; UTF-8 without BOM.
- Filename `cloud0-cost-<scope>-<from>-<to>.csv`. Pure function `buildCsv(columns, rows)` unit-tested.

## Success Criteria
- [ ] Unit (`costtable`): rows for `Generative Language API` and `Gemini API` sum into `gemini_api`; `Cloud SQL` → `cloud_sql`; `Cloud Storage` → `gcs`; `VM Manager` → `vm_manager`; `cloud sql ` (case/space) → `cloud_sql`; `Cloud Monitoring` → `svc:Cloud Monitoring` placed immediately before `total`; a service with zero net in every row produces no extra column.
- [ ] Unit: `total.net` = Σ service cells (exact float sum); scope filter changes cells and total but not `snapshot.row_count`.
- [ ] Unit: `delta_*` null for the first fetched week; `pct_change` null when the previous total is 0; previous week before `from` is still used.
- [ ] Unit: `stale` true for `status_at_fetch=partial` with `week_status=final`; false when equal.
- [ ] Real data (smoke): for `CLOUD0_SMOKE_WEEK`, `cells.compute_engine.net > 0`, `cells.total.net > 0`, `cells.total.net` = Σ service cells ± 0.005, and `cells.vm_manager.gross > 0` with `cells.vm_manager.net` within 0.005 of 0 (fully-credited service appears as 0 net, non-zero gross).
- [ ] Vitest: `buildCsv` output's first line equals the labels joined by `,`; a label containing a comma is quoted; a value `1191.69` renders `1191.69`; not-fetched row → `2026-W43,,,,…`; line endings `\r\n`.
- [ ] e2e: header cells read, in order, `Week, Compute Engine, Networking, CloudSQL, Gemini API, Cloud Run, GCS, VM manager, Cloud Build, Cloud Logging, Secret Manager, Artifact Registry, [extras], Total GCP, % change, AI Cost (Groq) (stub), claude (usage) (stub)`; the download's first line matches the headers without "(stub)".
