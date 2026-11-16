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
Columns are not hard-coded: they come from the deployment's layout (`costtable/layout.go`, configured by `CLOUD0_COST_COLUMNS` and `CLOUD0_VENDOR_COLUMNS`). A billing `service.description` maps by exact match after trimming and case-folding.

**Dynamic layout (default, `CLOUD0_COST_COLUMNS` unset).** `week`, then one `svc:<description>` column per service with a non-zero net *or gross* in some row of the range (fully-credited services stay visible), ordered by the range's net total descending, ties by label; then `total`, `pct_change`. No vendor columns unless `CLOUD0_VENDOR_COLUMNS` is set.

**Pinned layout (`CLOUD0_COST_COLUMNS` set).** `;`-separated entries `[key:]Label[=Service|Service…]` (key defaults to the label in snake_case, services to the label). The listed columns are always present, in order; any other service with net ≠ 0 in the range follows as an `svc:<description>` column flagged `extra`, sorted by label; then `total`, `pct_change`, then vendor stubs.

The original "Tech Weekly Review" sheet is reproduced by this example (also `costtable.SheetColumns` / `SheetVendors`, used by the tests and the e2e suite):

```
CLOUD0_COST_COLUMNS=Compute Engine; Networking; cloud_sql:CloudSQL=Cloud SQL; Gemini API=Gemini API|Generative Language API; Cloud Run; GCS=Cloud Storage; VM manager=VM Manager; Cloud Build; Cloud Logging; Secret Manager; Artifact Registry
CLOUD0_VENDOR_COLUMNS=groq:AI Cost (Groq); claude:claude (usage)
```

| # | key | Header | Notes |
|---|---|---|---|
| 1 | `week` | Week | always |
| 2… | layout keys / `svc:<description>` | per layout | see above |
| n−1… | `total` | Total GCP | Σ of every service column (pinned + extra) |
| | `pct_change` | % change | total vs previous week |
| | `stub:<id>` | per `CLOUD0_VENDOR_COLUMNS` | stub until Phase 4 |

The layout lives in one place on the server; the UI renders whatever `columns` the API returns (its offline fallback is `Week · Total GCP · % change`).

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
