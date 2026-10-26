# cloud0 — Architecture

## System overview

cloud0 is a single Go process that (1) serves a static, pre-built web dashboard and (2) exposes a JSON API on the same origin. On "Refresh" it runs one partition-filtered BigQuery query against the detailed billing export, stores the result as an immutable weekly snapshot in a local SQLite file, and the dashboard renders the Cost table from stored snapshots. Nothing is scheduled; nothing is computed by an LLM (`spec/agent.md`).

```
browser ──HTTP (one origin)──► cloud0 (Go, one binary)
                                 ├─ embedded static UI  (Next.js static export, go:embed)
                                 ├─ /api/v1/*           (JSON; spec/api.md)
                                 ├─ refresh orchestrator ──► BigQuery billing export (read-only, partition-filtered)
                                 └─ store ───────────────► SQLite file at CLOUD0_DB_PATH (snapshots, runs, settings, annotations)
```

Phase 2+ adds more read-only sources behind the same orchestrator (Cloud Monitoring, Error Reporting, Logging; later Billing Budgets, Monitoring alert policies, vendor usage APIs). Each source writes its own `run_sources` row so partial failure is always visible, never a blank page.

## Components

| Component | Package | Responsibility |
|---|---|---|
| Entry point | `cmd/cloud0` | Subcommands `serve`, `migrate up`, `migrate status`, `version`. Loads config, wires dependencies. |
| Config | `internal/config` | Typed settings from env (+ `.env` for local dev), validated at startup; secrets never printed. |
| Logging | `internal/logging` | `log/slog` JSON handler on stdout; HTTP request-logging middleware. |
| ISO week math | `internal/isoweek` | Pure functions: parse/format `YYYY-Www`, Monday–Sunday UTC bounds, current/last-complete week, week status (partial / settling / final), range enumeration. |
| Store | `internal/store` | SQLite open + pragmas, goose migrations (embedded SQL), repositories for snapshots, runs, settings, annotations. |
| Billing source | `internal/billing` | Builds the parameterised BigQuery SQL for a usage window, runs it, returns grouped rows + job statistics, classifies errors into code + fix hint. |
| Cost table | `internal/costtable` | Pure assembly: service → sheet-column mapping, totals, % change, anomaly flags, scope filtering, row statuses. |
| Refresh | `internal/refresh` | Orchestrates one run: calls the billing source, writes `runs` / `run_sources` / `snapshots` / `snapshot_rows` in a single transaction; one refresh at a time (mutex). |
| HTTP API | `internal/httpapi` | Router, response envelope, handlers per resource (`spec/api.md`). |
| Web UI host | `internal/webui` | Embeds the built frontend, serves it with SPA fallback, serves the committed placeholder when no frontend build is present. |
| Test env | `internal/testenv` | Test-only helpers: load repo-root `.env` when vars are absent, skip-with-message when BigQuery vars are missing, temp SQLite DB per test. |
| Frontend | `web/` | Next.js (TypeScript) app, `output: 'export'`, consumes `/api/v1` via a typed client; Playwright e2e. |

## Data flow — the Phase 1 journey

1. Browser loads `/` → embedded `index.html` + `/_next/static/*`.
2. UI calls `GET /api/v1/config` (projects, first/current/default week) and `GET /api/v1/cost/table?scope=all` (rows for every week first→current from the latest snapshot per week; weeks without a snapshot come back with `cells: null`).
3. User picks a week + scope and clicks **Refresh** → `POST /api/v1/refresh {mode:"week", week}` (or **Refresh all weeks** → `{mode:"all"}`).
4. `refresh.Run` creates a `runs` row (status `running`), asks `billing.Fetch` for the window. `billing` builds the SQL (`## BigQuery query` below), executes it with `cloud.google.com/go/bigquery`, returns rows grouped by (iso_year, iso_week, project, service, sku, resource) plus job id / bytes processed / bytes billed / cache hit.
5. In one transaction: `run_sources` row (SQL text, params JSON, stats, or error + hint), one `snapshots` row per ISO week in the window (status at fetch computed by `isoweek.Status(now)`), and its `snapshot_rows`. `runs.status` → `ok` / `failed`.
6. Response carries the run + new snapshots; the UI re-fetches the table. Scope is applied at read time (`costtable.Filter`), so one snapshot serves "all" and every single-project view.
7. Clicking a cell → `GET /api/v1/snapshots/{id}` (provenance: exact SQL + params + job stats + statuses) and `GET /api/v1/snapshots/{id}/breakdown?level=sku|resource&…` (drill-down), both read from SQLite only.
8. CSV export is client-side from the rendered table; no server call.

