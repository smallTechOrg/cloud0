# cloud0 — API contract (Phase 1 frozen; Phase 2 additions in "Phase 2 additions")

Base URL: `http://127.0.0.1:${CLOUD0_PORT}` (default `http://127.0.0.1:8087`). Prefix `/api/v1`. All bodies are UTF-8 JSON. The frontend (`web/src/lib/types.ts`) and the backend (`internal/httpapi`) both implement this file literally; changing it requires editing this file first.

## Common types

| Type | Shape | Notes |
|---|---|---|
| `WeekID` | string `YYYY-Www` | e.g. `2026-W43`; `ww` two digits 01–53; validated against the ISO calendar (2026 has W53, 2027 does not). |
| `Scope` | `"all"` or a project ID string | Project IDs come from `GET /config`. Unknown → `400 invalid_scope`. |
| `WeekStatus` | `"partial" \| "settling" \| "final"` | `spec/data.md → Week`. |
| `Money` | JSON number | Unrounded `float64` in `config.currency` (default INR); credits are ≤ 0. UI rounds to 2 dp. |
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
 "default_week": "2026-W43", "settle_hours": 48, "currency": "INR", "now": "2026-10-26T09:00:00Z"}
```
`projects` is `CLOUD0_GCP_PROJECTS` in order. `currency` is `CLOUD0_CURRENCY` (ISO 4217, default `INR`): the unit of every `Money` value. `default_week` = last complete week (`current_week − 1`, never before `first_week`).

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
     "rows": [{"project": "project-a", "resource": "vm-a", "is_total": false, "cells": {"cpu_pct": {…MetricCell}, "memory_pct": {…}, "disk_pct": {…}}},
              {"project": "", "resource": "", "is_total": true, "cells": {"cpu_pct": {"value": null, "gap": "—", …}, …}}]}
  ]
}
```
- `groups` always has all seven groups, in this order: `vm`, `cloudsql`, `networking`, `cloudrun`, `gcs`, `logging`, `errors` (labels/columns from `gcpsource.MetricGroups()`, the single source of truth; columns in sheet order: vm/cloudsql `cpu_pct, memory_pct, disk_pct`; networking `lb_hours, outbound_gb, inbound_gb`; cloudrun `service_memory_gib_s` (Memory total), `service_memory_active_gib_s` (Active memory), `service_memory_idle_gib_s` (Idle min-instance memory), `requests`, `service_cpu_sec` (CPU total), `service_cpu_active_sec`, `service_cpu_idle_sec`, `egress_intercontinental_gb`; gcs `class_a_ops, class_b_ops, download_apac_gb, sent_gb, storage_gb_month`; logging `logging_gb`; errors `c4xx, c5xx`).
- `snapshot` is `null` and every group has `rows: []` when the week has no metric snapshot (UI renders "not fetched yet").
- `rows` = the resource rows of that group whose `project` is in scope (`scope` = a project id filters on `project`), in stored order, followed by exactly one **TOTAL row** (`is_total: true`, `project: ""`, `resource: ""`) when the group has any row. Stored rows are merged at read time by `(project, resource)` — for Cloud Run the Monitoring row (`service_memory_gib_s`, `requests`, `service_cpu_sec`) and the billing row (`service_memory_active_gib_s`, `service_memory_idle_gib_s`, `service_cpu_active_sec`, `service_cpu_idle_sec`, `egress_intercontinental_gb`) of the same service name share one row; a service present in only one source keeps its cells and the other source's cells are gaps (`no data`). A stored project-level row (`Resource ""`: billing egress, Networking) is not a resource row: when the group has resource rows it only feeds the TOTAL; when it has none (Networking) it is shown with `resource` = the project id. `cells` has one entry per column of the group; a column absent from the stored row is filled with `{"value": null, "gap": "not collected", …}` with `provenance.source: ""`.
- **TOTAL row**: per column, the sum over the (in-scope, all-project) resource rows that have a value (hint `sum of N rows`, provenance of the first contributing cell); when no resource row has a value the project-level aggregates supply it, else the first gap is carried (`no data` if none). Percentage columns (`unit: "%"`) are never summed: they are the gap `"—"` (hint "a percentage is not summed across resources"). The TOTAL row carries the `limit_level` of the column's allowance, since limits evaluate on totals. Groups with no rows have no TOTAL row.
- **Cloud Run memory/CPU**: `service_memory_gib_s` / `service_cpu_sec` are the Cloud Monitoring TOTAL allocation (active + idle min-instance); `*_active_*` comes from billing SKUs `Services Memory|CPU (Request-based billing)`, `*_idle_*` from `Services Min Instance Memory|CPU (Request-based billing)` (per service, `resource.name`). Active + idle reconciles with the Monitoring total (W42: 25,441 + 300,337 ≈ 322,486 GiB-s within 5%). The free-tier allowances (`cloudrun_memory_gib_s`, `cloudrun_cpu_vcpu_s`) are evaluated on the TOTAL columns and their Limits rows carry the note "free tier is consumed by active + idle min-instance memory|CPU" in `reason`.
- The `errors` group is derived from the latest error batch of the week (see `GET /errors/table`): one row per `source: "cloudrun"` error row (`resource` = service), cells `c4xx`/`c5xx` carrying that row's counts (or its gap), same provenance. No error batch → `rows: []`.

### `GET /api/v1/errors/table`
Query: `week` (default `default_week`), `scope`. 200:
```json
{
  "week": "2026-W43", "scope": "all", "generated_at": "2026-10-26T09:00:00Z",
  "snapshot": {"id": 8, "fetched_at": "2026-10-26T09:05:00Z", "status_at_fetch": "settling", "run_id": 8, "row_count": 3, "stale": false},
  "rows": [
    {"source": "cloudrun", "project": "project-a", "service": "svc-a", "c2xx": 1000, "c3xx": 0, "c4xx": 12, "c5xx": 1, "groups": null, "occurrences": null, "by_code": {"200": 1000, "404": 11, "401": 1, "502": 1}, "logs_url": "https://console.cloud.google.com/logs/query;query=…;timeRange=…?project=project-a", "gap": null, "hint": null, "provenance": {…}},
    {"source": "error_reporting", "project": "project-a", "service": "svc-a", "c2xx": null, "c3xx": null, "c4xx": null, "c5xx": null, "groups": 3, "occurrences": 40, "gap": null, "hint": null, "provenance": {…}},
    {"source": "load_balancer", "project": "project-a", "service": "", "c2xx": null, "c3xx": null, "c4xx": null, "c5xx": null, "groups": null, "occurrences": null, "gap": "logging disabled", "hint": "enable logging on the backend service (sample rate >= 0.1)", "provenance": {…}}
  ],
  "totals": {"c2xx": 1000, "c3xx": 0, "c4xx": 12, "c5xx": 1, "groups": 3, "occurrences": 40}
}
```
- `source` ∈ `cloudrun | error_reporting | load_balancer | nginx` (`gcpsource.ErrSrc*`). Counts are `null` exactly when `gap` is non-null (or the source does not produce that count: `groups`/`occurrences` only on `error_reporting`, `c*xx` only on the others); an explicit `0` is a real zero.
- `rows` are the latest error batch (highest `run_id`) of the week, filtered to `scope` by `project`, stored order. `snapshot: null` and `rows: []` when never fetched. `totals` sums non-null values over the returned rows (`null` per field when no row has it; all `null` when `rows` is empty).

- `by_code` (cloudrun rows only, else `null`): exact HTTP status code -> request count for the week, from Cloud Monitoring `run.googleapis.com/request_count` grouped by `metric.labels.response_code` (as well as `response_code_class`); stored in `error_rows.by_code_json` (migration `0003_error_codes.sql`, NULL for batches fetched before it). The per-project totals row (`service: ""`) sums its services. Codes sum to the class counts.
- `logs_url` (cloudrun rows with a service and no gap, else `null`): a Logs Explorer deep link built at request time: `https://console.cloud.google.com/logs/query;query=<url-encoded filter>;timeRange=<week start>%2F<week end>?project=<project>`, filter `resource.type="cloud_run_revision" AND resource.labels.service_name="<svc>" AND httpRequest.status>=400`.
- Triage (additive; `errorsrc.Triage`, constants documented in `internal/errorsrc/triage.go`): top-level `summary` `{needs_attention, watch, noise, healthy, headline}` (counts add up to `rows.length`; gap rows count as `healthy`), and per row `verdict` (`needs_attention|watch|noise|healthy`), `reasons` (plain-language strings), `rank` (1 = look first: needs_attention, then watch, then noise, each by 5xx count desc, Error Reporting rows by occurrences; healthy = 0), `rates` `{requests, rate_5xx, rate_client_4xx, rate_404}` (fractions 0..1, `null` when requests unknown/zero; the last two also `null` without `by_code`) and `change` `{prev_week, prev_5xx, prev_client_4xx, prev_by_code, ratio_5xx}` — compared with the same source+project+service in the latest stored batch of the ISO week before the requested one; `null` when that week or service has no stored row (always `null` for `error_reporting`).
  - needs_attention: 5xx >= 5 AND rate_5xx >= 0.5 %; OR prev_5xx > 0 AND 5xx >= 3x prev_5xx AND 5xx >= 5. watch: non-404 client errors (400,401,403,405,422,429 from `by_code`) >= 25 or rate >= 2 %; or any 5xx below the attention bar; or 4xx+5xx >= 2x previous week and >= 50. noise: only 404 elevated (>= 50 or rate >= 5 %). Otherwise healthy. Error Reporting: occurrences > 0 watch; occurrences >= 50 or groups >= 5 needs_attention. Rows without `by_code` are judged on class counts and say so in `reasons`.
