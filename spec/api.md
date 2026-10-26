# cloud0 — API contract (Phase 1 frozen; Phase 2 additions in "Phase 2 additions")

Base URL: `http://127.0.0.1:${CLOUD0_PORT}` (default `http://127.0.0.1:8087`). Prefix `/api/v1`. All bodies are UTF-8 JSON. The frontend (`web/src/lib/types.ts`) and the backend (`internal/httpapi`) both implement this file literally; changing it requires editing this file first.

## Common types

| Type | Shape | Notes |
|---|---|---|
| `WeekID` | string `YYYY-Www` | e.g. `2026-W43`; `ww` two digits 01–53; validated against the ISO calendar (2026 has W53, 2027 does not). |
| `Scope` | `"all"` or a project ID string | Project IDs come from `GET /config`. Unknown → `400 invalid_scope`. |
| `WeekStatus` | `"partial" \| "settling" \| "final"` | `spec/data.md → Week`. |
| `Money` | JSON number | USD, unrounded `float64`; credits are ≤ 0. UI rounds to 2 dp. |
| `Timestamp` | string RFC3339 UTC | `2026-10-26T09:15:00Z` |
| `Date` | string `YYYY-MM-DD` | |

Query parameters that are omitted take the documented defaults. Unknown query parameters are ignored. `Content-Type: application/json` on all responses. Every response carries `X-Request-Id` (echoed from the request header when present, otherwise generated).

## Envelope

- **Success:** the resource object at the top level. It never contains a key named `error`.
- **Error:** HTTP 4xx/5xx with body `{"error": {"code": string, "message": string, "hint": string | null}}`. `message` is one sentence for humans; `hint` is the concrete next action or `null`.

| HTTP | `code` | When |
|---|---|---|
| 400 | `invalid_request` | malformed JSON body, missing required field, wrong type |
| 400 | `invalid_week` | `week`/`from`/`to` not a valid `WeekID` |
| 400 | `week_out_of_range` | week before `first_week` or after `current_week`, or `from` > `to` |
| 400 | `invalid_scope` | scope is neither `all` nor a configured project ID |
| 400 | `invalid_level` | breakdown `level` not in `service|sku|resource`, or required `service`/`sku` missing for the level |
| 400 | `invalid_settings` | threshold not a finite number ≥ 0 |
| 400 | `annotation_too_long` | body > 10,000 characters |
| 404 | `not_found` | unknown snapshot/run id, unknown `/api/v1/*` path, no annotation for the week |
| 405 | `method_not_allowed` | |
| 409 | `refresh_in_progress` | another refresh is running; `hint: "Wait for the current refresh to finish, then retry."` |
| 500 | `internal_error` | unexpected; message is generic, details only in the server log with the `request_id` |
| 503 | `unhealthy` | health check failed (DB unreachable / migrations pending) |

A **source failure during refresh is not an HTTP error**: `POST /refresh` returns 200 with `run.status: "failed"` and the source's error fields, because the run was recorded and the UI renders it inline.

## Endpoints

### `GET /api/v1/health`
200:
```json
{"ok": true, "version": "0.1.0", "db": "ok", "migrations": "current", "ui": "real",
 "bigquery": {"status": "ok", "checked_at": "2026-10-26T09:00:00Z", "error_code": null, "message": null, "fix_hint": null}}
```
`ui` is `"real"` or `"placeholder"` (`spec/architecture.md → Embedded frontend`). `bigquery` reflects the startup dry-run; `status` is `"ok"` or `"error"`; the field is `null` in the scaffold build (before Phase 1 slice S3 adds the billing source). 503 with `unhealthy` when `db` is not `ok` or migrations are pending.

### `GET /api/v1/config`
200:
```json
{"projects": ["project-a", "project-b"], "first_week": "2026-W18", "current_week": "2026-W44",
 "default_week": "2026-W43", "settle_hours": 48, "now": "2026-10-26T09:00:00Z"}
```
`projects` is `CLOUD0_GCP_PROJECTS` in order. `default_week` = last complete week (`current_week − 1`, never before `first_week`).