Every HTTP request emits one JSON log line (`## Conventions → Logging`).

## BigQuery query (the only query in Phase 1)

Exactly this text (table name substituted from `CLOUD0_BILLING_EXPORT_TABLE`, validated against `^[A-Za-z0-9-]+\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$` at startup) is executed and stored verbatim in `run_sources.sql_text`; the four parameters are stored in `run_sources.params_json`. No other formatting is applied, so provenance shows the executed SQL.

```sql
SELECT
  EXTRACT(ISOYEAR FROM usage_start_time) AS iso_year,
  EXTRACT(ISOWEEK FROM usage_start_time) AS iso_week,
  project.id            AS project_id,
  service.id            AS service_id,
  service.description   AS service,
  sku.id                AS sku_id,
  sku.description       AS sku,
  resource.name         AS resource_name,
  resource.global_name  AS resource_global_name,
  SUM(cost)             AS gross,
  SUM((SELECT IFNULL(SUM(c.amount), 0) FROM UNNEST(credits) AS c)) AS credits,
  COUNT(*)              AS line_items
FROM `<CLOUD0_BILLING_EXPORT_TABLE>`
WHERE DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end
  AND usage_start_time >= @usage_start
  AND usage_start_time <  @usage_end
GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9
```

- `net = gross + credits` is computed in Go per row (credits are negative numbers). Credits are summed per line item via the correlated subquery — never `LEFT JOIN UNNEST(credits)`, which would multiply `cost` by the number of credits on a line.
- Parameters: `@usage_start` = Monday 00:00:00 UTC of the first week in the window, `@usage_end` = Monday 00:00:00 UTC after the last week (exclusive); `@part_start` = `DATE(@usage_start) − CLOUD0_PARTITION_MARGIN_DAYS`, `@part_end` = `DATE(@usage_end) + CLOUD0_PARTITION_MARGIN_DAYS` (default margin 1 day each side).
- `EXTRACT(ISOWEEK …)` in BigQuery is UTC-based for TIMESTAMP, matching the Mon–Sun UTC week.
- No `project.id` filter: a snapshot covers every billed project in the export; scope is a read-time filter (`## Stack` → Assumed). `CLOUD0_GCP_PROJECTS` only populates the scope dropdown.
- Expected cost: one week ≈ tens of MB processed; the full first→current range ≈ the whole table (< 1 GB). Both are far inside the BigQuery free tier.
- At `serve` startup a dry-run of this query (one week) validates credentials, table name and permissions; failure is logged at `ERROR` with the fix hint and surfaced in `/api/v1/health`, but the server still starts so the UI can show the error inline.

## Stack

User preferences from intake are binding and marked **(binding)**. Everything else is derived from requirements and marked `Assumed`.

