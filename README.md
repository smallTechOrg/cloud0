# cloud0

Internal dashboard for a small GCP shop that replaces the hand-maintained "Tech Weekly Review" spreadsheet: it pulls the detailed billing export from BigQuery, stores an immutable snapshot per ISO week (Mon–Sun, UTC) and renders the weekly Cost table — net of credits — with drill-downs, provenance, anomaly flags, CSV export and notes. One Go binary serves the JSON API and the embedded Next.js UI on one origin. No AI in the product; nothing is scheduled.

Design and contract: [`spec/`](spec/) (`architecture.md`, `api.md`, `data.md`, `ui.md`, `roadmap.md`). Build journal: [`NOTES.md`](NOTES.md).

**All commands below run from the repository root** (`/Users/sai/Workspace/Code/cloud0`) unless the code block says otherwise. The dashboard URL is **http://127.0.0.1:8087/**.

## Scaffold vs. Phase 1 (this table describes the delta; Phase 1 is the target state of this repo)

| Area | Today (scaffold) | Phase 1 |
|---|---|---|
| Binary | `cloud0 serve \| migrate up \| migrate status \| version` | same |
| API | `GET /api/v1/health`, `GET /api/v1/config`; JSON error envelope; 404/405 envelopes | `cost/table`, `refresh`, `snapshots/{id}` (+ `breakdown`), `runs`, `settings`, `annotations/{week}` |
| Data | SQLite at `CLOUD0_DB_PATH`, migration `0001_init.sql` with the full Phase 1 schema (runs, run_sources, snapshots, snapshot_rows, annotations, settings) | repositories writing/reading those tables |
| BigQuery | credentials validated by a test (`SELECT 1`) | the partition-filtered billing query, startup dry-run reported in `/health` |
| UI | real Next.js shell embedded in the binary: title, API connection status, configuration summary, Cost tab with a labelled "Cost table arrives in Phase 1" placeholder, other tabs badged P2/P3/P4 | week picker, scope, Refresh, Cost table, drill-down, provenance, CSV, anomaly thresholds, run log, annotations; other tabs remain labelled stubs |
| Logging | one JSON line per HTTP request (`"msg":"request"`) | plus `refresh.start` / `source.done` / `refresh.done` |
| Tests | Go unit tests, real-BigQuery credentials test, cross-process boot test, Vitest, one Playwright health/shell spec | real-BigQuery integration tests, live smoke, the Playwright cost journey |

## Prerequisites (pinned)

| Tool | Version | Path |
|---|---|---|
| Go | 1.23.3 | `/opt/homebrew/bin/go` |
| Node.js | 22.23.1 (LTS) | `/Users/sai/.local/bin/node` |
| pnpm | 11.7.0 | asdf shim `pnpm`; the `make` targets run it on Node 22 as `/Users/sai/.local/bin/node $(asdf which pnpm)` because the shim itself is bound to a newer Node and pnpm 11 ignores `use-node-version` |
| Playwright Chromium | installed once by `pnpm exec playwright install chromium` | `~/Library/Caches/ms-playwright` |

If you call `pnpm` directly (outside `make`), make sure it runs on Node 22: `/Users/sai/.local/bin/node "$(asdf which pnpm)" <args>` — or `make <target> PNPM=pnpm` when your own `pnpm` already uses Node 22.

