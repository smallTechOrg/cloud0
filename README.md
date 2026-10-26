# cloud0

Internal dashboard for a small GCP shop that replaces the hand-maintained "Tech Weekly Review" spreadsheet: it pulls the detailed billing export from BigQuery, stores an immutable snapshot per ISO week (Mon–Sun, UTC) and renders the weekly Cost table — net of credits — with drill-downs, provenance, anomaly flags, CSV export and notes. One Go binary serves the JSON API and the embedded Next.js UI on one origin. No AI in the product; nothing is scheduled.

Design and contract: [`spec/`](spec/) (`architecture.md`, `api.md`, `data.md`, `ui.md`, `roadmap.md`). Build journal: [`NOTES.md`](NOTES.md).

**All commands below run from the repository root** (`/Users/sai/Workspace/Code/cloud0`) unless the code block says otherwise. The dashboard URL is **http://127.0.0.1:8087/**.

## What exists today (scaffold) vs. what Phase 1 adds

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

Open **http://127.0.0.1:8087/**. Expected: the cloud0 shell with the status line reading `connected · v0.1.0 · db ok · migrations current · ui real · bigquery not checked (Phase 1)`, a configuration card (projects, weeks shown, default week), the Cost tab showing the labelled placeholder "Cost table arrives in Phase 1", and the other tabs badged with their phase. The terminal prints one JSON line per request. Stop with Ctrl-C (graceful shutdown).

Verify from another terminal:

```sh
# working directory: repo root
curl -s http://127.0.0.1:8087/api/v1/health    # {"ok":true,"version":"0.1.0","db":"ok","migrations":"current","ui":"real","bigquery":null}
curl -s http://127.0.0.1:8087/api/v1/config    # {"projects":[...],"first_week":"2026-W18","current_week":"...","default_week":"...","settle_hours":48,"now":"..."}
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
- Every test uses its own temp SQLite file; nothing touches `./data/cloud0.db`.

Gates (`spec/roadmap.md`):

```sh
# working directory: repo root
make scaffold-gate   # build → migrate → test
make gate            # scaffold-gate → web-test → e2e
```

## Layout

```
cmd/cloud0/            entry point (serve | migrate up | migrate status | version)
internal/config        env + .env loading, validation, redacted view
internal/logging       slog JSON logger, request-log middleware
internal/isoweek       ISO week math (bounds, status, ranges)
internal/store         SQLite open/pragmas, goose migrations (migrations/0001_init.sql)
internal/httpapi       router, envelope, health + config handlers
internal/webui         embedded UI (dist/ synced by make web-build) + placeholder page
internal/testenv       test helpers: .env loading, BigQuery skip rule, temp DB, free port
tests/scaffold         cross-process boot test
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