- How Cloud Run counts arise: `c4xx`/`c5xx` count EVERY 4xx/5xx HTTP response the service sent, including internet scanners probing `/.git/config`, `/.env`, `/wp-*`; 5xx are the application-failure signal. `error_reporting` rows count grouped application exceptions (separate source).

### `GET /api/v1/errors/samples`
Query: `week` (default `default_week`), `service` (required, Cloud Run service), `project` (required when several projects are configured; must be a configured project), `min_status` (100..599, default 400), `limit` (1..200, default 50, max entries returned), `focus` (`all` default | `server` = status>=500 | `client` = 400/401/403/405/422/429 | `notfound` = 404; applied in the Cloud Logging filter so the 1,000-entry budget is spent on that class; `min_status` applies only to `all`; unknown value -> `400 invalid_request`). On-demand read of Cloud Logging (raw Logging API `ListLogEntries`, newest first; `httpRequest.requestUrl` stays a string so one malformed scanner URL never fails the read) for the week window; **sampled from the most recent entries in the window, at most 1,000 scanned** — counts describe the sample, not the whole week. 200:
```json
{
  "week": "2026-W42", "project": "project-a", "service": "svc-a", "min_status": 400, "generated_at": "…",
  "logs_url": "https://console.cloud.google.com/logs/query;query=…",
  "focus": "all", "unparsed": 0, "scanned": 1000, "sampled": true, "note": "Sampled from the most recent entries in the window …",
  "out_of_retention": false, "retention_note": "",
  "status_counts": {"404": 960, "401": 30, "502": 10},
  "top_paths": [{"path": "/.git/config", "status": 404, "count": 120, "scanner_like": true}],
  "scanner_share_4xx": 0.93,
  "heuristic_note": "scanner_like is a path-pattern heuristic …",
  "entries": [{"timestamp": "…", "status": 404, "method": "GET", "path": "/.env", "user_agent": "…", "remote_ip": "203.0.113.0/24", "latency_ms": 4.2, "trace": "projects/…/traces/…", "scanner_like": true}]
}
```
- `focus` echoes the request. `unparsed` = scanned entries whose request URL was malformed (e.g. `/%u002f/.env`): they are kept with a best-effort path (percent-decoded when possible, else raw; query dropped; truncated to 200 chars) and counted here.
- `top_paths`: top 10 (path, status) groups by count, query string stripped. `entries`: up to `limit`, newest first; `remote_ip` is truncated to the network (IPv4 /24, IPv6 /48), never a full address.
- `scanner_like` is a **heuristic** (`errorsrc.LooksLikeScanner`; lists `ScannerPrefixes`/`ScannerSuffixes`: dotfiles other than `.well-known`, `/wp-*`, `*.php`, `*.bak`, `/cgi-bin`, `/admin`, `/phpmyadmin` …). `scanner_share_4xx` = scanner-like fraction of scanned 4xx entries (`null` when none).
- Cloud Logging's `_Default` bucket keeps logs 30 days: a week that ended more than 30 days ago answers 200 with `out_of_retention: true`, `retention_note` set and no query. An in-retention week with no matching entries has `scanned: 0`.
- Errors: `400 invalid_request` (missing `service`/`project`, bad `min_status`/`limit`), `400 invalid_scope` (unknown project), `400 invalid_week`, `400 week_out_of_range`; Logging failures keep the envelope with a hint: `403 permission_denied` (the only case whose hint names `roles/logging.viewer`), `503 api_disabled`, `429 quota`, `502 samples_failed` (any other failure: plain message, no role hint); `503 samples_unavailable` when no Logging client could be built.

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

`/api/v1/cost/by-product`, `/api/v1/cost/by-environment`, `/api/v1/attribution/*`, `/api/v1/targets`, `/api/v1/rightsizing`, `/api/v1/vendors/*`. Their shapes are frozen in this file when their phase starts.

## Alerts additions (FROZEN 2026-10-27)

Go types: `internal/alerting/types.go` (JSON tags are the wire format); TS types: `web/src/lib/types.ts` (Alerts section); examples: `web/src/lib/fixtures3.ts`. Money is INR (`config.currency`). Resource names are full GCP names (`projects/proj-a/alertPolicies/123`, `billingAccounts/BILLING_ACCOUNT_ID/budgets/uuid`); in a URL path they are passed as `{project}/{id}`. All lists return `[]`, never `null`; absent single values are `null`.

### Credentials and error codes
- **Reader** credentials (`cloud0-reader`) serve every `GET` below except budgets. **Alerts** credentials (`CLOUD0_ALERTS_CREDENTIALS`, SA `cloud0-alerts`) are used ONLY by `apply`, `GET /budgets`, `GET /budgets/suggestions` (reads billing account budgets) and everything that creates/changes/deletes. `preview` for policy/uptime/snooze/channel kinds works with reader credentials (it only reads); `preview` for `budget` and every `apply` need alerts credentials.
- When `CLOUD0_ALERTS_CREDENTIALS` is unset those endpoints return `503 alerts_not_configured`, hint `Set CLOUD0_ALERTS_CREDENTIALS to the cloud0-alerts service-account key path and restart — see README → Alerts setup`.

| HTTP | `code` | When |
|---|---|---|
| 400 | `invalid_params` | template params out of range / unknown template / bad draft / bad routing; `message` names the key |
| 400 | `confirm_required` | `apply` of a `delete` action without `"confirm_delete": true` |
| 403 | `permission_denied` | GCP 403; `hint` names the role, e.g. `Grant roles/monitoring.alertPolicyEditor to the cloud0-alerts service account on proj-a` (policies), `roles/monitoring.notificationChannelEditor` (channels), `roles/monitoring.uptimeCheckConfigEditor` (uptime), `roles/monitoring.snoozeEditor` (snooze), `roles/billing.costsManager` on the billing account (budgets), `roles/monitoring.viewer` (reads) |
| 404 | `not_found` | unknown policy/check/budget/snooze/channel, unknown template id in a path |
| 409 | `preview_stale` | unknown/expired token (TTL 10 min), or the live resource changed since the preview (re-read hash differs); hint `Run preview again and review the new diff.` Nothing is applied. |
| 502 | `upstream_error` | other GCP API failure; message carries the GCP status, no secrets |
| 503 | `alerts_not_configured` | see above |
| 503 | `api_disabled` | `monitoring.googleapis.com` / `billingbudgets.googleapis.com` not enabled; hint with the enable command |

The existing `400 invalid_request` (malformed JSON/missing field) and `405` still apply.

### `GET /api/v1/alerts/status`
200: `{"alerts_configured": true, "billing_account_configured": true, "projects": ["proj-a"], "generated_at": "2026-10-27T09:00:00Z"}` — `alerts_configured` = credentials env var set; UI uses it to disable Apply buttons with the setup hint. Never errors for missing credentials.

### `GET /api/v1/alerts/channels`
200:
```json
{"channels": [{"name": "projects/proj-a/notificationChannels/111", "project": "proj-a", "type": "email", "display_name": "Alert", "enabled": true,
               "verification_status": "UNVERIFIED", "address": "a***@example.com",
               "warnings": ["Email channel is not verified: notifications may not be delivered. Open the channel in the console and verify the address."]}],
 "descriptors": [{"type": "email", "display_name": "Email", "creatable": true, "hint": "", "labels": [{"key": "email_address", "description": "Email address", "required": true, "sensitive": false}]},
                 {"type": "slack", "display_name": "Slack", "creatable": false, "hint": "Slack channels need console OAuth: create it in Cloud Console → Monitoring → Notification channels, then pick it here.", "labels": []},
                 {"type": "webhook_tokenauth", "display_name": "Webhook (basic auth/token)", "creatable": true, "hint": "", "labels": [{"key": "url", "description": "Webhook URL", "required": true, "sensitive": true}]}],
 "generated_at": "2026-10-27T09:00:00Z"}
```
Channels of every configured project, ordered project, display_name. `verification_status` ∈ `VERIFIED|UNVERIFIED|VERIFICATION_STATUS_UNSPECIFIED`; any email channel whose status is not `VERIFIED` carries the warning above. `address` is masked (email) or `""`. Sensitive label values are never returned. `descriptors` is the union over projects (`creatable:false` for types needing console OAuth, Slack included). Creating a channel is `kind: "channel"` via preview/apply (params = `ChannelDraft`; sensitive labels go in `sensitive_labels`, are kept only in server memory until apply, are shown as `"[redacted]"` in `desired`, and appear nowhere in the DB, logs or change history).