| Concern | Choice | Rationale |
|---|---|---|
| Language / runtime | **Go 1.23 (binding)** — `/opt/homebrew/bin/go`, `go.mod` says `go 1.23`, no `toolchain` directive | One static binary, trivial `embed`, first-class GCP client libraries. |
| GCP clients | `cloud.google.com/go/bigquery` now; `cloud.google.com/go/monitoring/apiv3/v2`, `cloud.google.com/go/logging`, `cloud.google.com/go/errorreporting`, `cloud.google.com/go/billing/budgets` in later phases **(binding)** | Official libraries; Application Default Credentials via `GOOGLE_APPLICATION_CREDENTIALS` locally and the attached service account on Cloud Run. |
| HTTP routing | Go standard library `net/http` with Go 1.22+ method patterns (`mux.HandleFunc("GET /api/v1/cost/table", …)`) | > **Assumed:** ~12 routes, no middleware zoo needed; zero dependencies to pin. |
| Frontend | **Next.js (React, TypeScript), `output: 'export'`, embedded via `embed`, served by Go on one origin (binding)**; Node 22 LTS at `/Users/sai/.local/bin/node`, pnpm (asdf shim); lockfile committed | One process, one artefact. The production build is part of every gate. |
| Frontend version pins | Latest stable Next.js ≥ 15 at scaffold time, React 19, TypeScript 5, pinned exact in `web/package.json`; `engines.node` `>=22 <23` | > **Assumed:** pin what `pnpm create next-app` yields on the scaffold day; the gate proves `output: 'export'` works with it. |
| Styling | CSS Modules + one `globals.css`; no Tailwind, no UI kit | > **Assumed:** fewest build moving parts; the styled-render gate only needs real selectors in the emitted CSS. |
| Client state / data | React state + `fetch` wrapper in `web/src/lib/api.ts` typed from `spec/api.md`; tabs via `location.hash` (`#cost`, `#metrics`, …) | > **Assumed:** single route keeps the static export to one `index.html`; `useSearchParams` Suspense pitfalls avoided. |
| Frontend tests | Vitest for pure modules (`csv.ts`, `week.ts`); Playwright (`@playwright/test`, Chromium) for the live-server e2e | > **Assumed:** Playwright is required by the brief; Vitest keeps CSV/week logic deterministic and fast. |
| Store | SQLite via `modernc.org/sqlite` (cgo-free) at `CLOUD0_DB_PATH`; WAL, `busy_timeout=5000`, `foreign_keys=ON` | > **Assumed:** single-process, tiny data (thousands of rows/week), no new paid GCP service, cgo-free keeps `CGO_ENABLED=0` static builds. Postgres swap later: SQL kept to the portable subset (`spec/data.md`). |
| Migrations | `github.com/pressly/goose/v3` as a library, SQL files embedded from `internal/store/migrations`, exposed as `cloud0 migrate up` and `cloud0 migrate status` | > **Assumed:** goose supports embedded FS + per-dialect directories, so a Postgres directory can be added without changing commands. |
| `.env` loading | `github.com/joho/godotenv` — `serve`/`migrate` load `./.env` from the working directory if present, never overriding already-set env; tests load the repo-root `.env` via `internal/testenv` | > **Assumed:** local dev convenience; containers get real env vars and no `.env`. |
| Logging | `log/slog` with `slog.NewJSONHandler(os.Stdout)` **(binding)** | Structured from Phase 1; Cloud Run ingests JSON stdout natively. |
| Container | Multi-stage `Dockerfile` (node → go → `gcr.io/distroless/static`), `CLOUD0_BIND=0.0.0.0` | > **Assumed:** present from the scaffold for the "same container locally and on Cloud Run" requirement, but **not** part of any Phase 1 gate (Docker may be absent locally). |
| AI / LLM | None in the product (`spec/agent.md`) | Deterministic by requirement. |

### Embedded frontend design (go build never needs Node)

- `internal/webui/embed.go` declares `//go:embed all:dist` and separately `//go:embed placeholder.html`.
- `internal/webui/dist/.gitkeep` is committed; everything else under `dist/` is gitignored. Because `.gitkeep` always matches, a plain `go build` compiles with or without a frontend build.
- `make web` runs `pnpm build` in `web/` and copies `web/out/.` into `internal/webui/dist/` (after clearing it, keeping `.gitkeep`).
- At runtime `webui.Handler()` serves `dist/index.html` when present; otherwise it serves `placeholder.html` (a styled page saying "cloud0 UI not built — run `make web`", body attribute `data-cloud0-ui="placeholder"`). The real UI sets `data-cloud0-ui="real"` on `<body>`. `/api/v1/health` reports `ui: "real" | "placeholder"`. The gates assert `real`.
- Unknown non-`/api` paths fall back to `index.html` (200). `/_next/static/*` is served with `Cache-Control: public, max-age=31536000, immutable`; `index.html` with `no-cache`.

### Serve startup sequence (fail loudly)