### `GET /api/v1/cost/table`
Query: `scope` (default `all`), `from` (default `first_week`), `to` (default `current_week`).

200:
```json
{
  "scope": "all", "from": "2026-W18", "to": "2026-W44", "generated_at": "2026-10-26T09:00:00Z",
  "thresholds": {"pct": 20, "abs_usd": 50},
  "columns": [
    {"key": "week",              "label": "Week",              "kind": "week"},
    {"key": "compute_engine",    "label": "Compute Engine",    "kind": "service", "services": ["Compute Engine"]},
    {"key": "networking",        "label": "Networking",        "kind": "service", "services": ["Networking"]},
    {"key": "cloud_sql",         "label": "CloudSQL",          "kind": "service", "services": ["Cloud SQL"]},
    {"key": "gemini_api",        "label": "Gemini API",        "kind": "service", "services": ["Gemini API", "Generative Language API"]},
    {"key": "cloud_run",         "label": "Cloud Run",         "kind": "service", "services": ["Cloud Run"]},
    {"key": "gcs",               "label": "GCS",               "kind": "service", "services": ["Cloud Storage"]},
    {"key": "vm_manager",        "label": "VM manager",        "kind": "service", "services": ["VM Manager"]},
    {"key": "cloud_build",       "label": "Cloud Build",       "kind": "service", "services": ["Cloud Build"]},
    {"key": "cloud_logging",     "label": "Cloud Logging",     "kind": "service", "services": ["Cloud Logging"]},
    {"key": "secret_manager",    "label": "Secret Manager",    "kind": "service", "services": ["Secret Manager"]},
    {"key": "artifact_registry", "label": "Artifact Registry", "kind": "service", "services": ["Artifact Registry"]},
    {"key": "svc:Cloud Monitoring", "label": "Cloud Monitoring", "kind": "service", "services": ["Cloud Monitoring"], "extra": true},
    {"key": "total",             "label": "Total GCP",         "kind": "total"},
    {"key": "pct_change",        "label": "% change",          "kind": "pct_change"},
    {"key": "stub:groq",         "label": "AI Cost (Groq)",    "kind": "stub", "phase": 4},
    {"key": "stub:claude",       "label": "claude (usage)",    "kind": "stub", "phase": 4}
  ],
  "rows": [
    {
      "week": "2026-W42", "week_start": "2026-10-12", "week_end": "2026-10-18", "week_status": "final",
      "snapshot": {"id": 12, "fetched_at": "2026-10-26T09:01:10Z", "status_at_fetch": "final", "run_id": 7, "row_count": 2345, "stale": false},
      "cells": {
        "compute_engine": {"net": 1191.69, "gross": 1191.69, "credits": 0, "delta_abs": 14.2, "delta_pct": 1.21, "flagged": false},
        "networking":     {"net": 403.41,  "gross": 596.92,  "credits": -193.51, "delta_abs": null, "delta_pct": null, "flagged": false},
        "total":          {"net": 2095.11, "gross": 2400.02, "credits": -304.91, "delta_abs": 423.89, "delta_pct": 25.36, "flagged": true}
      },
      "pct_change": 25.36, "flagged": true, "annotation": "LB added for staging"
    },
    {
      "week": "2026-W43", "week_start": "2026-10-19", "week_end": "2026-10-25", "week_status": "settling",
      "snapshot": null, "cells": null, "pct_change": null, "flagged": false, "annotation": null
    }
  ]
}
```
Rules:
- Rows are every ISO week from `from` to `to` inclusive, ascending, whether or not a snapshot exists.
- `columns` is the complete ordered header for this response: fixed service columns always present (even if all-zero); `extra: true` columns appear only when at least one row in the range has a non-zero net for that service, sorted by label, after `artifact_registry` and before `total`; stub columns always last.
- `cells` has one entry per `service`/`total` column key when `snapshot` is non-null (fixed columns with no rows get `net: 0, gross: 0, credits: 0`); `cells` is `null` when no snapshot exists for the week.
- `delta_*` compare this row's cell with the same cell of the previous week's row **in this response**; `null` when the previous week has no snapshot or is before `from` (the server computes the previous week even if `from` excludes it — it looks up `from − 1` internally). `delta_pct` is `null` when the previous value is 0.
- `flagged` on a cell = `|delta_abs| ≥ thresholds.abs_usd && |delta_pct| ≥ thresholds.pct`. Row `flagged` = `cells.total.flagged`. `pct_change` = `cells.total.delta_pct`.
- `snapshot.stale` = `status_at_fetch` is an earlier stage than `week_status`.
- `annotation` = the week's note body or `null`.
- Scope filtering happens on `snapshot_rows.project_id` before aggregation; `snapshot` meta (`row_count`) is for all projects.