### `GET /api/v1/alerts/policies`
Query: `project` (optional filter), `managed` (`true|false`, optional). 200:
```json
{"policies": [{"name": "projects/proj-a/alertPolicies/222", "project": "proj-a", "display_name": "Zero-Backend CPU Spike", "enabled": true, "severity": "",
   "managed": false, "template_id": "", "instance": "",
   "conditions": [{"display_name": "CPU > 80%", "type": "threshold", "summary": "CPU utilization > 80 % for 5 min on vm-a only", "filter": "metric.type=\"compute.googleapis.com/instance/cpu/utilization\" resource.type=\"gce_instance\"", "comparison": "COMPARISON_GT", "threshold": 0.8, "duration_seconds": 300}],
   "channels": [{"name": "projects/proj-a/notificationChannels/111", "display_name": "Alert", "type": "email", "verification_status": "UNVERIFIED"}],
   "adopt_shape": "zero_cpu_spike", "warnings": ["Covers one VM only; new VMs are not alerted."], "updated_at": null, "raw_json": {"name": "projects/proj-a/alertPolicies/222", "displayName": "Zero-Backend CPU Spike"}}],
 "projects_failed": [{"project": "proj-b", "code": "permission_denied", "message": "…", "hint": "Grant roles/monitoring.viewer …"}],
 "generated_at": "2026-10-27T09:00:00Z"}
```
`managed` = `userLabels["managed-by"] == "cloud0"`; then `template_id` = `userLabels["cloud0-template"]`, `instance` = `userLabels["cloud0-instance"]` (else `""`). `threshold` is the raw condition value (ratio 0.8 for utilisation metrics). `severity` `""` when unset. `adopt_shape` ∈ `""|zero_cpu_spike|high_memory_load|uptime_backend_down|uptime_down_2h`, detected for unmanaged policies by display name AND condition signature (case-insensitive: "zero-backend cpu spike", "high memory load", "smalltech backend down", "smalltech down for 2 hours"). `warnings` carries lint (e.g. `High memory load` has `duration 0 s: fires on a single sample, not 15 min`). Per-project failures never fail the request.

### `GET /api/v1/alerts/policies/{project}/{id}`
200: one `PolicyView` (same object). 404 `not_found`.

### `POST /api/v1/alerts/preview`
Body = `PreviewRequest`:
```json
{"kind": "policy", "action": "create", "target": "", "project": "proj-a", "template_id": "vm_cpu_high", "params": {"threshold": 80, "duration_seconds": 300}}
```
- `kind` ∈ `policy|uptime_check|budget|snooze|channel`; `action` ∈ `create|update|delete|enable|disable|end` (`enable|disable` policy only, `end` snooze only).
- `policy`: create from a template needs `project`, `template_id`, `params` (`TemplateParams`, omitted keys = defaults). `update` of a managed policy: `target` = policy name + `params` (template taken from its label). `update` of an unmanaged policy: `target` + `desired` (raw AlertPolicy REST JSON; cloud0 never templates it). `delete|enable|disable`: `target` only. Channels on a templated policy = `params.channel_override` else the routing for the effective severity (§Routing) resolved to the policy's project; none resolvable → warning `No notification channel resolved for severity ERROR in proj-a: the policy would notify nobody.`
- `uptime_check`: `create` (`project`, `params` = `UptimeDraft`), `update` (`target`, `params`), `delete` (`target`). Unmanaged checks may be updated/deleted (explicit target) — templates never touch them.
- `budget`: `create` / `update` (`target` = budget name) with `params` = `BudgetDraft`, `delete` (`target`). Channels = `channel_override` else routing for `severity` (default `ERROR`), `default_iam_recipients` default true; thresholds default `[{50,current_spend},{80,current_spend},{100,current_spend},{100,forecasted_spend}]`.
- `snooze`: `create` (`project`, `params` = `SnoozeDraft`), `end` (`target` = snooze name; sets end time to now).
- `channel`: `create` (`params` = `ChannelDraft`), `delete` (`target`).
- Exactly one of `params`/`desired` per request; both → `400 invalid_params`.