1. Load `.env` (if present) → build `Config` → `Validate()` (required vars present, table name matches the regex, `CLOUD0_FIRST_WEEK` parses, port numeric). Missing/invalid → exit 2 with a message naming the variable and pointing at `.env.example`.
2. Open SQLite, apply pragmas, check goose status. Pending migrations → exit 3 with "pending migrations — run `./bin/cloud0 migrate up`".
3. Dry-run the billing query (one week) — **from Phase 1 slice S3 onward** (the scaffold has no billing package yet). Failure → `ERROR` log `msg:"bigquery.dryrun"` with code + fix hint, health reports it, serve continues.
4. Listen on `CLOUD0_BIND:CLOUD0_PORT`; log `{"msg":"listening","addr":…,"ui":"real|placeholder"}`.

## Layout

```
cloud0/
├── README.md                     exact commands (working dir stated first), URL http://127.0.0.1:8087/
├── NOTES.md                      harness-improvement journal (kept)
├── .env                          real values — gitignored, never staged
├── .env.example                  every variable with a placeholder (## Environment variables)
├── .gitignore                    + bin/, data/, web/node_modules, web/out, web/.next, internal/webui/dist/* (!.gitkeep), web/test-results, web/playwright-report
├── Makefile                      web-install web-build web build build-go migrate run test web-test e2e gate scaffold-gate
├── Dockerfile                    multi-stage; not gated in Phase 1
├── go.mod, go.sum                module github.com/smallTechOrg/cloud0 ; go 1.23
├── cmd/cloud0/main.go            subcommands: serve | migrate up | migrate status | version
├── internal/
│   ├── config/config.go          Config struct, Load(), Validate(), Redacted(); config_test.go
│   ├── logging/logger.go         New(level) *slog.Logger (JSON)
│   ├── logging/middleware.go     request logging + X-Request-Id
│   ├── isoweek/isoweek.go        Parse, Format, Bounds, Current, LastComplete, Range, Status; isoweek_test.go
│   ├── store/store.go            Open, Migrate, MigrationStatus, Close, tx helper
│   ├── store/migrations/0001_init.sql
│   ├── store/snapshots.go        InsertSnapshot(+rows), LatestPerWeek, Get, Breakdown
│   ├── store/runs.go             CreateRun, FinishRun, AddSource, ListRuns, GetRun
│   ├── store/settings.go         GetThresholds, PutThresholds
│   ├── store/annotations.go      Get, Upsert, Delete
│   ├── store/*_test.go           each test opens its own temp SQLite file (testenv.TempDB)
│   ├── billing/query.go          BuildQuery(window, table) (sql, params) — pure
│   ├── billing/source.go         Fetch(ctx, window) → rows + JobStats ; DryRun(ctx)
│   ├── billing/hints.go          Classify(err) → (code, message, hint)
│   ├── billing/query_test.go     pure; source_test.go real BigQuery (skips with message if vars absent)
│   ├── costtable/columns.go      fixed columns + mapping + extra/stub columns
│   ├── costtable/table.go        Assemble(weeks, snapshots, rows, thresholds, scope, now) — pure
│   ├── costtable/*_test.go
│   ├── refresh/refresh.go        Runner{Run(ctx, mode, week)} ; mutex ; tx write
│   ├── httpapi/server.go         New(deps) *http.Server, middleware chain, static mount
│   ├── httpapi/routes.go         registerRoutes(mux, deps)
│   ├── httpapi/envelope.go       writeJSON, writeError, APIError
│   ├── httpapi/health.go  config.go  table.go  refresh.go  snapshots.go  runs.go  settings.go  annotations.go
│   ├── httpapi/*_test.go         httptest + temp SQLite; refresh_test hits real BigQuery
│   ├── webui/embed.go            //go:embed all:dist ; //go:embed placeholder.html ; Handler(), IsReal()
│   ├── webui/placeholder.html
│   ├── webui/dist/.gitkeep
│   ├── testenv/testenv.go        LoadDotEnv(t), RequireBigQuery(t), TempDB(t), FreePort(t)
│   └── testenv/credentials_test.go  TestCredentialsAlive: one minimal real BigQuery call (SELECT 1) proving the .env key is alive
├── tests/
│   ├── scaffold/boot_test.go     builds binary → migrate up/status → serve → GET /api/v1/health
│   └── smoke/live_test.go        Phase 1 live smoke (make build → serve → refresh → table values → assets)
└── web/
    ├── package.json  pnpm-lock.yaml  next.config.ts  tsconfig.json  playwright.config.ts  vitest.config.ts  .gitignore
    ├── src/app/layout.tsx        <body data-cloud0-ui="real">
    ├── src/app/page.tsx          shell: TopBar + NavTabs + active panel + RunLogDrawer
    ├── src/app/globals.css
    ├── src/components/           TopBar, WeekPicker, ScopeSelect, RefreshButtons, NavTabs, CostTable, CellInspector,
    │                             DrilldownTable, ProvenanceView, RunLogDrawer, AnnotationField, ThresholdsEditor,
    │                             StatusBadge, StubPanel, EmptyState, ErrorBanner
    ├── src/lib/api.ts            typed client (mirrors spec/api.md)
    ├── src/lib/types.ts          request/response types (mirrors spec/api.md)
    ├── src/lib/csv.ts            buildCsv(columns, rows) ; csv.test.ts
    ├── src/lib/week.ts           labels, status text ; week.test.ts
    └── e2e/cost-journey.spec.ts  Playwright: picker → Refresh → table → drill-down → provenance → CSV → annotation
```