### `POST /api/v1/refresh`
Body: `{"mode": "week", "week": "2026-W43"}` or `{"mode": "all"}`.
- `mode: "week"` → one BigQuery job for that week → one snapshot.
- `mode: "all"` → one BigQuery job for `first_week`→`current_week` → one snapshot per week in the range.
- Synchronous; server-side timeout 120 s (then `run.status: "failed"`, `error_code: "timeout"`).

200:
```json
{
  "run": {
    "id": 8, "trigger": "refresh_week", "from": "2026-W43", "to": "2026-W43",
    "started_at": "2026-10-26T09:05:00Z", "finished_at": "2026-10-26T09:05:08Z", "duration_ms": 8123,
    "status": "ok", "error": null,
    "sources": [{
      "id": 8, "source": "bigquery_billing", "status": "ok", "duration_ms": 7900,
      "job_id": "job_abc123", "bytes_processed": 41234567, "bytes_billed": 41943040, "cache_hit": false, "rows_returned": 2345,
      "error_code": null, "error_message": null, "fix_hint": null
    }]
  },
  "snapshots": [{"id": 13, "week": "2026-W43", "status_at_fetch": "settling", "row_count": 2345, "net_total": 2012.5}]
}
```
Failure example (still 200): `run.status: "failed"`, `snapshots: []`, source with `status: "failed", error_code: "permission_denied", error_message: "…", fix_hint: "Grant roles/bigquery.jobUser on CLOUD0_BIGQUERY_PROJECT and BigQuery Data Viewer on the export dataset to the service account in GOOGLE_APPLICATION_CREDENTIALS."`
Errors: `400 invalid_request` (bad mode / missing week), `400 invalid_week`, `400 week_out_of_range`, `409 refresh_in_progress`.

### `GET /api/v1/snapshots/{id}`
Provenance for a snapshot. 200:
```json
{
  "id": 13, "week": "2026-W43", "week_start": "2026-10-19", "week_end": "2026-10-25",
  "fetched_at": "2026-10-26T09:05:00Z", "status_at_fetch": "settling", "week_status": "final", "stale": true,
  "run_id": 8, "row_count": 2345, "gross_total": 2300.1, "credits_total": -287.6, "net_total": 2012.5,
  "projects": ["project-a", "project-b"],
  "formula": "net = SUM(cost) + SUM(credits.amount), grouped by ISO week of usage_start_time (UTC) and service.description; credits are summed per line item",
  "source": {
    "source": "bigquery_billing", "job_id": "job_abc123", "bytes_processed": 41234567, "bytes_billed": 41943040,
    "cache_hit": false, "duration_ms": 7900,
    "sql": "SELECT\n  EXTRACT(ISOYEAR FROM usage_start_time) AS iso_year, …",
    "params": {"part_start": "2026-10-18", "part_end": "2026-10-27", "usage_start": "2026-10-19T00:00:00Z", "usage_end": "2026-10-26T00:00:00Z"}
  }
}
```
`projects` = distinct `project_id` values present in the snapshot rows. `sql` is the exact executed text (`run_sources.sql_text`). 404 `not_found` for unknown id.