200 = `Preview`:
```json
{"preview_token": "pv_3f9a…", "expires_at": "2026-10-27T09:10:00Z", "kind": "policy", "action": "create", "target": "",
 "current": null,
 "desired": {"displayName": "cloud0: VM CPU high", "severity": "WARNING", "enabled": true, "combiner": "OR", "userLabels": {"managed-by": "cloud0", "cloud0-template": "vm_cpu_high"}, "conditions": ["…"], "notificationChannels": ["projects/proj-a/notificationChannels/111"]},
 "diff": {"changes": [{"path": "displayName", "op": "add", "before": null, "after": "cloud0: VM CPU high"}], "unified": "+ displayName: cloud0: VM CPU high\n…"},
 "gcloud": "gcloud alpha monitoring policies create --project=proj-a --policy-from-file=policy.json  # policy.json = the `desired` object above",
 "warnings": [], "requires_confirm": false}
```
`current`/`desired` are REST-shaped JSON (null on create / delete). `diff.changes` is a flat path list (`conditions[0].conditionThreshold.thresholdValue`), `op` ∈ `add|remove|change`. `requires_confirm` is true for `delete`. Managed display names are `cloud0: <Template title>` (+ ` — <instance>` when an instance is set). The token is `pv_` + hex of SHA-256 over (kind, action, target, canonical desired JSON, hash of the live resource's canonical JSON); the server keeps (request, desired, hash) in memory for 10 min (restart invalidates). Nothing in GCP changes.

### `POST /api/v1/alerts/apply`
Body `{"preview_token": "pv_3f9a…", "confirm_delete": false}`. The server re-reads the live resource, recomputes the hash, and if it differs from the preview's → `409 preview_stale`. A `delete` preview without `"confirm_delete": true` → `400 confirm_required` (hint `Resend with confirm_delete: true after reviewing the delete preview.`). Success writes one `change_history` row (before = live before, after = resulting resource, `preview_hash` = the token hash; secrets redacted), then 200 = `ApplyResult`:
```json
{"ok": true, "kind": "policy", "action": "create", "target": "projects/proj-a/alertPolicies/333", "resource": {"name": "projects/proj-a/alertPolicies/333", "displayName": "cloud0: VM CPU high"},
 "change_history_id": 12, "applied_at": "2026-10-27T09:02:00Z", "warnings": []}
```
`resource` is null after delete. A token is single-use (second apply → `409 preview_stale`). GCP failure → `403 permission_denied` / `502 upstream_error` and NO history row.

### `GET /api/v1/alerts/templates`
200:
```json
{"templates": [{"id": "vm_cpu_high", "kind": "metric_threshold", "title": "VM CPU high", "description": "CPU utilisation above the threshold on any Compute Engine VM, including future ones.",
   "severity": "WARNING", "default_enabled": true,
   "param_specs": [{"key": "threshold", "label": "CPU %", "type": "number", "unit": "%", "min": 1, "max": 100, "options": [], "help": ""},
                   {"key": "duration_seconds", "label": "For", "type": "int", "unit": "seconds", "min": 60, "max": 86400, "options": [], "help": ""},
                   {"key": "severity", "label": "Severity", "type": "enum", "unit": "", "min": null, "max": null, "options": ["CRITICAL", "ERROR", "WARNING"], "help": ""}],
   "defaults": {"threshold": 80, "duration_seconds": 300, "severity": "WARNING", "enabled": true},
   "params": null, "installed": [],
   "covered_resources": [{"project": "proj-a", "resource_type": "vm", "name": "vm-a", "covered": true, "note": ""}]}],
 "generated_at": "2026-10-27T09:00:00Z"}
```
All 13 templates in `AllTemplateIDs` order. `params` = effective params of the installed managed policy (from its labels/conditions; `null` if not installed); `installed` = names of its managed policies. `covered_resources` = live inventory with `covered:false` + `note` for skipped resources (VM without Ops Agent for memory/disk; Cloud SQL shared-core for `sql_memory_high`).

Frozen defaults (`defaults`, `severity`, metric basis) — builders and UI follow this table; thresholds are percents/counts in params and converted to ratios by the builder:

| id | kind | severity | defaults | basis |
|---|---|---|---|---|
| `vm_cpu_high` | metric_threshold | WARNING | 80 %, 300 s | `compute.googleapis.com/instance/cpu/utilization` (all `gce_instance`) |
| `vm_memory_high` | metric_threshold | ERROR | 85 %, 900 s | `agent.googleapis.com/memory/percent_used` state=used (Ops Agent; skips VMs without it) |
| `vm_disk_high` | metric_threshold | ERROR | 85 %, 300 s | `agent.googleapis.com/disk/percent_used` state=used |
| `run_5xx_burst` | metric_threshold | ERROR | > 5 in `window_seconds` 600 | `run.googleapis.com/request_count` response_code_class=5xx, ALIGN_SUM over window, all `cloud_run_revision` |
| `run_5xx_rate` | metric_ratio | ERROR | 2 %, 600 s | numerator 5xx request_count / denominatorFilter all request_count |
| `run_error_logged` | log_match | WARNING | rate limit 3600 s | `conditionMatchedLog` `resource.type="cloud_run_revision" AND severity>=ERROR`; title "Application error logged" (the Monitoring API has no "new error group" condition) |
| `lb_5xx_burst` | metric_threshold | ERROR | > 5 in 600 s | `loadbalancing.googleapis.com/https/request_count` response_code_class=500 |
| `sql_down` | metric_threshold | CRITICAL | `database/up` < 1 for 180 s | `cloudsql.googleapis.com/database/up` |
| `sql_cpu_high` | metric_threshold | WARNING | 80 %, 300 s | `database/cpu/utilization` |
| `sql_memory_high` | metric_threshold | ERROR | 85 %, 300 s | `database/memory/total_usage` ÷ `quota` ratio (denominatorFilter); shared-core tiers skipped with hint |
| `sql_disk_high` | metric_threshold | ERROR | 85 %, 300 s | `database/disk/utilization` |
| `uptime_down` | uptime | CRITICAL | 600 s (`instance` `warning`, severity WARNING) and 7200 s (`instance` `critical`, severity CRITICAL) | `monitoring.googleapis.com/uptime_check/check_passed` for every check (or `uptime_check_ids`) |
| `run_401_403_spike` | metric_threshold | WARNING | > 50 in 600 s, **`enabled:false`** | request_count response_code 401/403 (opt-in; 4xx never alerts by default) |

`params` validation (`400 invalid_params`): `threshold` within the spec min/max; `duration_seconds` 60..86400 multiple of 60; `window_seconds` 60..3600; `rate_limit_seconds` 300..86400; `severity` ∈ CRITICAL|ERROR|WARNING; unknown keys rejected.

### Uptime checks
`GET /api/v1/alerts/uptime-checks` → `{"checks": [{"name": "projects/proj-a/uptimeCheckConfigs/abc", "project": "proj-a", "display_name": "api health", "host": "api.example.com", "path": "/health", "port": 443, "use_ssl": true, "period_seconds": 60, "timeout_seconds": 10, "content_match": "", "managed": false, "raw_json": {…}}], "generated_at": "…"}`. Create/update/delete: preview/apply `kind: "uptime_check"`, `params` = `UptimeDraft` `{project, display_name, host, path, port, use_ssl, period_seconds (60|300|600|900), timeout_seconds (1–60), content_match}` (validated; `host` hostname only, no scheme). Created checks get userLabel `managed-by=cloud0`.

### `GET /api/v1/alerts/snoozes`
200: `{"snoozes": [{"name": "projects/proj-a/snoozes/s1", "project": "proj-a", "display_name": "deploy window", "policy_names": ["projects/proj-a/alertPolicies/333"], "start_time": "…", "end_time": "…", "state": "active"}], "generated_at": "…"}` (`state` `active|scheduled|expired`; newest first). Create/end via preview/apply `kind: "snooze"` (`SnoozeDraft`: `duration_seconds` 300..2592000, `policy_names` non-empty and in the project).

### `GET /api/v1/alerts/incidents`
Query: `state` = `open` (default) | `closed` | `all`; `project`. 200:
```json
{"incidents": [{"id": "0abc", "project": "proj-a", "policy_name": "projects/proj-a/alertPolicies/333", "policy_display_name": "cloud0: VM CPU high", "severity": "WARNING",
   "state": "open", "opened_at": "2026-10-27T07:40:00Z", "closed_at": null, "resource_type": "gce_instance", "resource_name": "vm-a",
   "summary": "CPU utilization above 80 % for 5 min", "url": "https://console.cloud.google.com/monitoring/alerting/alerts/0abc?project=proj-a"}],
 "total": 1, "projects_failed": [], "generated_at": "…"}
```
Ordered by `opened_at` desc. Read from the Monitoring `alerts` API; per-project failure listed in `projects_failed` (same shape as policies).

### `GET /api/v1/alerts/history`
Query: `week` (`WeekID`, default `default_week`). `400 invalid_week`/`week_out_of_range`. 200:
```json
{"week": "2026-W43", "total_opened": 3, "still_open": 1,
 "rows": [{"project": "proj-a", "policy_name": "projects/proj-a/alertPolicies/333", "policy_display_name": "cloud0: VM CPU high", "severity": "WARNING",
           "opened": 3, "still_open": 1, "resources": ["vm-a"], "first_opened_at": "…", "last_opened_at": "…"}],
 "projects_failed": [], "generated_at": "…"}
```
Alerts whose `opened_at` is in the ISO week (Mon 00:00Z..next Mon), grouped by policy; rows ordered `opened` desc then display name.

### `GET /api/v1/alerts/coverage`
200:
```json
{"rows": [{"project": "proj-a", "resource_type": "vm", "resource": "vm-a",
           "signals": [{"signal": "cpu", "covered": true, "policies": ["projects/proj-a/alertPolicies/333"], "reason": ""},
                       {"signal": "memory", "covered": false, "policies": [], "reason": "Ops Agent not installed"},
                       {"signal": "disk", "covered": false, "policies": [], "reason": "Ops Agent not installed"}],
           "hint": "VM vm-a has CPU alert but no memory/disk alert coverage: install the Ops Agent."}],
 "generated_at": "…"}
```
`resource_type` ∈ `vm|cloudsql|cloud_run|lb|uptime_check`; signals per type: vm `cpu|memory|disk`, cloudsql `down|cpu|memory|disk`, cloud_run `errors`, lb `errors`, uptime_check `down`. `covered` requires an ENABLED policy (managed or not) whose condition metric/filter matches the resource; `reason` is `""` when covered. `hint` `""` when nothing to say.

### `GET /api/v1/alerts/routing` · `PUT /api/v1/alerts/routing`
200 (both): `{"routing": {"CRITICAL": ["projects/proj-a/notificationChannels/111"], "ERROR": ["…"], "WARNING": ["…"]}, "defaulted": false, "updated_at": "…"|null, "warnings": ["…UNVERIFIED…"], "channels": [Channel…]}`. Stored in settings `alerts.routing.critical|error|warning` (comma list of channel resource names); when unset the server resolves every severity to the channel(s) whose `display_name == "Alert"` (`defaulted: true`, `updated_at: null`). PUT body `{"routing": {"CRITICAL": […], "ERROR": […], "WARNING": […]}}` — all three keys required, each a list of existing channel names (non-empty; unknown name, unknown severity key or duplicates → `400 invalid_params` naming it; nothing saved). Unverified email channels are allowed but returned in `warnings`. A successful PUT writes a `change_history` row (`kind: "routing"`, `target: "alerts.routing"`). PUT needs no GCP write credentials (local setting) but reads channels with reader credentials.

### Budgets
`GET /api/v1/budgets` (alerts credentials) → 
```json
{"billing_account": "BILLING_ACCOUNT_ID", "currency": "INR",
 "budgets": [{"name": "billingAccounts/BILLING_ACCOUNT_ID/budgets/uuid-1", "display_name": "Example-budget", "managed": false, "amount_kind": "specified", "amount": 5000, "currency": "INR",
   "projects": ["proj-a"], "service_ids": [], "thresholds": [{"percent": 50, "basis": "current_spend"}, {"percent": 90, "basis": "current_spend"}, {"percent": 100, "basis": "current_spend"}],
   "channel_names": [], "default_iam_recipients": true, "adopt_shape": "budget_account", "warnings": ["No monitoring notification channels: alerts go to billing admins by email only."], "raw_json": {…}}],
 "generated_at": "…"}
```
`managed` = display name starts with `cloud0-` (Budgets API has no labels). `adopt_shape` `budget_account` for any unmanaged budget. `projects: []` = whole billing account; `service_ids` like `services/6F81-5844-456A`.

`GET /api/v1/budgets/suggestions` → `{"currency": "INR", "suggestions": [{"scope": "account", "service_id": "", "service_name": "All services", "amount": 6480, "basis": {"method": "snapshots_avg", "weeks_used": 4, "avg_weekly": 1260, "multiplier": 1.2, "explanation": "1.2 × average weekly net cost of the last 4 complete weeks (1,260.00) × 30/7"}, "draft": {"display_name": "cloud0-account-monthly", "amount": 6480, "projects": [], "service_ids": [], "thresholds": […4 defaults…], "severity": "ERROR"}, "exists": false}, {"scope": "service", "service_id": "services/6F81-5844-456A", "service_name": "Compute Engine", …}]}`. Account amount = setting `target.monthly_total` when set (`method: "monthly_total_setting"`, multiplier 1), else the formula from the latest ≤ 4 complete weekly snapshots (all projects, net, stored data only; fewer than 1 week → `method: "none"`, `amount: 0`, explanation says refresh first). Service suggestions for the services Compute Engine, Networking, Cloud SQL, Cloud Run, Gemini API (service ids from `snapshot_rows.service_id`; a service with no id/data is omitted). Create/update/delete: preview/apply `kind: "budget"`; update/delete/adopt of an unmanaged budget need an explicit `target`. Budgets calls use the main project as quota/user project.

### `POST /api/v1/alerts/adopt/preview`
Body `{"shape": "zero_cpu_spike", "target": "projects/proj-a/alertPolicies/222"}` (`target` optional when exactly one policy has that `adopt_shape`; shapes: `zero_cpu_spike`, `high_memory_load`, `uptime_backend_down`, `uptime_down_2h`, `budget_account`). 200 = `AdoptPreview`:
```json
{"shape": "zero_cpu_spike", "replaces": ["projects/proj-a/alertPolicies/222"],
 "previews": [{"preview_token": "pv_…", "kind": "policy", "action": "create", "…": "full Preview object for the managed vm_cpu_high policy (80 %, 300 s)"}],
 "follow_up": [{"action": "disable", "kind": "policy", "target": "projects/proj-a/alertPolicies/222", "note": "Disable the old policy only after the replacement is applied; cloud0 never deletes it for you."}],
 "notes": ["Replacement covers every VM, not just vm-a."]}
```
Mapping: `zero_cpu_spike` → `vm_cpu_high` (80 %, 300 s); `high_memory_load` → `vm_memory_high` (85 %, 900 s — a true 15 min); `uptime_backend_down` and `uptime_down_2h` → BOTH `uptime_down` previews (`instance: warning` 600 s WARNING, `instance: critical` 7200 s CRITICAL; same two previews whichever uptime shape is used, `replaces` lists both old policies when both exist); `budget_account` → a `budget create` preview `cloud0-<slug>` copying amount/filter/thresholds of the unmanaged budget plus routed channels. Each preview is applied independently via `/apply`; an already-managed equivalent yields a preview with `action: "update"`. The disable of the old resource is a separate normal preview/apply (`action: "disable"`). Unknown shape → `400 invalid_params`; no matching resource → `404 not_found`.

### `GET /api/v1/history`
Query: `kind` (`policy|uptime_check|budget|snooze|channel|routing`, optional), `limit` (default 50, max 500). 200: `{"entries": [{"id": 12, "at": "…", "actor": "local-user", "kind": "policy", "action": "create", "target": "projects/proj-a/alertPolicies/333", "before": null, "after": {…}, "preview_hash": "…"}], "limit": 50}`; newest first; payloads redacted of credential-like keys; invalid kind or limit → `400 invalid_request`. No write endpoints (append-only).

## Attribution additions (FROZEN 2026-10-27)

Phase 3: attribution rules, product/environment breakdowns, label suggestions, targets, rightsizing, optional LLM rule proposer. Same envelope, `WeekID`/`Scope` handling and error codes as above. Go types: `internal/attribution/types.go`; TypeScript: `web/src/lib/attributionTypes.ts`. Money is in `config.currency` (INR). Everything is computed at read time from the **latest snapshot of the requested week** (`week` default `default_week`; `scope` default `all`); a week with no snapshot answers 200 with `snapshot: null` and empty `groups`/`items`/`candidates` (never zeros, never 404), except `POST /attribution/explain` (404).

New error codes:

| HTTP | `code` | When |
|---|---|---|
| 400 | `invalid_rule` | a rule in `PUT /attribution/rules` is invalid (unknown field/op/source, `key` missing for `label` or present otherwise, empty `value`, uncompilable `regex`, action sets nothing, `shared` together with `product`, bad name, > 500 rules); message names the 0-based index; nothing saved |
| 400 | `invalid_allocate` | `allocate` not in `none|even|proportional` |
| 400 | `invalid_target` | target amount not a finite number ≥ 0 (or `null` to clear), unknown product name, bad `month_from` |
| 400 | `invalid_dimension` | `env`/`product` filter on `/cost/by-*` is not a valid name, `(shared)` or `(unattributed)` |
| 503 | `proposer_not_configured` | `POST /attribution/propose` while `CLOUD0_OPENROUTER_API_KEY` is unset; **no outbound call is made**; `hint: "Set CLOUD0_OPENROUTER_API_KEY in .env and restart to enable LLM rule proposals."` |
| 502 | `proposer_failed` | OpenRouter call failed or returned output that fails schema validation; no rules change |
| 503 | `inventory_unavailable` | never returned by `GET /rightsizing` (source failures are reported inside `sources[]`); reserved |

### Semantics (the engine contract)

- **Row input.** Each `snapshot_rows` row: `project_id`, `service`, `sku`, `resource_name`, `resource_global_name`, `labels` (parsed from `labels_json`, a JSON array `[{"key":"env","value":"prod"}]` as produced by BigQuery `TO_JSON_STRING(labels)`; `null` for rows fetched before migration 0005 = no labels known, rules fall back to the other fields).
- **Rule.** `{id, priority, match:{field, op, key?, value}, action:{product?, environment?, shared?}, enabled, source, note?}`.
  - `match.field`: `label` (needs `key`; compares the value of that user label), `project` (`project_id`), `resource_name` (matches `resource_name` **or** `resource_global_name`; a row with neither never matches), `sku` (`sku` description), `service` (`service` description).
  - `match.op`: `equals`, `contains`, `prefix`, `regex`. All comparisons are **case-insensitive** (`regex` is compiled with `(?i)`, Go RE2 syntax, unanchored). `value` is non-empty.
  - `action`: at least one of `product` (name), `environment` (name) or `shared: true`. `shared` and `product` are mutually exclusive. Names match `^[a-z0-9][a-z0-9_-]{0,39}$` after lower-casing/trimming.
  - `source`: `seed` (created by the seeder), `user` (hand-written), `proposed` (accepted LLM proposal). `priority`: integer ≥ 0; lower runs first; ties broken by `id`.
- **Evaluation.** Built-in label rules run first (rule id `0`, shown as `builtin`): user label `product` (value `shared` → shared; else the product), and `env` or `environment` (the environment). Then enabled rules in `(priority, id)` order. **Product and environment resolve independently:** the first matching rule that carries a `product` (or `shared`) decides the product/shared bucket; the first matching rule that carries an `environment` decides the environment; evaluation stops once both are decided. No decision → product `(unattributed)`, environment `(no environment)`. Result: `{product, environment, shared, rule_id, environment_rule_id, evidence[]}`; `product` is `""` when shared or unattributed; `rule_id` is the deciding product/shared rule (`0` builtin, `null` none). `evidence` is human strings such as `label env=prod`, `resource_name contains "example-web"`.
- **Seeder tiers** (priority ranges, so the precedence "labels > project > name tokens > service/SKU fallbacks" is data, not code): `100–199` label value→product/env maps (incl. aliases), `200–299` project defaults, `300–599` resource-name tokens (product and environment tokens `prod|production|staging|stage|dev|beta|test`), `600–899` SKU/service rules for resource-less shared costs, `900–999` shared fallbacks (e.g. `service equals "VM Manager"`, forwarding-rule SKUs, Networking minimum charges, Artifact Registry storage).
- **Buckets.** Group kinds: `product`, `environment`, `shared` (label `(shared)`), `unattributed` (label `(unattributed)` for products, `(no environment)` for environments). Shared and unattributed are never merged.
- **Coverage.** `attributed_pct` = 100 × net of rows with a product ÷ total net (shared excluded from the numerator); `shared_pct`; `unattributed_pct`; the three sum to 100 (± 0.01); all `0` when total net is 0.

### `GET /api/v1/attribution/rules` · `PUT /api/v1/attribution/rules`
GET 200:
```json
{
  "rules": [
    {"id": 1, "priority": 100, "match": {"field": "label", "op": "equals", "key": "app", "value": "example-alias"},
     "action": {"product": "example-product"}, "enabled": true, "source": "seed", "note": null},
    {"id": 2, "priority": 310, "match": {"field": "resource_name", "op": "contains", "value": "example-product"},
     "action": {"product": "example-product"}, "enabled": true, "source": "seed", "note": null},
    {"id": 3, "priority": 320, "match": {"field": "resource_name", "op": "regex", "value": "(^|[-_/])prod($|[-_/])"},
     "action": {"environment": "prod"}, "enabled": true, "source": "seed", "note": null},
    {"id": 4, "priority": 900, "match": {"field": "service", "op": "equals", "value": "VM Manager"},
     "action": {"shared": true}, "enabled": true, "source": "user", "note": "OS patch management, not per product"}
  ],
  "allocate": "none",
  "aliases": {"example-alias": "example-product"},
  "proposer": {"enabled": false, "model": null},
  "updated_at": "2026-10-27T09:00:00Z"
}
```
`allocate` is the persisted default (`attribution.allocate`), `aliases` the persisted seeder alias map (`attribution.seed.aliases`, JSON object alias→product), `proposer.enabled` is true iff `CLOUD0_OPENROUTER_API_KEY` is set (`model` = `CLOUD0_LLM_MODEL` or the default, else `null`). With no rules stored: `rules: []`, `updated_at: null`. Rules sorted by `(priority, id)`.

PUT body `{"rules": [Rule…], "allocate"?: "none|even|proportional", "aliases"?: {…}}` **replaces the whole list atomically**. A rule with `id` > 0 that exists keeps its id; `id` omitted/`0` is created (new id); stored ids absent from the body are deleted. Omitted `allocate`/`aliases` are left unchanged. Validation as in `invalid_rule` / `invalid_allocate`. 200: the GET body. Attribution is recomputed on the next read; snapshots are never modified. Writes are local (no GCP calls, no `change_history` row).

### `POST /api/v1/attribution/seed/preview` · `POST /api/v1/attribution/seed/apply`
Body (both): `{"week": "2026-W43", "scope": "all", "aliases": {"example-alias": "example-product"}, "shared_tokens": ["example-shared-db"]}` — all optional (`week` default `default_week`; `aliases` default the stored map; `shared_tokens` = resource-name substrings that are shared, default `[]`). The seeder derives rules from the **latest snapshot rows of that week** (names, labels, projects, resource-less SKUs) and is deterministic.

Preview 200 (writes nothing):
```json
{
  "week": "2026-W43", "snapshot": {"id": 13, "fetched_at": "2026-10-26T09:05:00Z", "status_at_fetch": "settling", "run_id": 8, "row_count": 2345, "stale": false},
  "labels_captured": true,
  "proposed": [{"id": null, "priority": 310, "match": {"field": "resource_name", "op": "contains", "value": "example-product"}, "action": {"product": "example-product"}, "enabled": true, "source": "seed", "note": "7 resources, 412.30 net"}],
  "counts": {"rules": 14, "label": 3, "project": 0, "resource_name": 9, "sku_service": 2, "products": 6, "environments": 3, "kept_user_rules": 1, "replaced_seed_rules": 12},
  "coverage": {"total_net": 2012.5, "attributed_net": 1850.0, "shared_net": 90.0, "unattributed_net": 72.5, "attributed_pct": 91.9, "shared_pct": 4.5, "unattributed_pct": 3.6},
  "coverage_before": {"total_net": 2012.5, "attributed_net": 1200.0, "shared_net": 0, "unattributed_net": 812.5, "attributed_pct": 59.6, "shared_pct": 0, "unattributed_pct": 40.4},
  "products": [{"product": "example-product", "net": 412.3, "resources": 7}]
}
```
`proposed` is the complete resulting **seed** rule set (not user/proposed rules). `coverage` is what the week would show with the user's existing enabled user/proposed rules plus `proposed`; `coverage_before` with the currently stored rules. `replaced_seed_rules` = stored `source=seed` rules the apply would delete; `kept_user_rules` = stored non-seed rules kept untouched. `labels_captured` false when the snapshot predates migration 0005 (hint in `warnings`: `["Labels not captured for this week; refresh it to enable label rules."]`, otherwise `[]`; field `warnings` is always present).

Apply 200: `{"applied": true, "rules": [Rule…stored…], "counts": {…as above…}, "coverage": {…}}`; it deletes all stored `source=seed` rules, inserts `proposed` with new ids (in one transaction), persists `aliases` when supplied, and leaves `user`/`proposed` rules unchanged. Apply is idempotent for unchanged input. `400 invalid_week|week_out_of_range|invalid_scope|invalid_request`. A week with no snapshot → `200` with `proposed: []`, `snapshot: null`, zero counts, and `warnings: ["No snapshot for this week — Refresh first."]` (apply then changes nothing: `applied: false`).

### `GET /api/v1/attribution/unattributed`
Query: `week`, `scope`, `limit` (default 100, max 1000). 200:
```json
{
  "week": "2026-W43", "scope": "all", "snapshot": {"id": 13, "…": "SnapshotRef"}, "labels_captured": true,
  "total_net": 2012.5, "unattributed_net": 72.5, "unattributed_pct": 3.6, "item_count": 9, "truncated": false,
  "items": [
    {"kind": "resource", "key": "example-orphan-vm", "project_id": "proj-a", "service": "Compute Engine",
     "resource_name": "example-orphan-vm", "resource_global_name": "//compute.googleapis.com/projects/proj-a/zones/zone-a/instances/example-orphan-vm",
     "sku": null, "net": 31.2, "share_pct": 1.55, "line_items": 168, "reason": "no_matching_rule",
     "suggested": {"product": "example-orphan", "environment": null, "confidence": "low", "basis": "name token 'example-orphan'"},
     "evidence": ["resource_name has no label product/env", "no enabled rule matched"]},
    {"kind": "sku", "key": "Network Inter Region Egress", "project_id": "proj-a", "service": "Networking",
     "resource_name": null, "resource_global_name": null, "sku": "Network Inter Region Egress", "net": 12.1, "share_pct": 0.6, "line_items": 40,
     "reason": "no_resource_name", "suggested": null, "evidence": ["billing line has no resource name; add a service/SKU rule or mark it shared"]}
  ]
}
```
Items = rows whose product is unresolved (`product == ""` and not shared), grouped: rows with a resource → `kind: "resource"` grouped by `(project_id, resource_name)` summing across services/SKUs (`service` is the highest-net service, `sku` null); rows without resource → `kind: "sku"` grouped by `(project_id, service, sku)`. Sorted by `net` descending then `key`. `reason` ∈ `no_resource_name | no_matching_rule | environment_only` (an environment rule matched but no product). `suggested` is `null` or `{product, environment, confidence: "high|medium|low", basis}` from the seeder heuristics (aliases applied); `evidence` explains why the row is unattributed. Σ `net` of all items (before `limit`) = `unattributed_net`.

### `POST /api/v1/attribution/explain`
Body `{"resource": "example-web", "week": "2026-W43", "scope": "all"}` (`resource` required: matched against `resource_name` or `resource_global_name`, case-insensitive exact, else a unique suffix match on `resource_global_name`; `week`, `scope` optional). 200:
```json
{
  "week": "2026-W43", "resource": "example-web", "matches": 2,
  "results": [{
    "project_id": "proj-a", "service": "Cloud Run", "resource_name": "example-web", "resource_global_name": "//run.googleapis.com/…",
    "labels": {"env": "staging"}, "net": 18.4,
    "evaluation": {"product": "example-product", "environment": "staging", "shared": false, "rule_id": 2, "environment_rule_id": 0, "evidence": ["resource_name contains \"example-product\"", "label env=staging"]},
    "trail": [
      {"rule_id": 0, "builtin": true, "description": "label product", "matched": false, "applied": null, "reason": "no label 'product'"},
      {"rule_id": 0, "builtin": true, "description": "label env|environment", "matched": true, "applied": "environment", "reason": "label env=staging"},
      {"rule_id": 1, "builtin": false, "priority": 100, "description": "label app equals \"example-alias\" → product example-product", "matched": false, "applied": null, "reason": "no label 'app'"},
      {"rule_id": 2, "builtin": false, "priority": 310, "description": "resource_name contains \"example-product\" → product example-product", "matched": true, "applied": "product", "reason": "resource_name \"example-web\" does not contain it; resource_global_name does"}
    ]
  }]
}
```
`results` = one entry per distinct `(project_id, service)` for the resource in the week's snapshot (a resource billed under several services yields several); `trail` lists every enabled rule in evaluation order up to and including the last deciding rule (`applied`: `product|environment|shared|null` — a matching rule whose dimension was already decided has `matched: true, applied: null`). `404 not_found` when the week has no snapshot or no row matches; `400 invalid_request` when `resource` is empty.

### `GET /api/v1/cost/by-product` · `GET /api/v1/cost/by-environment`
Query: `week`, `scope`, `allocate` (`none|even|proportional`; default the persisted `allocate`, itself default `none`), optional filter `env` (by-product only: only rows resolving to that environment; `(no environment)` allowed) / `product` (by-environment only; `(shared)` and `(unattributed)` allowed). 200 (`by-product` shown):
```json
{
  "dimension": "product", "week": "2026-W43", "week_start": "2026-10-19", "week_end": "2026-10-25", "week_status": "settling",
  "scope": "all", "allocate": "none", "filter": {"env": null, "product": null}, "generated_at": "2026-10-26T09:00:00Z",
  "snapshot": {"id": 13, "fetched_at": "2026-10-26T09:05:00Z", "status_at_fetch": "settling", "run_id": 8, "row_count": 2345, "stale": false},
  "labels_captured": true,
  "columns": [
    {"key": "compute_engine", "label": "Compute Engine", "kind": "service", "services": ["Compute Engine"]},
    {"key": "cloud_run", "label": "Cloud Run", "kind": "service", "services": ["Cloud Run"]},
    {"key": "total", "label": "Total GCP", "kind": "total"}
  ],
  "groups": [
    {"key": "example-product", "label": "example-product", "kind": "product",
     "cells": {"compute_engine": {"net": 300.0, "gross": 300.0, "credits": 0}, "cloud_run": {"net": 112.3, "gross": 120.0, "credits": -7.7}, "total": {"net": 412.3, "gross": 420.0, "credits": -7.7}},
     "direct_net": 412.3, "allocated_net": 0, "net": 412.3, "share_pct": 20.5, "rule_ids": [2, 1]},
    {"key": "(shared)", "label": "(shared)", "kind": "shared", "cells": {"total": {"net": 90.0, "gross": 90.0, "credits": 0}},
     "direct_net": 90.0, "allocated_net": 0, "net": 90.0, "share_pct": 4.5, "rule_ids": [4]},
    {"key": "(unattributed)", "label": "(unattributed)", "kind": "unattributed", "cells": {"total": {"net": 72.5, "gross": 72.5, "credits": 0}},
     "direct_net": 72.5, "allocated_net": 0, "net": 72.5, "share_pct": 3.6, "rule_ids": []}
  ],
  "total": {"cells": {"compute_engine": {"net": 1191.69, "gross": 1191.69, "credits": 0}, "total": {"net": 2012.5, "gross": 2300.1, "credits": -287.6}}},
  "coverage": {"total_net": 2012.5, "attributed_net": 1850.0, "shared_net": 90.0, "unattributed_net": 72.5, "attributed_pct": 91.9, "shared_pct": 4.5, "unattributed_pct": 3.6},
  "reconciliation": {"cost_table_total": 2012.5, "breakdown_total": 2012.5, "diff": 0, "ok": true},
  "notes": []
}
```
- `columns` = the Cost-table service/total columns of `GET /cost/table` for the same snapshot, **without** `week`, `pct_change` and stub columns (same keys, labels, `extra` rule). `cells` per group have one entry per column key with a non-zero value plus `total` (always present, missing service keys mean 0).
- `groups` sorted: kind `product`/`environment` by `net` desc then key; then `shared`; then `unattributed` last. `shared` and `unattributed` groups are always present (zero cells when empty) when `snapshot` is non-null. `rule_ids` = deciding rule ids ordered by net contribution desc (max 5; `0` = builtin label).
- **Allocation.** `none`: shared stays in its own group. `even`: the shared net (per service column) is split equally across all `product` (by-environment: `environment`) groups that have `direct_net` ≠ 0; `proportional`: split by each group's share of the summed `direct_net` of those groups. Allocation is per service column and for gross/credits too. After allocating: group `allocated_net` = its received amount, `net` = `direct_net + allocated_net`, `cells` include the allocation; the `shared` group keeps `direct_net` = original, `allocated_net` = −`direct_net`, `net` = 0 and zero cells. `unattributed` is never allocated. With no eligible group, nothing is allocated and `notes` gets `"No products to allocate shared cost to."`.
- **by-environment** is identical with `dimension: "environment"` and group `kind: "environment"`; the unresolved bucket is `{"key": "(no environment)", "kind": "unattributed"}` and shared-product rows are placed in the `shared` group regardless of their environment (so Σ groups = total).
- **Reconciliation.** `cost_table_total` = the `total` cell net of `/cost/table` for the same week+scope (with an `env`/`product` filter set it is the filtered net total and `notes` contains `"Filtered view"`); `breakdown_total` = Σ group `net`; `ok` = |diff| ≤ 0.005. Σ over groups of any column cell = the Cost table's cell for that column (unfiltered).
- Errors: `400 invalid_allocate|invalid_dimension|invalid_week|week_out_of_range|invalid_scope`.
- Drill-down: `GET /api/v1/snapshots/{id}/breakdown` additionally accepts `product` and `environment` (names, `(shared)`, `(unattributed)`, `(no environment)`); rows are first filtered through the engine; totals reconcile to the group cell. Invalid names → `400 invalid_dimension`.

### `GET /api/v1/attribution/label-suggestions`
Query: `week`, `scope`, `limit` (default 200, max 1000). 200:
```json
{
  "week": "2026-W43", "scope": "all", "snapshot": {"id": 13, "…": "SnapshotRef"}, "labels_captured": true,
  "count": 2, "done_count": 5,
  "items": [
    {"project_id": "proj-a", "resource_name": "example-vm", "resource_global_name": "//compute.googleapis.com/projects/proj-a/zones/zone-a/instances/example-vm",
     "resource_type": "compute_instance", "region_or_zone": "zone-a", "net": 120.4,
     "labels": {"product": "example-product", "env": "prod"}, "missing": ["product", "env"],
     "basis": {"product_rule_id": 2, "environment_rule_id": 3},
     "command": "gcloud compute instances add-labels example-vm --project=proj-a --zone=zone-a --labels=product=example-product,env=prod",
     "risk": "Metadata-only change; no restart. Label values are visible in billing exports."},
    {"project_id": "proj-a", "resource_name": "example-web", "resource_global_name": "//run.googleapis.com/projects/proj-a/locations/region-a/services/example-web",
     "resource_type": "cloud_run_service", "region_or_zone": "region-a", "net": 18.4,
     "labels": {"product": "example-product"}, "missing": ["product"], "basis": {"product_rule_id": 2, "environment_rule_id": null},
     "command": "gcloud run services update example-web --project=proj-a --region=region-a --update-labels=product=example-product",
     "risk": "Creates a new Cloud Run revision (traffic follows the latest revision if so configured); do it in a quiet window."}
  ]
}
```
- One item per distinct `(project_id, resource_name)` whose product **or** environment was decided by a non-label rule (rule id ≠ 0 / not a label-field rule) and whose label for that dimension is missing; `labels` = only the labels to add (product omitted when product is shared/unattributed or already labelled; env omitted when unresolved or already labelled), `missing` lists them. Resources with both labels present, shared resources, and unresolved ones are not listed. Label values are sanitized to `[a-z0-9_-]{0,63}`; if sanitizing changes the value the item is still emitted. `done_count` = resources in this snapshot already carrying the label(s) that an earlier-seeded rule would have suggested (i.e. attributed via `builtin`); informational.
- `resource_type` ∈ `compute_instance | compute_disk | compute_address | cloud_run_service | cloud_run_job | gcs_bucket | cloud_sql_instance | artifact_repository | unknown`, parsed from `resource_global_name` (`/instances/`, `/disks/`, `/addresses/`, `/services/`, `/jobs/`, `/buckets/`, `sqladmin` `/instances/`, `/repositories/`); `unknown` → `command: null`, `risk` says to label it in the console, `labels` still shown. `region_or_zone` parsed from `/zones/<z>/`, `/locations/<r>/`, `/regions/<r>/`, else `null` (then `command` is `null` for types that need it).
- Command templates (shell-safe: values are validated, names are single-quoted only when they contain characters outside `[A-Za-z0-9._-]`): compute instance `gcloud compute instances add-labels NAME --project=P --zone=Z --labels=…`; disk `gcloud compute disks add-labels …`; Cloud Run service `gcloud run services update NAME --project=P --region=R --update-labels=…`; job `gcloud run jobs update …`; GCS `gcloud storage buckets update gs://NAME --project=P --update-labels=…`; Cloud SQL `gcloud sql instances patch NAME --project=P --update-labels=…` (risk: "Runs a patch operation; confirm no unrelated pending settings changes."); Artifact Registry `gcloud artifacts repositories update NAME --project=P --location=R --update-labels=…`. cloud0 never executes these.

### `GET /api/v1/targets` · `PUT /api/v1/targets`
GET query: `month` (`YYYY-MM`, default current month; `400 invalid_month|month_out_of_range`), `scope` (default `all`). 200:
```json
{
  "month": "2026-10", "month_start": "2026-10-01", "month_end": "2026-10-31", "currency": "INR", "generated_at": "2026-10-26T09:00:00Z",
  "month_status": "partial", "covered_until": "2026-10-25", "share_weeks": ["2026-W41", "2026-W42", "2026-W43"],
  "account": {"product": "*", "target": 25000, "month_from": "2026-09", "net": 18200.5, "projected": 22100.3, "pct": 72.8, "remaining": 6799.5, "status": "on_track"},
  "products": [
    {"product": "example-product", "target": 6000, "month_from": "2026-10", "net": 4100.2, "projected": 4980.0, "pct": 68.3, "remaining": 1899.8, "status": "on_track", "share_pct": 22.5},
    {"product": "example-other", "target": null, "month_from": null, "net": 1200.0, "projected": 1457.0, "pct": null, "remaining": null, "status": "no_target", "share_pct": 6.6}
  ],
  "shared": {"net": 800.0, "projected": 971.0, "share_pct": 4.4},
  "unattributed": {"net": 300.0, "projected": 364.0, "share_pct": 1.6},
  "history": [{"product": "example-product", "month_from": "2026-10", "amount": 6000, "created_at": "2026-10-20T08:00:00Z"}]
}
```
- `account.net`/`projected` come from the month snapshot via `monthcost.SummaryWithSettle` (scope-filtered, the number the Month tab shows); `net` 0 and `status: "no_data"` when the month was never fetched. **Product figures** = `share × account` where `share` = the product's net ÷ total net over the latest weekly snapshots of the ISO weeks whose **Monday falls in the month and which are ≤ the current week** (`share_weeks`; the same convention as `GET /limits`), attributed by the engine; `share_pct` is that share. `shared` and `unattributed` get the same treatment, so Σ products + shared + unattributed = `account.net` (± 0.005). `covered_until` = Sunday of the last `share_weeks` entry (clamped to today); no share weeks → products `net: 0`, `status: "no_data"`, `share_weeks: []`.
- `target` applies from `month_from` until superseded (the newest `targets` row with `month_from` ≤ `month` per product; a row with `amount: null` clears it). The account (`*`) target is also written to / read from settings `target.monthly_total` (so the Month tab and budget suggestions agree); GET prefers the `targets` row, falls back to the setting (`month_from: null`).
- `pct` = 100 × net ÷ target. `status`: `on_track` (projected ≤ target), `at_risk` (projected ≤ 1.10 × target), `over` (greater), `no_target`, `no_data`. `remaining` = target − net (may be negative). Products listed = union of products with a target and products seen in the share weeks, sorted by net desc.
- PUT body `{"month_from": "2026-11", "targets": [{"product": "example-product", "amount": 6000}, {"product": "*", "amount": 25000}, {"product": "example-other", "amount": null}]}`; `month_from` optional (default current month), `targets` non-empty; `amount` finite ≥ 0 or `null` (clear); `product` is `*` or a valid name (`400 invalid_target` otherwise; nothing saved). Each entry inserts a `targets` row (history retained; same `(product, month_from)` replaces the row). 200: the `GET /targets` body for the current month (optional `month` query honoured).

### `GET /api/v1/rightsizing`
Query: `week` (default `default_week`; the window **ends** at this week), `scope`. 200:
```json
{
  "week": "2026-W43", "scope": "all", "currency": "INR", "generated_at": "2026-10-26T09:00:00Z", "monthly_factor": 4.348214,
  "thresholds": {"cpu_pct": 15, "weeks": 3, "stale_snapshot_days": 30, "idle_run_min_net": 1},
  "sources": [
    {"source": "monitoring_vm", "status": "ok", "error_code": null, "message": null, "fix_hint": null},
    {"source": "compute_inventory", "status": "failed", "error_code": "permission_denied", "message": "…", "fix_hint": "Grant roles/compute.viewer to the service account and enable compute.googleapis.com."}
  ],
  "candidates": [
    {"id": "low_cpu_vm:proj-a:example-vm", "kind": "low_cpu_vm", "project_id": "proj-a", "resource_name": "example-vm", "resource_type": "compute_instance",
     "location": "zone-a", "confidence": "medium",
     "evidence": {"window_start": "2026-10-05", "window_end": "2026-10-25",
                  "points": [{"week": "2026-W41", "value": 6.2, "unit": "%"}, {"week": "2026-W42", "value": 7.9, "unit": "%"}, {"week": "2026-W43", "value": 5.1, "unit": "%"}],
                  "notes": ["mean CPU < 15 % in each of the last 3 stored weeks"]},
     "net_last_week": 410.0, "estimated_monthly_saving": 891.5,
     "saving_basis": "50 % of last week's instance core+RAM net (205.0) × 4.348 weeks/month — assumes one machine size down",
     "action": "Review the workload; consider a smaller machine type or stopping the instance. cloud0 makes no changes."},
    {"id": "idle_disk:proj-a:example-disk", "kind": "idle_disk", "…": "…", "evidence": {"window_start": "2026-10-25", "window_end": "2026-10-25", "points": [], "notes": ["status READY, no attached users (Compute API at 2026-10-26T09:05:00Z)"]}, "net_last_week": 22.0, "estimated_monthly_saving": 95.7, "saving_basis": "last week's disk net × 4.348 weeks/month", "action": "…"}
  ],
  "totals": {"count": 2, "estimated_monthly_saving": 987.2}
}
```
- `kind` ∈ `low_cpu_vm | idle_disk | unused_ip | stale_snapshot | cloud_run_min_instances`. `confidence` ∈ `high | medium | low`. Candidates sorted by `estimated_monthly_saving` desc (null last), then `id`; `estimated_monthly_saving` and `net_last_week` may be `null` when no billing row matches (then `saving_basis` says so).
- `low_cpu_vm`: from the stored VM CPU metric rows (`metric_rows` group `vm`, cell `cpu`): the VM's weekly mean CPU is `< thresholds.cpu_pct` in **each of the `thresholds.weeks` (default 3) consecutive ISO weeks ending at `week`**, every one of which has a stored metric snapshot with a numeric value (a missing week or a `gap` cell breaks the streak and yields no candidate — never treated as 0). `points` has exactly one entry per window week. Savings: 50 % of that VM's last-week Compute Engine core+RAM SKU net (`confidence` is always `medium`) × `monthly_factor`.
- `idle_disk` (Compute API disks: status `READY`, no `users`), `unused_ip` (addresses: status `RESERVED`, no `users`), `stale_snapshot` (snapshots older than `stale_snapshot_days` whose source disk no longer exists or any snapshot older than 4× that): `net_last_week` = the resource's last-week billing net (by resource name), saving = that × `monthly_factor`; `confidence: "high"` for disk/IP, `medium` for snapshots. These need the `compute_inventory` source; when it fails or is unconfigured those kinds are absent and the failure is in `sources[]` with its hint (the response is still 200).
- `cloud_run_min_instances`: for each Cloud Run service whose last-week billing rows include **idle** (instance-based/min-instance idle) CPU or memory SKUs with net ≥ `idle_run_min_net` (SKU description contains `idle`, case-insensitive): `net_last_week` = that idle net; saving = idle net × `monthly_factor` ("what lowering `minScale` to 0 would remove if traffic tolerates cold starts"), `confidence: "medium"`. Evidence `points` = idle net per week for the window (stored weeks only).
- Window/threshold settings: `rightsizing.cpu_pct` (15), `rightsizing.weeks` (3), `rightsizing.stale_snapshot_days` (30), `rightsizing.idle_run_min_net` (1) in `settings` (no PUT endpoint in Phase 3; defaults apply). `monthly_factor` = 365.25 / 84 = 4.348214.
- Cloud0 never modifies resources; advisory only. Dismissal is out of scope in Phase 3.

### `POST /api/v1/attribution/propose`
Body `{"week": "2026-W43", "scope": "all", "max_rules": 20}` (all optional; `max_rules` 1–50, default 20). Optional and **off by default**: with `CLOUD0_OPENROUTER_API_KEY` unset → `503 proposer_not_configured` and zero outbound requests. Otherwise one batched OpenRouter chat completion over the week's unattributed items (resource names, labels, project ids, service names — **no costs**), schema-validated, returned as proposals; **never applied**:
```json
{
  "week": "2026-W43", "model": "anthropic/claude-sonnet-4-6", "latency_ms": 2400, "input_tokens": 1850, "output_tokens": 420,
  "proposals": [
    {"rule": {"id": null, "priority": 330, "match": {"field": "resource_name", "op": "contains", "value": "example-orphan"}, "action": {"product": "example-orphan"}, "enabled": true, "source": "proposed", "note": null},
     "confidence": "medium", "rationale": "Three resources share the token 'example-orphan'.", "covers": 3}
  ],
  "coverage_if_applied": {"total_net": 2012.5, "attributed_net": 1900.0, "shared_net": 90.0, "unattributed_net": 22.5, "attributed_pct": 94.4, "shared_pct": 4.5, "unattributed_pct": 1.1}
}
```
The user accepts a proposal by including its `rule` (source `proposed`) in a normal `PUT /attribution/rules`. Proposed rules that fail rule validation are dropped (count in `dropped`). `502 proposer_failed` on provider error/invalid JSON (hint: the provider message, trimmed). The call is logged (latency, tokens) without prompt contents. Model from `CLOUD0_LLM_MODEL`.

### Settings keys (Phase 3, table `settings`, no seed rows)
`attribution.allocate` (`none|even|proportional`, default `none`), `attribution.seed.aliases` (JSON object), `rightsizing.cpu_pct`, `rightsizing.weeks`, `rightsizing.stale_snapshot_days`, `rightsizing.idle_run_min_net`.