Rules: all Go application code under `internal/` (one module root); all frontend under `web/`; cross-process tests under `tests/`; Go unit/integration tests stay package-local (`_test.go`) per Go convention. Nothing else at the repo root.

## Conventions

### Go
- Formatting `gofmt`; `go vet ./...` clean is part of every gate. No third-party linters required.
- Package names short, lowercase, no underscores. Exported identifiers documented with a sentence only when the name is not self-explanatory.
- Errors: wrap with `fmt.Errorf("billing: run query: %w", err)`; sentinel errors as package-level `var ErrX = errors.New(...)`. Never `panic` outside `main` initialisation.
- Context first argument everywhere that does I/O; timeouts: BigQuery query 120 s, HTTP server read/write 130 s, shutdown 10 s.
- Time: always UTC; `time.Time` in memory, RFC3339 `TEXT` in SQLite (`2026-10-26T12:00:00Z`), ISO dates as `YYYY-MM-DD`. Functions that need "now" take a `now time.Time` parameter or a `Clock` so tests inject it.
- Money: `float64` USD end to end (BigQuery `FLOAT64` → Go → SQLite `REAL` → JSON number). Round only for display (UI, 2 dp). Tests compare with tolerance `0.005`.
- Types at boundaries: request/response structs in `httpapi`, row structs in `store`, no `map[string]any` crossing packages.
- JSON field names `snake_case`, matching `spec/api.md` exactly.
- Response envelope: success = resource object at top level (never contains a key named `error`); failure = `{"error":{"code","message","hint"}}` with the HTTP status from `spec/api.md`. Handlers return `*APIError`; `envelope.go` is the only place that writes error bodies. No stack traces in responses.
- Config: `Config` fields typed; `Redacted()` prints every field except credentials (`GOOGLE_APPLICATION_CREDENTIALS` shown as `set`/`unset (ADC)`). Startup logs `Redacted()` once.
- SQL: `?` placeholders, lowercase keywords allowed, one statement per `Exec`/`Query`; multi-row inserts batched (≤ 500 rows per statement). Portable subset only (`spec/data.md → Portability`).
- Tests: `TestXxx_DoesY` names read as sentences; table-driven where natural; each DB test gets a fresh temp file from `testenv.TempDB(t)`; tests needing BigQuery call `testenv.RequireBigQuery(t)` first. `go test ./... -count=1` must be green with `.env` present; with the BigQuery vars absent those tests `t.Skip` with the message `CLOUD0_BILLING_EXPORT_TABLE not set — fill .env (see .env.example) to run real BigQuery tests`.

### Logging (slog JSON, stdout)
- Every request: `{"time","level":"INFO","msg":"request","request_id","method","path","status","duration_ms","bytes_out","remote_addr"}`; `level` `WARN` for 4xx, `ERROR` for 5xx with `"error"`.
- Every refresh: `msg:"refresh.start"` `{run_id, mode, from, to}`; per source `msg:"source.done"` `{run_id, source, status, job_id, bytes_processed, bytes_billed, cache_hit, rows, duration_ms, error_code, fix_hint}`; `msg:"refresh.done"` `{run_id, status, snapshots, duration_ms}`.
- Never log secret values, the credentials path, or full SQL (SQL lives in the DB; log `job_id`).
- Level from `CLOUD0_LOG_LEVEL` (`debug|info|warn|error`).