Direct `go` commands need `GO111MODULE=on GOTOOLCHAIN=local` on this machine (`GO111MODULE=off` is persisted in the user's global Go env and Go 1.23.3 must not auto-download a newer toolchain). The `make` targets export both; prefix them yourself when calling `go` directly, e.g. `GO111MODULE=on GOTOOLCHAIN=local /opt/homebrew/bin/go test ./internal/... -count=1`.

Go dependencies are pinned to the newest releases whose `go.mod` accepts Go 1.23: `modernc.org/sqlite v1.39.0`, `github.com/pressly/goose/v3 v3.26.0`, `cloud.google.com/go/bigquery v1.70.0`, `github.com/joho/godotenv v1.5.1`. Frontend: `next 16.4.3`, `react 19.3.1`, `typescript 5.9.3`, `vitest 5.0.5`, `@playwright/test 1.64.1` (exact pins in `web/package.json`, lockfile committed).

## 1. Configure

```sh
# working directory: repo root
cp .env.example .env
# then edit .env — see the table below
```

`.env` is gitignored and never committed; the repo is public, so project IDs, billing account IDs, table names and key paths live only there. `serve` and `migrate` load `./.env` when present without overriding variables already set in the environment.

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `GOOGLE_APPLICATION_CREDENTIALS` | no | unset → Application Default Credentials | Absolute path to the read-only service-account key, outside the repo. Unset on Cloud Run. |
| `CLOUD0_GCP_PROJECTS` | yes | — | Comma-separated billed project IDs; populates the scope dropdown. |
| `CLOUD0_BILLING_ACCOUNT_ID` | no (Phase 4) | — | Billing account for the Budgets API. |
| `CLOUD0_BILLING_EXPORT_TABLE` | yes | — | Fully-qualified `project.dataset.table` of the detailed billing export. |
| `CLOUD0_BIGQUERY_PROJECT` | yes | — | Project that runs BigQuery jobs (`roles/bigquery.jobUser`). |
| `CLOUD0_PORT` | no | `8087` | Listen port (falls back to `PORT` when unset). |
| `CLOUD0_BIND` | no | `127.0.0.1` | Bind address; the Dockerfile sets `0.0.0.0`. |
| `CLOUD0_DB_PATH` | no | `./data/cloud0.db` | SQLite file; parent directory created if missing. |
| `CLOUD0_FIRST_WEEK` | no | `2026-W18` | First ISO week shown / covered by Refresh all. |
| `CLOUD0_SETTLE_HOURS` | no | `48` | Hours after a week ends before it is labelled `final`. |
| `CLOUD0_PARTITION_MARGIN_DAYS` | no | `1` | Days added on each side of the `_PARTITIONTIME` filter. |
| `CLOUD0_LOG_LEVEL` | no | `info` | `debug` / `info` / `warn` / `error`. |
| `CLOUD0_CURRENCY` | no | `INR` | 3-letter ISO 4217 code of the billing export's `currency` column. Display only: used to format every money value; no conversion is applied. |
| `CLOUD0_SMOKE_WEEK` | tests only | `2026-W42` | A known complete, non-empty week for smoke/e2e tests. |
| `CLOUD0_E2E_PORT` | tests only | `8097` | Port Playwright starts the binary on. |

Missing or invalid required variables make `serve`/`migrate` exit with status 2 and a message naming the variable.

## 2. Build

```sh
# working directory: repo root
make web-install          # cd web && pnpm install --frozen-lockfile
make web-build            # cd web && pnpm build, then sync web/out → internal/webui/dist
make build-go             # /opt/homebrew/bin/go build -o bin/cloud0 ./cmd/cloud0
./bin/cloud0 version      # verify: prints "cloud0 0.1.0"
```

`make build` runs all three. `make build-go` alone also works without Node: the binary then serves a labelled placeholder page (`/api/v1/health` reports `"ui":"placeholder"`) until `make web-build && make build-go` is run again.

Playwright's browser is installed once:

```sh
# working directory: web
/Users/sai/.local/bin/node "$(asdf which pnpm)" exec playwright install chromium
```

## 3. Migrate

```sh
# working directory: repo root
./bin/cloud0 migrate up        # applies pending migrations to CLOUD0_DB_PATH
./bin/cloud0 migrate status    # verify: lists 0001_init.sql with an "Applied At" timestamp
```

`make migrate` runs both. `serve` refuses to start (exit 3) while migrations are pending.

## 4. Run

```sh
# working directory: repo root
./bin/cloud0 serve
```

Open **http://127.0.0.1:8087/** (set `CLOUD0_PORT=8087` in `.env`; 8087 is also the default). Expected (Phase 1): the cloud0 header with a week picker defaulting to the last complete week, scope "All billed projects", the Cost table with every row reading "not fetched — Refresh", and the status line `connected · db ok · migrations current · bigquery ok`. The terminal prints one JSON line per request. Stop with Ctrl-C (graceful shutdown).

Verify from another terminal:

```sh
# working directory: repo root
curl -s http://127.0.0.1:8087/api/v1/health   # {"ok":true,"version":"0.1.0","db":"ok","migrations":"current","ui":"real","bigquery":{"status":"ok",...}}
curl -s http://127.0.0.1:8087/api/v1/config   # {"projects":[...],"first_week":"2026-W18","current_week":"...","default_week":"...",...}
curl -s -X POST http://127.0.0.1:8087/api/v1/refresh -d '{"mode":"week","week":"2026-W42"}'
curl -s http://127.0.0.1:8087/api/v1/cost/table | jq '.rows[] | select(.week=="2026-W42")'    # jq is optional
```

`make run` is the same as `./bin/cloud0 serve`.

## 5. Test

```sh
# working directory: repo root
make test        # go vet ./... ; go test ./internal/... ; go test ./tests/... -p 1
make web-test    # cd web && pnpm test        (Vitest)
make e2e         # cd web && pnpm exec playwright test   (starts ./bin/cloud0 on CLOUD0_E2E_PORT with a temp DB)
```

- `make test` includes `internal/testenv`'s `TestCredentialsAlive`, which runs a real `SELECT 1` on BigQuery with the key from `.env`; it skips (and the gate is **not** green) only when the BigQuery variables are absent.
- `tests/scaffold` builds the binary, migrates a temp DB, starts `serve` on an OS-assigned port and checks health, the served UI, its CSS/JS and the request log. It needs `make web-build` to have run first.
- `tests/smoke` (Phase 1 live smoke) runs `make build`, migrates a temp DB, starts `serve` on an OS-assigned port with the real `.env` BigQuery settings, waits for `health.bigquery.status == "ok"`, refreshes `CLOUD0_SMOKE_WEEK` (default `2026-W42`), asserts Compute Engine net > 0 and Total GCP = Σ services ± 0.005, fetches `/` and every linked CSS/JS, checks the `request` / `refresh.done` log lines, then stops the server and deletes the temp DB. It runs for real against BigQuery (one query, well under 200 MB) and takes a few minutes because it rebuilds the UI. Run it alone with `GO111MODULE=on GOTOOLCHAIN=local /opt/homebrew/bin/go test ./tests/smoke/ -count=1 -v -timeout 15m`.
- `tests/smoke/live_phase2_test.go` starts its own server, runs `refresh` mode `all` and then the smoke week against real GCP, and asserts the Phase 2 gate checks (section 8). Run it alone with `... go test ./tests/smoke/ -run Phase2 -count=1 -v`.
- Every test uses its own temp SQLite file; nothing touches `./data/cloud0.db`.

Gates (`spec/roadmap.md`):

```sh
# working directory: repo root
make scaffold-gate   # build → migrate → test
make gate            # Phase 1 gate: build → migrate → test (vet, internal, tests/scaffold + tests/smoke) → web-test → e2e
```

## 6. Phase 1 walkthrough (how to test it by hand)

```sh
# working directory: repo root
make build && ./bin/cloud0 migrate up && ./bin/cloud0 serve
```

Open **http://127.0.0.1:8087/** (needs the BigQuery variables from section 1 in `.env`). Then:

1. **First load.** The week picker defaults to the last complete week, scope is "All billed projects", and every row reads "not fetched — Refresh" (never zeros).
2. **Refresh all weeks.** Click it (about 10–30 s, progress text visible): every week fills from the first week to the current one with one BigQuery job. The current week is badged *partial*, last week *settling* or *final*.
3. **Reconcile.** Compare Total GCP of a known week with the spreadsheet; expect roughly equal (small differences come from the sheet's manual day boundaries).
4. **Drill down.** Click a Compute Engine cell: the inspector shows the exact SQL, parameters, BigQuery job id, bytes, fetch time and status. The SKU list appears; click a SKU for resource rows with gross / credits / net. Networking shows gross > net (discount credits); VM manager shows 0 net with gross offset by credits.
5. **Export CSV.** Click **Export CSV** and open the file in a spreadsheet: same header order, 2-dp values.
6. **Anomaly thresholds.** Change them to 5% / 5: more rows are flagged. Set them back. They persist across reload.
7. **Notes.** Type a note on the selected week, Save, reload: it persists (also across a server restart).
8. **Run log.** Open the run log drawer: runs with duration, rows and bytes billed.
9. **Scope.** Switch to one project: numbers change and provenance shows the scope-filter line.

Single-week refresh from the shell:

```sh
# working directory: repo root
curl -s -X POST http://127.0.0.1:8087/api/v1/refresh -d '{"mode":"week","week":"2026-W42"}'
curl -s http://127.0.0.1:8087/api/v1/runs
```

Server log lines to expect in the terminal: `request` (one per HTTP request), and for each refresh `refresh.start`, `source.done`, `refresh.done`.

### Labelled stubs (not bugs)

- Tabs **Attribution, Budgets & Alerts, Vendors, Rightsizing, Targets** each show a "Coming in Phase N" panel (Attribution/Rightsizing/Targets: Phase 3; Budgets & Alerts/Vendors: Phase 4). Metrics, Errors, Month and Limits are real since Phase 2 (section 8).
- The Cost table columns **AI Cost (Groq) (stub)** and **claude (usage) (stub)** show "—" in every cell (Phase 4).
- The Groq limits in the Limits tab are labelled stubs (Phase 4). There is no login, no scheduler and no AI in the product.

## 7. Phase 1 gate

```sh
# working directory: repo root
make gate
```

Runs, in order: `make build` → `./bin/cloud0 migrate up && ./bin/cloud0 migrate status` → `go vet ./...` → `go test ./internal/...` → `go test ./tests/... -p 1` (includes `tests/scaffold` and `tests/smoke`, real BigQuery) → `cd web && pnpm test` → `cd web && pnpm exec playwright test` (starts its own server on `CLOUD0_E2E_PORT` with a temp DB and walks the cost journey). Prerequisites: a filled `.env`, and Playwright's Chromium installed (section 2).

## 8. Phase 2: Metrics, Errors, Month, Limits

Four more tabs, all read-only views over data the **Refresh** buttons store (nothing is scheduled):

| Tab | Shows |
|---|---|
| **Metrics** | The sheet's block for the selected week, in order: VMs (CPU / Memory / Disk %), Cloud SQL (CPU / Memory / Disk %), Networking, Cloud Run, Cloud Storage, Cloud Logging, Errors. A missing value is an explicit gap (`no agent`, `no data`, `source failed`, `not collected`) with a fix hint, never a `0`. Click a cell for its provenance: the exact Monitoring filter, aggregation, window and fetch time. Export CSV. |
| **Errors** | Cloud Run 2xx/3xx/4xx/5xx per service, Error Reporting groups and occurrences per service, and cards for the load balancer (counts when its metric has data, otherwise a gap with the fix hint) and nginx (`not collected` with the fix hint). |
| **Month** | Net cost per service per calendar month (UTC) with a summary line (current month net, target, projected), a month picker, **Refresh month** and CSV export. |
| **Limits** | Month-to-date usage against the free-tier allowances (14 entries) with ok / near / over levels and a warn percentage; allowances and the warn percentage are editable and persist; the Groq entries are labelled stubs. |

**Service account roles.** Phase 2 reads Cloud Monitoring, Cloud Logging and Error Reporting for every project in `CLOUD0_GCP_PROJECTS`, so the read-only service account needs viewer roles on **each** of those projects (`roles/monitoring.viewer`, `roles/logging.viewer`, `roles/errorreporting.viewer`), and the Error Reporting API (`clouderrorreporting.googleapis.com`) must be enabled on them. A project that lacks a role or API does not abort a refresh: that source is recorded as failed in the run log with a fix hint and its cells read `source failed`. No new environment variables are needed.

**Refresh modes** (`POST /api/v1/refresh`): `week` runs billing plus every Phase 2 source for that week; `month` refreshes one calendar month's cost; `all` runs billing for every week, month cost for every month in range, and the Phase 2 sources for the last complete week. Each source is one row in the run log.

**How to test it by hand.** `make build && ./bin/cloud0 migrate up && ./bin/cloud0 serve`, open http://127.0.0.1:8087/, pick `CLOUD0_SMOKE_WEEK` and click **Refresh week**; then:

1. Metrics: group headings in the order above, CPU % for each VM, Memory/Disk values or an explicit gap; click a cell to see the Monitoring filter.
2. Errors: a Cloud Run table with a non-zero 4xx for at least one service; the nginx card reads `not collected` with its hint.
3. Month: month rows with a summary line containing `target` and `projected`; **Refresh month** fills the picked month.
4. Limits: a level per allowance; edit one, Save, reload (it persists); enter `-5` to see the inline error.

**Phase 2 gate.** Same command as Phase 1, `make gate`; the new checks are `tests/smoke/live_phase2_test.go` (refresh `all` then the smoke week against real GCP: CPU for at least one VM, numeric Cloud Run 4xx/5xx, limit statuses, month total equal to the sum of its service rows, one run-log row per source) and `web/e2e/phase2-journey.spec.ts` (Playwright walk of Metrics, Errors, Month and Limits with provenance and CSV). Real GCP calls are made; a skipped test is not a pass.

## Layout

```
cmd/cloud0/            entry point (serve | migrate up | migrate status | version)
internal/config        env + .env loading, validation, redacted view
internal/logging       slog JSON logger, request-log middleware
internal/isoweek       ISO week math (bounds, status, ranges)
internal/store         SQLite open/pragmas, goose migrations (migrations/0001_init.sql)
internal/billing       BigQuery billing source (query builder, fetch, error classification)
internal/costtable     pure cost-table assembly and drill-down grouping
internal/refresh       refresh orchestrator (one run, one row per source, snapshots)
internal/gcpsource     shared source result types, error classification, Monitoring helper
internal/monitoring    Cloud Monitoring sources (VM, Cloud SQL, Cloud Run, GCS, Logging)
internal/errorsrc      error sources (Cloud Run, Error Reporting, load balancer, nginx gaps)
internal/billingusage  billing-usage quantities (egress, LB hours)
internal/monthcost     calendar-month roll-up and summary
internal/limits        free-tier allowances and proximity levels
internal/httpapi       router, envelope, handlers for every endpoint in spec/api.md
internal/webui         embedded UI (dist/ synced by make web-build) + placeholder page
internal/testenv       test helpers: .env loading, BigQuery skip rule, temp DB, free port
tests/scaffold         cross-process boot test
tests/smoke            live smokes (Phase 1 cost refresh; Phase 2 metrics/errors/limits/month; real GCP, real server)
web/                   Next.js (static export) app, Vitest, Playwright
```

## Docker (not gated in Phase 1)

```sh
# working directory: repo root
docker build -t cloud0 .
docker run --rm --env-file .env -e CLOUD0_DB_PATH=/data/cloud0.db -v cloud0-data:/data cloud0 migrate up
docker run --rm --env-file .env -e CLOUD0_DB_PATH=/data/cloud0.db -v cloud0-data:/data -p 8087:8087 cloud0 serve
```

The image is multi-stage (node → go → distroless) and binds `0.0.0.0`. Mount the service-account key read-only and point `GOOGLE_APPLICATION_CREDENTIALS` at it; on Cloud Run leave it unset. Docker may be absent locally, so this path is not part of any gate yet.
