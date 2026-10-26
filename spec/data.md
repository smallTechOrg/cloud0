# cloud0 — Data

Store: one SQLite file at `CLOUD0_DB_PATH` (engine choice and portability rules: `spec/architecture.md → Stack`). All timestamps are RFC3339 UTC text; all dates are `YYYY-MM-DD`; money is `REAL` USD; booleans are `INTEGER` 0/1. Schema changes only via numbered goose migrations in `internal/store/migrations/`.

## Entities

### Week (not a table)
An ISO week `YYYY-Www`, Monday 00:00:00 UTC to the next Monday 00:00:00 UTC (exclusive). Derived facts, computed on demand from `now`:
- `week_status`: `partial` (now < week end) · `settling` (week end ≤ now < week end + `CLOUD0_SETTLE_HOURS`) · `final` (otherwise).
- Range shown everywhere: `CLOUD0_FIRST_WEEK` → current week (the week containing `now`). Default selected week: the last complete week (current − 1).
- 2026 has 53 ISO weeks; the math must handle W53 and year rollover.

### snapshots
One immutable result of one BigQuery fetch for one ISO week, covering **all** billed projects. A refresh never updates a snapshot; it inserts a new one. The latest snapshot per week (highest `id`) is what the table displays.

| Column | Type | Notes |
|---|---|---|
| id | INTEGER PK AUTOINCREMENT | |
| iso_year, iso_week | INTEGER NOT NULL | |
| week_start | TEXT NOT NULL | Monday date (inclusive) |
| week_end | TEXT NOT NULL | Sunday date (inclusive, for display); queries use exclusive next-Monday timestamps stored in `run_sources.params_json` |
| fetched_at | TEXT NOT NULL | query start time |
| status_at_fetch | TEXT NOT NULL CHECK IN ('partial','settling','final') | `week_status` evaluated at `fetched_at` |
| run_id | INTEGER NOT NULL REFERENCES runs(id) | |
| run_source_id | INTEGER NOT NULL REFERENCES run_sources(id) | provenance pointer |
| row_count | INTEGER NOT NULL | number of `snapshot_rows` |
| gross_total, credits_total, net_total | REAL NOT NULL | sums over rows (all projects) |
| created_at | TEXT NOT NULL | |

Indices: `(iso_year, iso_week, id DESC)` — latest-per-week lookup; `(run_id)`.

### snapshot_rows
The finest grain kept: one row per (project, service, SKU, resource) for the snapshot's week. Everything the UI shows for a week (service columns, drill-down, scope filtering) is a `SUM` over these rows.

| Column | Type | Notes |
|---|---|---|
| id | INTEGER PK AUTOINCREMENT | |
| snapshot_id | INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE | |
| project_id | TEXT NOT NULL | `project.id` |
| service_id | TEXT NOT NULL DEFAULT '' | `service.id` |
| service | TEXT NOT NULL | `service.description` exactly as exported |
| sku_id | TEXT NOT NULL DEFAULT '' | `sku.id` |
| sku | TEXT NOT NULL | `sku.description` |
| resource_name | TEXT NULL | `resource.name`; NULL = unattributed |
| resource_global_name | TEXT NULL | `resource.global_name` |
| gross | REAL NOT NULL | `SUM(cost)` |
| credits | REAL NOT NULL | `SUM(credits.amount)` (≤ 0 normally) |
| net | REAL NOT NULL | `gross + credits` |
| line_items | INTEGER NOT NULL | billing export lines aggregated into this row |

Indices: `(snapshot_id, service)`, `(snapshot_id, service, sku)`, `(snapshot_id, project_id)`.

### runs
One user-triggered refresh.

| Column | Type | Notes |
|---|---|---|
| id | INTEGER PK AUTOINCREMENT | |
| trigger | TEXT NOT NULL CHECK IN ('refresh_week','refresh_all') | |
| from_iso_year, from_iso_week, to_iso_year, to_iso_week | INTEGER NOT NULL | inclusive week range |
| started_at | TEXT NOT NULL | |
| finished_at | TEXT NULL | |
| duration_ms | INTEGER NULL | |
| status | TEXT NOT NULL CHECK IN ('running','ok','partial','failed') | `partial` = some sources failed (possible from Phase 2) |
| error | TEXT NULL | run-level error (e.g. DB write failure) |
| created_at | TEXT NOT NULL | |

Index: `(started_at DESC)`.

### run_sources
One row per data source invoked by a run. Phase 1 has exactly one source, `bigquery_billing`. This row **is** the provenance record.