### TypeScript / React
- Strict TypeScript; no `any`. Types in `src/lib/types.ts` mirror `spec/api.md` field-for-field (snake_case preserved, no renaming layer).
- Components are function components; one component per file, `PascalCase.tsx` + `PascalCase.module.css`.
- All API calls go through `src/lib/api.ts`; it throws `ApiError{code,message,hint,status}` built from the error envelope; components render `hint` when present.
- Static export constraints: no `next/image`, no server components with data fetching, no dynamic routes; `next.config.ts` = `{ output: 'export', trailingSlash: false, reactStrictMode: true }` (plus a dev-only `rewrites` of `/api/*` to `http://127.0.0.1:${CLOUD0_PORT}` for `pnpm dev`; the two-server dev flow is a convenience, never the tested path).
- Stub surfaces render `StubPanel` or a `(stub)` column header — never an empty table or blank panel.
- Tests: Vitest for `src/lib/*.test.ts`; Playwright specs under `web/e2e/`, `baseURL` `http://127.0.0.1:${CLOUD0_E2E_PORT}` (read via `dotenv` from `../.env`, default 8097), `webServer` runs `sh -c './bin/cloud0 migrate up && ./bin/cloud0 serve'` with `cwd: '..'`, env `CLOUD0_PORT=${CLOUD0_E2E_PORT}` and `CLOUD0_DB_PATH` pointed at a temp file, `reuseExistingServer: false`.

### Public-repo hygiene
- No project IDs, billing account IDs, dataset/table names, hostnames, resource names or keys in any committed file (code, spec, tests, fixtures, README). Refer to them only by env var name; tests read them from `.env`. Pre-commit review of diffs for these patterns is part of every slice's self-check.

## Environment variables

All variables are read by `internal/config`. `.env.example` lists exactly this table with the placeholder values shown. Changes versus the current `.env`: **`CLOUD0_PORT` changes from 8080 to 8087** (8080 is a heavily squatted default); the optional variables below are **new** (all have defaults, so `.env` works without them).

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `GOOGLE_APPLICATION_CREDENTIALS` | no | unset → Application Default Credentials | Absolute path to the read-only service-account key, outside the repo. Unset on Cloud Run (attached SA). |
| `CLOUD0_GCP_PROJECTS` | yes | — | Comma-separated billed project IDs; populates the scope dropdown. |
| `CLOUD0_BILLING_ACCOUNT_ID` | no (Phase 4) | — | Billing account for the Budgets API. Unused before Phase 4. |
| `CLOUD0_BILLING_EXPORT_TABLE` | yes | — | Fully-qualified `project.dataset.table` of the detailed (resource-level) billing export. |
| `CLOUD0_BIGQUERY_PROJECT` | yes | — | Project that runs BigQuery jobs (needs `roles/bigquery.jobUser`). |
| `CLOUD0_PORT` | no | `8087` | Listen port. If unset and `PORT` is set (Cloud Run), `PORT` is used. |
| `CLOUD0_BIND` | no | `127.0.0.1` | Bind address. The Dockerfile sets `0.0.0.0`. |
| `CLOUD0_DB_PATH` | no | `./data/cloud0.db` | SQLite file; parent directory is created if missing. |
| `CLOUD0_FIRST_WEEK` | no | `2026-W18` | First ISO week shown in the Cost table / covered by Refresh all. |
| `CLOUD0_SETTLE_HOURS` | no | `48` | Hours after a week ends (Mon 00:00 UTC) before it is labelled `final`. |
| `CLOUD0_PARTITION_MARGIN_DAYS` | no | `1` | Days added on each side of the usage window for the `_PARTITIONTIME` filter. |
| `CLOUD0_LOG_LEVEL` | no | `info` | `debug` / `info` / `warn` / `error`. |
| `CLOUD0_SMOKE_WEEK` | no (tests only) | `2026-W42` | A known complete, non-empty week used by the smoke and e2e tests. |
| `CLOUD0_E2E_PORT` | no (tests only) | `8097` | Port Playwright starts the binary on (so e2e never collides with a running dev server on `CLOUD0_PORT`). The Go live tests use an OS-assigned free port instead. |