### `GET /api/v1/snapshots/{id}/breakdown`
Query: `level` (`service` | `sku` | `resource`, required), `scope` (default `all`), `service` (required for `sku` and `resource`; raw `service.description`, URL-encoded), `sku` (required for `resource`).

200 (`level=sku` shown):
```json
{
  "snapshot_id": 13, "level": "sku", "scope": "all", "service": "Compute Engine", "sku": null,
  "rows": [
    {"key": "N1 Predefined Instance Core running in Americas", "sku_id": "ABCD-1234-5678", "gross": 600.2, "credits": 0, "net": 600.2, "line_items": 168},
    {"key": "Storage PD Capacity", "sku_id": "…", "gross": 88.0, "credits": -2.0, "net": 86.0, "line_items": 56}
  ],
  "totals": {"gross": 1191.69, "credits": 0, "net": 1191.69}
}
```
- `level=service`: rows keyed by raw `service.description` with extra field `column_key` (the Cost-table column it maps to, e.g. `gemini_api` or `svc:Cloud Monitoring`).
- `level=sku`: rows keyed by `sku.description` for the given service, with `sku_id`.
- `level=resource`: rows grouped by (`project_id`, `resource_name`, `resource_global_name`) for the given service + SKU; fields `key` (= `resource_name` or `"(unattributed)"` when NULL), `project_id`, `resource_name`, `resource_global_name`, `gross`, `credits`, `net`, `line_items`.
- Rows sorted by `net` descending, then `key`. `totals` = sums over `rows`. Totals at `sku` level equal the service cell in the table for the same scope; totals at `resource` level equal the SKU row.
Errors: `404 not_found`, `400 invalid_level`, `400 invalid_scope`.

### `GET /api/v1/runs`
Query: `limit` (default 20, max 200). 200:
```json
{"runs": [{"id": 8, "trigger": "refresh_week", "from": "2026-W43", "to": "2026-W43", "started_at": "…", "finished_at": "…", "duration_ms": 8123, "status": "ok", "error": null,
           "sources": [{"id": 8, "source": "bigquery_billing", "status": "ok", "duration_ms": 7900, "job_id": "…", "bytes_processed": 41234567, "bytes_billed": 41943040, "cache_hit": false, "rows_returned": 2345, "error_code": null, "error_message": null, "fix_hint": null}],
           "snapshot_ids": [13]}]}
```
Newest first. `sql`/`params` are omitted in the list.

### `GET /api/v1/runs/{id}`
200: one run object as above plus, per source, `sql` and `params`. 404 `not_found`.

### `GET /api/v1/settings`
200: `{"anomaly_pct_threshold": 20, "anomaly_abs_threshold_usd": 50, "updated_at": "2026-10-26T09:00:00Z"}`

### `PUT /api/v1/settings`
Body: `{"anomaly_pct_threshold": 10, "anomaly_abs_threshold_usd": 25}` (both required, finite, ≥ 0). 200: same shape as GET. 400 `invalid_settings`.

### `GET /api/v1/annotations/{week}`
200: `{"week": "2026-W43", "body": "LB added for staging", "created_at": "…", "updated_at": "…"}`. 404 `not_found` when no note. 400 `invalid_week`.

### `PUT /api/v1/annotations/{week}`
Body: `{"body": "text"}`. Non-empty → upsert, 200 as GET. Empty string (after trim) → delete, 200 `{"week": "2026-W43", "body": "", "created_at": null, "updated_at": null}`. 400 `annotation_too_long` / `invalid_week`.

## Static routes (same origin)

| Path | Behaviour |
|---|---|
| `GET /` | embedded `index.html` (or `placeholder.html` when no frontend build), `Cache-Control: no-cache` |
| `GET /_next/static/*`, other emitted files | embedded asset, `Cache-Control: public, max-age=31536000, immutable` |
| any other non-`/api` path | `index.html`, 200 (SPA fallback) |
| `/api/v1/*` unmatched | 404 `not_found` envelope |