| Column | Type | Notes |
|---|---|---|
| id | INTEGER PK AUTOINCREMENT | |
| run_id | INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE | |
| source | TEXT NOT NULL | `bigquery_billing` (Phase 2+: `monitoring_*`, `error_reporting`, …) |
| status | TEXT NOT NULL CHECK IN ('ok','failed') | Phase 4 migration widens to include `skipped` (unconfigured vendor) |
| started_at, finished_at | TEXT NOT NULL | |
| duration_ms | INTEGER NOT NULL | |
| sql_text | TEXT NOT NULL | the exact text executed (for Monitoring sources: the exact filter/aggregation JSON) |
| params_json | TEXT NOT NULL | JSON object of named parameters, e.g. `{"part_start":"2026-10-11","part_end":"2026-10-20","usage_start":"2026-10-12T00:00:00Z","usage_end":"2026-10-19T00:00:00Z"}` |
| job_id | TEXT NULL | BigQuery job id |
| bytes_processed, bytes_billed | INTEGER NULL | from job statistics |
| cache_hit | INTEGER NULL | 0/1 |
| rows_returned | INTEGER NULL | |
| error_code | TEXT NULL | one of `credentials_missing`, `permission_denied`, `api_disabled`, `table_not_found`, `quota_exceeded`, `timeout`, `invalid_query`, `unknown` |
| error_message | TEXT NULL | provider message, trimmed to 2000 chars |
| fix_hint | TEXT NULL | human instruction (see `spec/capabilities/run-log.md`) |

Index: `(run_id)`.

### annotations
Free-text note per ISO week (scope-agnostic).

| Column | Type | Notes |
|---|---|---|
| id | INTEGER PK AUTOINCREMENT | |
| iso_year, iso_week | INTEGER NOT NULL, UNIQUE together | |
| body | TEXT NOT NULL | ≤ 10,000 chars |
| created_at, updated_at | TEXT NOT NULL | |

Phase 3 adds a nullable `cell_key` column for cell-level notes; Phase 1 rows keep it NULL.

### settings
Key/value, seeded by migration 0001.

| key | default value | Meaning |
|---|---|---|
| `anomaly_pct_threshold` | `20` | Flag when |Δ%| ≥ this **and** … |
| `anomaly_abs_threshold_usd` | `50` | … |Δ$| ≥ this (week-over-week, latest snapshots) |

Columns: `key TEXT PK`, `value TEXT NOT NULL`, `updated_at TEXT NOT NULL`. Later phases add keys (limits allowances, settle hours override) without schema change.

## Relationships

```
runs 1──n run_sources 1──n snapshots 1──n snapshot_rows
annotations ──(iso_year, iso_week)── weeks (no FK; weeks are virtual)
settings (singleton rows)
```

## Lifecycle

- **Refresh (week):** `runs` row inserted (`running`) → BigQuery fetch → in one transaction: `run_sources` row; one `snapshots` row for the week (even when zero rows returned: `row_count 0`, totals 0) + its `snapshot_rows`; `runs` → `ok`. On source failure: `run_sources` row with error fields, no snapshot, `runs` → `failed`.
- **Refresh (all):** same, one query over first→current; one `snapshots` row per ISO week in the range (weeks with no rows still get an empty snapshot so they read as "fetched, $0" rather than "not fetched"), all pointing to the same `run_source_id`.
- **Read:** table = latest snapshot per week (by `id`), rows filtered by scope (`project_id = ?` or none), aggregated by mapped column; previous-week comparison uses the latest snapshot of the previous week under the same scope.
- **Staleness:** a snapshot whose `status_at_fetch` is earlier than the week's current `week_status` is displayed as stale ("Refresh for final"). Data is never silently recomputed.
- **Annotations:** upsert on PUT; PUT with empty body deletes the row.
- **Settings:** PUT replaces both values atomically.
- **Retention:** indefinite in Phase 1 — one week of rows is a few thousand; a year of weekly refreshes stays well under 100 MB. A `cloud0 prune --keep-last N` subcommand is a candidate for Phase 4, not before.
- **Tests:** every test opens its own temp SQLite file, runs `migrate up`, and deletes the file on cleanup — never the dev DB.

## Migration 0001 (scope)

Creates all six tables above with their indices and seeds the two `settings` rows. Later phases add migrations `0002_…` onward; nothing in Phase 1 alters 0001 after it is committed.

## Portability (Postgres later)

- Types used: `INTEGER`, `REAL`, `TEXT` only; `AUTOINCREMENT` is the one SQLite-ism, isolated to the migration file (a `postgres/` goose directory would use `BIGSERIAL`).
- No SQLite-only functions in queries (`strftime`, `json_*`, `RETURNING` avoided; last-insert id read via `sql.Result.LastInsertId()` behind a one-line seam in `store.go`).
- `?` placeholders throughout; a Postgres driver would need a rebind helper in `store.go` only.
- Repositories accept `*sql.DB`/`*sql.Tx` interfaces, so the engine swap touches `store.Open` and the migrations directory, nothing above.