Placeholder values for `.env.example`:

```
GOOGLE_APPLICATION_CREDENTIALS=/absolute/path/outside/the/repo/sa-key.json
CLOUD0_GCP_PROJECTS=my-project-a,my-project-b
CLOUD0_BILLING_ACCOUNT_ID=XXXXXX-XXXXXX-XXXXXX
CLOUD0_BILLING_EXPORT_TABLE=my-project.my_dataset.gcp_billing_export_resource_v1_XXXXXX_XXXXXX_XXXXXX
CLOUD0_BIGQUERY_PROJECT=my-project
CLOUD0_PORT=8087
CLOUD0_BIND=127.0.0.1
CLOUD0_DB_PATH=./data/cloud0.db
CLOUD0_FIRST_WEEK=2026-W18
CLOUD0_SETTLE_HOURS=48
CLOUD0_PARTITION_MARGIN_DAYS=1
CLOUD0_LOG_LEVEL=info
CLOUD0_SMOKE_WEEK=2026-W42
CLOUD0_E2E_PORT=8097
```

Later phases add (documented when they land, all optional): `CLOUD0_OPENROUTER_API_KEY`, `CLOUD0_LLM_MODEL` (Phase 3, off by default), `CLOUD0_GROQ_API_KEY`, `CLOUD0_CLAUDE_USAGE_CMD` (Phase 4).

## Build and run commands

All commands run from the repo root `/Users/sai/Workspace/Code/cloud0` unless stated. The Makefile pins `GO ?= /opt/homebrew/bin/go` and `PNPM ?= pnpm` (both overridable: `make gate GO=/usr/local/go/bin/go`). Node must be the 22.x LTS (`/Users/sai/.local/bin/node`).

| Step | Command | Verify |
|---|---|---|
| Install frontend deps | `make web-install` → `cd web && pnpm install --frozen-lockfile` | exits 0 |
| Install Playwright browser (once) | `cd web && pnpm exec playwright install chromium` | exits 0 |
| Build frontend + sync into embed dir | `make web-build` → `cd web && pnpm build` then copy `web/out/.` → `internal/webui/dist/` | `ls internal/webui/dist/index.html` |
| Build binary (with UI) | `make build` (= `make web` + `/opt/homebrew/bin/go build -o bin/cloud0 ./cmd/cloud0`) | `./bin/cloud0 version` prints a version |
| Build binary (no Node, placeholder UI) | `make build-go` → `/opt/homebrew/bin/go build -o bin/cloud0 ./cmd/cloud0` | `./bin/cloud0 version` |
| Migrate | `./bin/cloud0 migrate up` | `./bin/cloud0 migrate status` prints every migration with `Applied At` filled, exit 0 |
| Run | `./bin/cloud0 serve` → `http://127.0.0.1:8087/` | `curl -s http://127.0.0.1:8087/api/v1/health` shows `"ok":true,"ui":"real"` |
| Go vet + tests (real BigQuery via `.env`, temp SQLite, live smoke) | `make test` → `/opt/homebrew/bin/go vet ./... && /opt/homebrew/bin/go test ./internal/... -count=1 -timeout 15m && /opt/homebrew/bin/go test ./tests/... -count=1 -p 1 -timeout 15m` (`-p 1` serialises the two live-server packages; each starts the binary on an OS-assigned free port passed as `CLOUD0_PORT` in the child environment) | `ok` for every package, zero `SKIP` lines when `.env` is filled |
| Frontend unit tests | `make web-test` → `cd web && pnpm test` (`vitest run`) | all green |
| Headless e2e | `make e2e` → `cd web && pnpm exec playwright test` | all green |
| Full Phase 1 gate | `make gate` = `build` → `migrate` → `test` → `web-test` → `e2e` | exits 0 |
| Scaffold gate | `make scaffold-gate` = `build` → `migrate` → `test` | exits 0 |

`make migrate` and the tests use `CLOUD0_DB_PATH` from `.env` for the dev DB only in `make migrate`/`make run`; every test (Go and Playwright) overrides `CLOUD0_DB_PATH` to a temp file it creates and deletes, so tests never touch the dev DB.