## Phase 2 additions (frozen)

Same envelope, `WeekID`/`Scope` rules and error handling as Phase 1. New error codes (add to the table above):

| HTTP | `code` | When |
|---|---|---|
| 400 | `invalid_month` | `month`/`from`/`to` of the month endpoints is not `YYYY-MM` (month 01–12) |
| 400 | `month_out_of_range` | month after the current month, or `from` > `to` |
| 400 | `invalid_limit` | `PUT /limits` body rejected by `limits.ParseOverrides` (unknown allowance id, non-finite or negative allowance, `warn_pct` outside (0, 100]) |

### Shared Phase 2 types

`Provenance` (`ProvenanceJSON` in `internal/store/metrics.go`):
```json
{"source": "monitoring_vm", "query": "metric.type=\"compute.googleapis.com/instance/cpu/utilization\" …", "aggregation": "{\"align\":\"ALIGN_MEAN\"}",
 "window": {"start": "2026-10-19T00:00:00Z", "end": "2026-10-26T00:00:00Z"}, "fetched_at": "2026-10-26T09:05:00Z"}
```
`MetricCell` (`CellJSON` + `limit_level`): **exactly one of `value` / `gap` is non-null.** `hint` is non-null only with a gap. `provenance` is always present. `unit` is always present (also on gaps).
```json
{"value": 12.5, "unit": "%", "gap": null, "hint": null, "provenance": {…}, "limit_level": null}
{"value": null, "unit": "%", "gap": "no agent", "hint": "install the Ops Agent", "provenance": {…}, "limit_level": null}
```
`limit_level` is `"ok" | "near" | "over" | null` (null = no limit applies to this cell or no data); it is the level of the limit whose metric is this cell's group+column (`limits.Allowance.MetricGroup/MetricKey`), evaluated as in `GET /limits`.
Gap strings in use: `no agent`, `no data`, `source failed`, `not collected` (and the error sources' own, e.g. `logging disabled`). A source that failed in the latest refresh yields `gap: "source failed"` cells with the source's `fix_hint` as `hint`; a metric with no value yields `gap: "no data"` — never `0`.
`SnapshotRef` = `{"id": 5, "fetched_at": "…", "status_at_fetch": "settling", "run_id": 8, "row_count": 14, "stale": false}`; `stale` as for the Cost table. For error batches `id` is the `run_id` of the batch (error rows have no snapshot header).

### `GET /api/v1/metrics/table`
Query: `week` (default `default_week`), `scope` (default `all`). 200:
```json
{
  "week": "2026-W43", "week_start": "2026-10-19", "week_end": "2026-10-25", "week_status": "settling",
  "scope": "all", "generated_at": "2026-10-26T09:00:00Z",
  "snapshot": {"id": 5, "fetched_at": "2026-10-26T09:05:00Z", "status_at_fetch": "settling", "run_id": 8, "row_count": 14, "stale": false},
  "groups": [
    {"group": "vm", "label": "VM",
     "columns": [{"key": "cpu_pct", "label": "CPU %", "unit": "%"}, {"key": "memory_pct", "label": "Memory %", "unit": "%"}, {"key": "disk_pct", "label": "Disk %", "unit": "%"}],
     "rows": [{"project": "project-a", "resource": "vm-a", "cells": {"cpu_pct": {…MetricCell}, "memory_pct": {…}, "disk_pct": {…}}}]}
  ]
}
```
- `groups` always has all seven groups, in this order: `vm`, `cloudsql`, `networking`, `cloudrun`, `gcs`, `logging`, `errors` (labels/columns from `gcpsource.MetricGroups()`, the single source of truth; columns in sheet order: vm/cloudsql `cpu_pct, memory_pct, disk_pct`; networking `lb_hours, outbound_gb, inbound_gb`; cloudrun `service_memory_gib_s, requests, service_cpu_sec, egress_intercontinental_gb`; gcs `class_a_ops, class_b_ops, download_apac_gb, sent_gb, storage_gb_month`; logging `logging_gb`; errors `c4xx, c5xx`).
- `snapshot` is `null` and every group has `rows: []` when the week has no metric snapshot (UI renders "not fetched yet").
- `rows` = the stored `MetricRow`s of that group whose `project` is in scope (`scope` = a project id filters on `project`; no cross-project summing), in stored order. `resource: ""` is a per-project group-total row. `cells` has one entry per column of the group; a column absent from the stored row is filled with `{"value": null, "gap": "not collected", …}` with `provenance.source: ""`.
- The `errors` group is derived from the latest error batch of the week (see `GET /errors/table`): one row per `source: "cloudrun"` error row (`resource` = service), cells `c4xx`/`c5xx` carrying that row's counts (or its gap), same provenance. No error batch → `rows: []`.

### `GET /api/v1/errors/table`
Query: `week` (default `default_week`), `scope`. 200:
```json
{
  "week": "2026-W43", "scope": "all", "generated_at": "2026-10-26T09:00:00Z",
  "snapshot": {"id": 8, "fetched_at": "2026-10-26T09:05:00Z", "status_at_fetch": "settling", "run_id": 8, "row_count": 3, "stale": false},
  "rows": [
    {"source": "cloudrun", "project": "project-a", "service": "svc-a", "c2xx": 1000, "c3xx": 0, "c4xx": 12, "c5xx": 1, "groups": null, "occurrences": null, "gap": null, "hint": null, "provenance": {…}},
    {"source": "error_reporting", "project": "project-a", "service": "svc-a", "c2xx": null, "c3xx": null, "c4xx": null, "c5xx": null, "groups": 3, "occurrences": 40, "gap": null, "hint": null, "provenance": {…}},
    {"source": "load_balancer", "project": "project-a", "service": "", "c2xx": null, "c3xx": null, "c4xx": null, "c5xx": null, "groups": null, "occurrences": null, "gap": "logging disabled", "hint": "enable logging on the backend service (sample rate >= 0.1)", "provenance": {…}}
  ],
  "totals": {"c2xx": 1000, "c3xx": 0, "c4xx": 12, "c5xx": 1, "groups": 3, "occurrences": 40}
}
```
- `source` ∈ `cloudrun | error_reporting | load_balancer | nginx` (`gcpsource.ErrSrc*`). Counts are `null` exactly when `gap` is non-null (or the source does not produce that count: `groups`/`occurrences` only on `error_reporting`, `c*xx` only on the others); an explicit `0` is a real zero.
- `rows` are the latest error batch (highest `run_id`) of the week, filtered to `scope` by `project`, stored order. `snapshot: null` and `rows: []` when never fetched. `totals` sums non-null values over the returned rows (`null` per field when no row has it; all `null` when `rows` is empty).

### `GET /api/v1/limits`
Query: `week` (default `default_week`). 200:
```json
{
  "week": "2026-W43", "month": "2026-10", "month_start": "2026-10-01", "month_end": "2026-10-31",
  "warn_pct": 80, "updated_at": "2026-10-26T09:00:00Z", "generated_at": "2026-10-26T09:00:00Z",
  "limits": [
    {"id": "logging_gb", "service": "Cloud Logging", "label": "Log ingestion", "unit": "GB",
     "allowance": 50, "default_allowance": 50, "used": 21.5, "projected": 28.3, "pct": 56.6, "level": "ok", "week_used": 5.1, "stub": false, "reason": null}
  ]
}
```
- `limits` = `limits.Defaults()` in order (14 entries incl. stubs), allowance overridden via `limits.Apply(…, ParseOverrides(settings "limit."))`; `warn_pct` = the override or 80; `updated_at` = `store.LimitsUpdatedAt` (`null` if never saved).
- `month` = the UTC calendar month containing the `week`'s Sunday, clamped to the current month. `used` (month to date) = sum of the allowance's metric over the **latest metric snapshots of every ISO week whose Monday is in that month, up to and including `week`** (each via `limits.UsageFromRows`; weeks without a snapshot or value contribute nothing). `week_used` = `limits.UsageFromRows` of the requested week's snapshot (`null` when none). `projected`, `pct`, `level` come from `limits.Evaluate(allowance, usage, month window, now, warn_pct)`; when no week yields a value: `level: "no data"`, `used`/`projected`/`pct`/`week_used` `null` (`limits.NoData`). Stubs: `level: "stub"`, `stub: true`, `reason` set, numbers `null`.
- `level` ∈ `ok | near | over | stub | no data`.

### `PUT /api/v1/limits`
Body (at least one key; both optional otherwise): `{"allowances": {"logging_gb": 75, "gcs_class_a_ops": 10000}, "warn_pct": 90}`. Each allowance is a finite JSON number ≥ 0 keyed by a known `limits.Allowance.ID`; `warn_pct` ∈ (0, 100]. The server builds settings `limit.<id>` / `limit.warn_pct`, validates the full merged set with `limits.ParseOverrides` (failure → `400 invalid_limit`, message names the key; nothing is saved), then `store.PutSettings`. Wrong JSON types / empty body → `400 invalid_request`. Stub allowances may be edited like any other. To reset an allowance, PUT its default. 200: the `GET /limits` body for `default_week` (optional query `week` honoured).

### `GET /api/v1/cost/month`
Query: `from` (default month of `first_week`'s Monday), `to` (default current month), both `YYYY-MM`; `scope`. 200:
```json
{
  "scope": "all", "from": "2026-09", "to": "2026-10", "generated_at": "2026-10-26T09:00:00Z",
  "thresholds": {"pct": 20, "abs_usd": 50},
  "columns": [{"key": "month", "label": "Month", "kind": "month"}, {"key": "compute_engine", "label": "Compute Engine", "kind": "service", "services": ["Compute Engine"]}, "…identical to GET /cost/table columns after the week column…"],
  "rows": [
    {"month": "2026-10", "month_start": "2026-10-01", "month_end": "2026-10-31", "month_status": "partial",
     "snapshot": {"id": 4, "fetched_at": "2026-10-26T09:06:00Z", "status_at_fetch": "partial", "run_id": 8, "row_count": 31, "stale": false},
     "cells": {"compute_engine": {"net": 1191.69, "gross": 1191.69, "credits": 0, "delta_abs": 14.2, "delta_pct": 1.21, "flagged": false}, "total": {…}},
     "pct_change": 3.1, "flagged": false, "invoice_months": ["202610"]},
    {"month": "2026-09", "month_start": "2026-09-01", "month_end": "2026-09-30", "month_status": "final", "snapshot": null, "cells": null, "pct_change": null, "flagged": false, "invoice_months": []}
  ],
  "summary": {"month": "2026-10", "net": 812.4, "target": 2500, "projected": 2210.9, "status": "partial", "has_data": true}
}
```
- Rows are every month `from`..`to` ascending (rows without a snapshot as the second example). `columns`, `cells`, `delta_*`, `flagged`, `pct_change`, `thresholds` follow the Cost-table rules exactly with "month" in place of "week" (service mapping identical to `/cost/table`, extra services appended the same way, stub columns last; the previous month is looked up internally even when before `from`). Scope filters `month_rows.project_id` before aggregation. `month_status` = `monthcost.Status(month, now, settle_hours)`. `invoice_months` = distinct `invoice_month` values of the snapshot rows in scope.
- `summary` is for the **current month** (`config.now`) regardless of `from`/`to`, computed with `monthcost.SummaryWithSettle` over that month's latest snapshot rows in scope: `net`, `projected` (linear), `status`, `target` = `store.GetMonthlyTarget` (`null` when unset), `has_data` false (and net/projected 0) when the current month was never fetched.
- Errors: `400 invalid_month`, `400 month_out_of_range`, `400 invalid_scope`.

### `POST /api/v1/refresh` (Phase 2)
Body: `{"mode": "week", "week": "2026-W43"}` | `{"mode": "month", "month": "2026-10"}` | `{"mode": "all"}`.

| mode | run `trigger` | run `from`/`to` | sources run (each exactly one `run_sources` row, in this order) |
|---|---|---|---|
| `week` | `refresh_week` | the week | `bigquery_billing`, then the week-window sources below |
| `month` | `refresh_month` | ISO weeks containing the month's first and last day (last day clamped to today) | `bigquery_month` only |
| `all` | `refresh_all` | `first_week`..`current_week` | `bigquery_billing` (all weeks), `bigquery_month` (every month overlapping the range → one `month_snapshots` row per month), then the week-window sources below for `default_week` (last complete week) |

Week-window sources (window = the ISO week): `monitoring_vm`, `monitoring_cloudsql`, `monitoring_cloudrun`, `monitoring_gcs`, `monitoring_logging`, `bigquery_usage`, `errors_cloudrun`, `errors_error_reporting`, `errors_load_balancer`, `errors_nginx`. Their `Metrics` (monitoring + usage) are merged into one `metric_snapshots` row for the week; their `Errors` into one `error_rows` batch for the week (`store.InsertMetricSnapshot` / `InsertErrorRows` inside the run). `bigquery_month` writes one `month_snapshots` row per month with `run_source_id` set.
- `run_sources.status` stays `ok | failed`; a non-ok `gcpsource.Result.Status` (`permission_denied`, `api_disabled`, `quota`, `failed`) is stored in `error_code` with `Message` → `error_message` and `Hint` → `fix_hint`. The exact filter/SQL text goes to `sql_text`, the aggregation/params JSON to `params`. Non-BigQuery sources have `job_id`, `bytes_*`, `cache_hit` all `null`; `rows_returned` = rows produced.
- `run.status`: `ok` when every source ok; `partial` when billing (week/all) or month cost (month) succeeded but at least one other source failed; `failed` when billing (week/all) / month cost (month) failed (other sources still run and are recorded, their data still stored). A failed source never aborts the others.
- Response (200): same shape as Phase 1 plus `metric_snapshots`, `month_snapshots`:
```json
{"run": {…, "trigger": "refresh_all", "status": "partial", "sources": [{…"source": "bigquery_billing", "status": "ok"…}, {…"source": "monitoring_vm", "status": "failed", "error_code": "permission_denied", "fix_hint": "grant the service account roles/monitoring.viewer"…}]},
 "snapshots": [{"id": 13, "week": "2026-W43", "status_at_fetch": "settling", "row_count": 2345, "net_total": 2012.5}],
 "metric_snapshots": [{"id": 5, "week": "2026-W43", "status_at_fetch": "settling", "row_count": 14}],
 "month_snapshots": [{"id": 4, "month": "2026-10", "status_at_fetch": "partial", "row_count": 31, "net_total": 812.4}]}
```
`snapshots` is `[]` for `mode: "month"`; `metric_snapshots`/`month_snapshots` are `[]` when not produced. `400 invalid_month` for a bad `month`, `400 month_out_of_range` for a future month; the other Phase 1 errors apply. `GET /runs` returns `trigger: "refresh_month"` for month runs and each source with its `source` name as above.

## Later phases (reserved paths, not implemented in Phase 1)

`/api/v1/cost/by-product`, `/api/v1/cost/by-environment`, `/api/v1/attribution/*`, `/api/v1/targets`, `/api/v1/rightsizing`, `/api/v1/budgets`, `/api/v1/alerts/*`, `/api/v1/history`, `/api/v1/vendors/*`. Their shapes are frozen in this file when their phase starts.
