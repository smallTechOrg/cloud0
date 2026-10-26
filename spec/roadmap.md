# cloud0 — Roadmap

## What

cloud0 is an internal, on-demand web dashboard that replaces the hand-maintained "Tech Weekly Review" spreadsheet for a small GCP shop. It pulls billing (and, in later phases, Cloud Monitoring metrics, error counts, budgets and vendor usage), stores an immutable snapshot per ISO week, and renders the same tables the team reviews every week — plus drill-downs, provenance on click, anomaly flags, CSV export and week notes. It is deterministic end to end; there is no AI in the product (`spec/agent.md`).

## Who

The founder and the tech team, a handful of people, on demand (no scheduler), on localhost or an internal Cloud Run URL. No login in any phase.

## Success criteria (Phase 1, all testable)

- [ ] Opening the dashboard and clicking **Refresh** for a complete ISO week produces a Cost table row whose columns are exactly `Week · Compute Engine · Networking · CloudSQL · Gemini API · Cloud Run · GCS · VM manager · Cloud Build · Cloud Logging · Secret Manager · Artifact Registry · [extra services…] · Total GCP · % change · AI Cost (Groq) (stub) · claude (usage) (stub)`.
- [ ] For the smoke week (`CLOUD0_SMOKE_WEEK`), `Total GCP` equals an independent single-aggregate BigQuery query (`SUM(cost)+SUM(credits.amount)`, same filters) within 0.005, and `Compute Engine` > 0.
- [ ] Every BigQuery job in the run log reports `bytes_billed` below 200 MB for a single week and below 2 GB for Refresh-all, and the stored SQL contains `DATE(_PARTITIONTIME) BETWEEN`.
- [ ] Drill-down totals reconcile: Σ SKU net = service cell net, Σ resource net = SKU net (± 0.005).
- [ ] A cell click shows the exact executed SQL and parameters, the BigQuery job id, fetch time, `status_at_fetch`, current week status and snapshot id.
- [ ] Two refreshes of the same week create two snapshots; the table shows the latest; the run log lists both.
- [ ] Rows without a snapshot read "not fetched — Refresh", never zeros; **Refresh all weeks** fills every week first→current with one BigQuery job.
- [ ] Anomaly flag appears exactly when |Δ| ≥ abs threshold **and** |Δ%| ≥ pct threshold against the previous week's latest snapshot; thresholds editable and persisted.
- [ ] A week annotation survives a page reload and a server restart.
- [ ] CSV export of the visible table opens in a spreadsheet with the same header order and 2-dp values.
- [ ] Every HTTP request produces one JSON log line; a refresh produces `refresh.start` / `source.done` / `refresh.done` lines.
- [ ] Refresh of one week completes in < 30 s; the dashboard loads stored weeks in < 1 s.

## Out of scope (all phases unless listed in a later phase)

- Natural-language Q&A, chat, LLM-written summaries, Slack/email digests, scheduled refreshes.
- Writing to Google Sheets; writing to any GCP resource except (Phase 4) budgets and alert policies.
- Authentication / multi-tenant; the tool's own GCP cost in the UI.
- Column chooser / hidden columns; per-cell annotations before Phase 3; data retention pruning before Phase 4.
- Any identifier of the user's GCP environment in committed files (env vars only).

## Capabilities by phase

See `spec/capabilities/index.md` for the full list and status. Summary:

| Phase | Capabilities |
|---|---|
| 1 | Week & scope selection · Refresh → snapshot · Cost table (+ CSV export) · Drill-down · Cell provenance · Anomaly flags · Run log · Week annotations |
| 2 | Metrics block · Errors row · Free-tier / limits warnings · Month view |
| 3 | Attribution rules engine · Product & environment breakdown · Label suggestions · Per-product targets · Rightsizing hints |
| 4 | Budgets · Alert policies & channels · Change history · Vendor usage (Groq, Claude) mirrored into the Cost table |

---

## Phases of Development

Conventions for every slice below: paths are relative to `/Users/sai/Workspace/Code/cloud0`; gates are run from that directory; `GO` is `/opt/homebrew/bin/go`, `pnpm` is the asdf shim; all tests that touch BigQuery load `.env` themselves and run for real (skipping only when the variables are genuinely absent — a skip does not pass a gate). HOW (stack, layout, commands) is in `spec/architecture.md`; the slices only name what they own.

### Scaffold — the runnable skeleton (one slice, lands before everything else)

**Goal:** `./bin/cloud0 serve` boots on `http://127.0.0.1:8087/` with a real (if nearly empty) Next.js UI embedded, JSON request logs, migrations applied and verifiable, and one boot test green.

**Owns:** `go.mod`, `go.sum`, `cmd/cloud0/main.go`, `internal/config/`, `internal/logging/`, `internal/isoweek/`, `internal/store/store.go`, `internal/store/migrations/0001_init.sql`, `internal/httpapi/server.go`, `internal/httpapi/routes.go` (health + config routes only), `internal/httpapi/envelope.go`, `internal/httpapi/health.go`, `internal/httpapi/config.go`, `internal/webui/`, `internal/testenv/`, `tests/scaffold/boot_test.go`, `web/` skeleton (`package.json`, lockfile, `next.config.ts`, `tsconfig.json`, `src/app/layout.tsx`, `src/app/page.tsx` rendering the app title, a `#cost` nav placeholder and the health status from `/api/v1/config`, `src/app/globals.css`, `src/lib/api.ts` with `getHealth`/`getConfig` only, `playwright.config.ts`, `vitest.config.ts`, an empty-but-valid `e2e/` and one trivial `src/lib/week.test.ts`), `Makefile`, `Dockerfile`, `.env.example`, `.gitignore` additions, `README.md`, `NOTES.md` (append).

**Contents:** subcommands `serve | migrate up | migrate status | version`; config loader with `.env` loading and validation; slog JSON middleware; migration 0001 with the full Phase 1 schema (`spec/data.md`); `isoweek` fully unit-tested (W18 2026 starts 2026-04-27; 2026 has 53 weeks; status partial/settling/final with injected `now`); embedded placeholder + real-UI detection; `/api/v1/health`, `/api/v1/config`.

**Dependencies:** none.

**Gate (exact):**
```
cd /Users/sai/Workspace/Code/cloud0 && make scaffold-gate
```
which runs, in order: `make web-install`, `make web-build`, `/opt/homebrew/bin/go build -o bin/cloud0 ./cmd/cloud0`, `./bin/cloud0 migrate up`, `./bin/cloud0 migrate status` (must list `0001_init.sql` as applied), `/opt/homebrew/bin/go vet ./...`, `/opt/homebrew/bin/go test ./internal/... -count=1 -timeout 15m`, `/opt/homebrew/bin/go test ./tests/... -count=1 -p 1 -timeout 15m` (includes `tests/scaffold`, which builds the binary, migrates a temp DB, starts `serve` with `CLOUD0_PORT` set to an OS-assigned free port in the child environment, asserts `GET /api/v1/health` → `{"ok":true,"ui":"real"}`, asserts `GET /` contains `data-cloud0-ui="real"` and that its linked CSS/JS return 200 non-empty, and asserts one JSON log line with `"msg":"request"` was written). Also: `.env.example` complete (every variable in `spec/architecture.md → Environment variables`); the real BigQuery credentials in `.env` validated once by a minimal query (`SELECT 1` through `cloud.google.com/go/bigquery` in `internal/testenv`'s `TestCredentialsAlive`, which skips with a message only if the vars are absent).

**How the user tests it:** update `.env` (`CLOUD0_PORT=8087`), run `make build && ./bin/cloud0 migrate up && ./bin/cloud0 migrate status && ./bin/cloud0 serve`, open `http://127.0.0.1:8087/`, see the cloud0 shell with "connected" status and a labelled "Cost table arrives in Phase 1" placeholder; `curl` health shows `ui: real`; the terminal shows one JSON line per request.

### Phase 1 — Weekly cost review (the first win)

**Goal:** pick a week and scope → Refresh → real net-of-credits Cost table in the sheet's column order for every stored week → drill into SKU/resource → provenance on click → CSV export → anomaly flags with editable thresholds → run log → week annotation. Every other tab is a labelled stub.

Slices (fan out S1, S2, S4 concurrently after the scaffold; S3 after S1+S2; S5 after S3+S4):

#### S1 — BigQuery billing source
- **Owns:** `internal/billing/` (`query.go`, `source.go`, `hints.go`, `query_test.go`, `source_test.go`).
- **Delivers:** `BuildQuery(window, table)` returning the exact SQL text and typed params (`spec/architecture.md → BigQuery query`); `Fetch(ctx, window)` executing it with job statistics; `DryRun(ctx)`; `Classify(err)` → (`error_code`, message, fix hint) per `spec/capabilities/run-log.md`.
- **Dependencies:** Scaffold (`config`, `isoweek`, `testenv`).
- **Gate:** `cd /Users/sai/Workspace/Code/cloud0 && /opt/homebrew/bin/go test ./internal/billing/ -count=1 -v` — real BigQuery: for `CLOUD0_SMOKE_WEEK` the grouped rows sum to the independent single-aggregate query within 0.005; a row with `service == "Compute Engine"` has net > 0; `rows > 100`; `bytes_billed < 200 MB`; `sql_text` contains `DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end`; params match the week's Monday bounds ±1 day; a query against a deliberately wrong table name classifies as `table_not_found` with a hint naming `CLOUD0_BILLING_EXPORT_TABLE`. Pure tests cover param derivation for W53 and year rollover.
- **How the user tests it:** `go test -v` output prints the job's bytes billed and the reconciled total for the smoke week.

#### S2 — Store repositories + cost-table assembly
- **Owns:** `internal/store/snapshots.go`, `runs.go`, `settings.go`, `annotations.go`, their `_test.go`; `internal/costtable/` (`columns.go`, `table.go`, tests).
- **Delivers:** repositories per `spec/data.md`; pure `costtable.Assemble` implementing `spec/capabilities/cost-table.md` (mapping, extra columns, totals, % change, anomalies, scope filter, statuses, stale flag) and `costtable.Breakdown` grouping for `spec/capabilities/drilldown.md`.
- **Dependencies:** Scaffold (`store.Open`, migration 0001, `isoweek`, `testenv.TempDB`).
- **Gate:** `cd /Users/sai/Workspace/Code/cloud0 && /opt/homebrew/bin/go test ./internal/store/ ./internal/costtable/ -count=1` on temp SQLite: latest-per-week picks the highest id; a service named `Generative Language API` lands in the `gemini_api` column; an unknown service becomes `svc:<name>` placed before `total`; `% change` is null without a previous snapshot; anomaly requires both thresholds; stale flag when `status_at_fetch=partial` and the week is now final; annotations upsert/delete; thresholds round-trip.
- **How the user tests it:** indirectly via S3/S5.

#### S3 — Refresh orchestrator + HTTP API
- **Owns:** `internal/refresh/`, `internal/httpapi/table.go`, `refresh.go`, `snapshots.go`, `runs.go`, `settings.go`, `annotations.go`, their `_test.go`; **edits** `internal/httpapi/routes.go` (adds routes) and `internal/httpapi/health.go` (adds `bigquery` field) — sequential after the scaffold, so no concurrent edit.
- **Delivers:** every endpoint in `spec/api.md` exactly; one-refresh-at-a-time (409); transactional snapshot writes; refresh logs.
- **Dependencies:** S1, S2.
- **Gate:** `cd /Users/sai/Workspace/Code/cloud0 && /opt/homebrew/bin/go test ./internal/refresh/ ./internal/httpapi/ -count=1` — `httptest` server with temp SQLite and the real BigQuery source: `POST /api/v1/refresh {mode:"week", week:$CLOUD0_SMOKE_WEEK}` → `run.status == "ok"`, one snapshot; `GET /api/v1/cost/table` → that week's `cells.compute_engine.net > 0`, `cells.total.net` equals Σ service cells ± 0.005, other weeks `cells == null`; second refresh → new snapshot id, `GET /api/v1/runs` has 2 runs; `GET /api/v1/snapshots/{id}` returns `sql` containing `_PARTITIONTIME` and `params` with 4 keys; breakdown reconciliation at sku and resource levels; invalid week → 400 `invalid_week`; week after current → 400 `week_out_of_range`; settings PUT/GET round-trip; annotation PUT/GET/empty-PUT; concurrent second refresh → 409.
- **How the user tests it:** `curl -X POST localhost:8087/api/v1/refresh -d '{"mode":"week","week":"2026-W42"}'` then `curl localhost:8087/api/v1/cost/table | jq '.rows[] | select(.week=="2026-W42")'`.

#### S4 — Frontend (static UI)
- **Owns:** everything under `web/` beyond the scaffold skeleton: `src/components/*`, `src/lib/types.ts`, `src/lib/api.ts` (extends), `src/lib/csv.ts` + test, `src/lib/week.ts` + test, `src/app/page.tsx` (replaces skeleton), styles, `e2e/cost-journey.spec.ts`.
- **Delivers:** `spec/ui.md` in full: top bar, nav tabs with stub panels, Cost table, cell inspector (provenance + drill-down), CSV export, run-log drawer, annotation field, thresholds editor, error banners with hints; the Playwright journey.
- **Dependencies:** Scaffold; builds against `spec/api.md` only (can proceed in parallel with S1–S3); its e2e passes only once S3 is live.
- **Gate:** `cd /Users/sai/Workspace/Code/cloud0 && make web-build && make web-test` (TypeScript strict build + Vitest: `buildCsv` quotes commas/quotes/newlines, formats 2 dp, exports visible columns in order, empty cells for not-fetched rows; week labels and status text), then — after S3 — `make build && make e2e`.
- **How the user tests it:** see Phase 1 "How the user tests it" below.

#### S5 — Live smoke, gate wiring, docs
- **Owns:** `tests/smoke/live_test.go`, `Makefile` targets `gate` (edits the scaffold's Makefile), `README.md` Phase 1 sections, `NOTES.md` entries.
- **Delivers:** the Go live smoke: runs `make build`, starts `./bin/cloud0 migrate up && ./bin/cloud0 serve` with `CLOUD0_PORT` set to an OS-assigned free port and `CLOUD0_DB_PATH` to a temp file (all other variables from `.env`), waits for health (including `bigquery.status == "ok"` from the startup dry-run), `POST /api/v1/refresh` for `CLOUD0_SMOKE_WEEK`, asserts `run.status=="ok"`, `GET /api/v1/cost/table` → Compute Engine net > 0 and Total > 0 and Total = Σ services ± 0.005, fetches `/` and asserts `data-cloud0-ui="real"` plus every `<link rel="stylesheet">` and `<script src>` returns 200 non-empty and the CSS contains `{`, asserts a `"msg":"request"` JSON log line and a `"msg":"refresh.done"` line were emitted, then stops the server and deletes the temp DB.
- **Dependencies:** S3, S4.
- **Gate:** the Phase 1 gate below.

**Phase 1 gate (exact, all must pass):**
```
cd /Users/sai/Workspace/Code/cloud0 && make gate
```
= `make build` (pnpm install + `pnpm build` + sync + `go build`) → `./bin/cloud0 migrate up && ./bin/cloud0 migrate status` → `/opt/homebrew/bin/go vet ./... && /opt/homebrew/bin/go test ./internal/... -count=1 -timeout 15m && /opt/homebrew/bin/go test ./tests/... -count=1 -p 1 -timeout 15m` (unit + real-BigQuery integration + `tests/scaffold` and `tests/smoke` live servers, serialised) → `cd web && pnpm test` → `cd web && pnpm exec playwright test` (Playwright launches `./bin/cloud0 migrate up && ./bin/cloud0 serve` with a temp DB on `CLOUD0_E2E_PORT`, walks: load `/` → select `CLOUD0_SMOKE_WEEK` → Refresh → row shows Compute Engine > 0 and Total > 0 → click Compute Engine cell → inspector shows SQL containing `_PARTITIONTIME`, snapshot id, status → SKU rows ≥ 1 → click first SKU → resource rows ≥ 1 → Export CSV download whose first line equals the visible headers → type annotation, Save, reload, still present → set thresholds 10 / 5, Save, reload, still 10 / 5 → Attribution tab shows "Coming in Phase 3" → run log drawer lists the run with bytes billed). Then the phase rules: boots via the documented run command (root session launches it in the background and health-checks), tree committed, README commands re-run, test-handoff published.

**How the user tests Phase 1:** `make build && ./bin/cloud0 migrate up && ./bin/cloud0 serve`; open `http://127.0.0.1:8087/`. Expect: week picker defaulting to last complete week, scope "All billed projects", every row "not fetched — Refresh". Click **Refresh all weeks** (≈10–30 s; progress text visible) → every week fills; the current week is badged *partial*, last week *settling* or *final*. Compare the Total GCP of a known week with the spreadsheet (expect ≈ equal; small differences come from the sheet's manual day boundaries). Click a Compute Engine cell → inspector: SQL + params + job id + bytes + fetch time + status; SKU list; click a SKU → resource rows with gross/credits/net; Networking shows gross > net (discount credits); VM manager shows 0 with gross offset by credits in drill-down. Click **Export CSV** → open the file. Change thresholds to 5% / 5 → more flags appear; set back. Type a note on the selected week → Save → reload → persists. Open the run log → runs with duration, rows, bytes billed. Switch scope to one project → numbers change; provenance shows the scope filter line. **Stubs (labelled, not bugs):** Metrics, Errors, Attribution, Month, Budgets & Alerts, Vendors, Limits, Rightsizing, Targets tabs each show a "Coming in Phase N" panel; the two vendor columns show "(stub)" with "—" cells.

### Phase 2 — Metrics + Errors + Month view

**Goal:** the Metrics tab block from Cloud Monitoring, the Errors row from Cloud Run request counts + Error Reporting (with LB/nginx shown as gaps with fix hints), free-tier/limit proximity warnings, and a calendar-month roll-up of cost.

**Capabilities:** `metrics-block`, `errors-row`, `limits-warnings`, `month-view` (`spec/capabilities/`).

**Slices:** (a) Monitoring source(s) — `internal/monitoring/` (time-series fetch per metric family, "no agent" gap detection, exact filter text stored as provenance); (b) Errors sources — `internal/errorsrc/` (Cloud Run `request_count` by `response_code_class`, Error Reporting `groupStats`, LB/nginx gap detectors with fix hints); (c) Store + API — migration `0002` (`metric_snapshots`, `metric_rows`, `limits` settings keys), endpoints `GET /api/v1/metrics/table`, `GET /api/v1/errors/table`, `GET /api/v1/limits`, `GET /api/v1/cost/month`, refresh extended to run all sources and record one `run_sources` row each; (d) Frontend — Metrics, Errors, Month, Limits tabs replace their stubs; (e) smoke/e2e extension. Dependencies: (c) after (a)+(b); (d) parallel on the frozen API; (e) last.

**Gate:** `cd /Users/sai/Workspace/Code/cloud0 && make gate` with new real-API tests: for `CLOUD0_SMOKE_WEEK` the Metrics table has a CPU% value for every VM in the project and a `no agent` gap (not 0) for any VM without the Ops Agent (currently every VM has it, so the live tests assert real memory values or explicit gaps, never a bare 0); the Errors row has a numeric 4xx and 5xx for at least one Cloud Run service; the LB row shows counts when the load-balancer metric has data (it does even with backend logging off) and otherwise a gap card whose hint contains "backend service logging"; the nginx row is a `not collected` gap card with its fix hint; month roll-up for the smoke week's month equals Σ of the daily-bounded cost query (± 0.005); e2e walks Metrics → Errors → Month → Limits with real content.

**How the user tests it:** Refresh the smoke week; Metrics tab shows the sheet's block (VMs CPU/Mem/Disk, Cloud SQL, Networking, Cloud Run, GCS, Logging) with values matching the sheet to within rounding and "no agent" for any un-instrumented VM; Errors tab shows 4xx/5xx per Cloud Run service, load-balancer counts (or a gap card with the fix hint when absent) and the nginx `not collected` card; Limits tab shows usage vs free-tier allowance with amber/red proximity; Month tab shows the calendar-month total. Stubs remaining: Attribution, Budgets & Alerts, Vendors, Rightsizing, Targets.

### Phase 3 — Attribution + Insights

**Goal:** product/environment attribution via a deterministic rules engine (GCP labels > project > name tokens), auto-seeded from names/labels, editable with an unattributed list; breakdowns by product and environment; label suggestions; per-product monthly targets; rightsizing hints; the optional, off-by-default OpenRouter rule proposer (`spec/agent.md`).

**Capabilities:** `attribution-rules`, `product-env-breakdown`, `label-suggestions`, `product-targets`, `rightsizing-hints`.

**Slices:** (a) `internal/attribution/` engine + seeder + `RuleProposer` interface with `NoopProposer` (+ `OpenRouterProposer` behind the key); (b) migration `0003` (`attribution_rules`, `targets`, cell-level `annotations.cell_key`) + endpoints `GET/PUT /api/v1/attribution/rules`, `GET /api/v1/attribution/unattributed`, `POST /api/v1/attribution/propose`, `GET /api/v1/cost/by-product`, `GET /api/v1/cost/by-environment`, `GET/PUT /api/v1/targets`, `GET /api/v1/rightsizing`; (c) Frontend — Attribution tab (rules editor, unattributed list, label command snippets), Rightsizing tab, Targets tab, product/environment toggles on the Cost table; (d) smoke/e2e extension.

**Gate:** `make gate` with: the seeder attributes ≥ 90% of the smoke week's net to a product (unattributed listed with reasons); Σ product nets + unattributed = Total GCP ± 0.005; a label on a resource overrides a name-token rule in a unit test; provenance of a product cell names the matched rule; targets persist and the Summary shows cost vs target; rightsizing lists every VM with 4-week mean CPU < 15% (real Monitoring data); with `CLOUD0_OPENROUTER_API_KEY` unset the propose endpoint returns 200 with `enabled:false` and no network call.

**How the user tests it:** Attribution tab lists products inferred from resource names and labels with per-product weekly cost; edit a rule → table updates; unattributed items show "no resource name" reasons and suggested `gcloud` label commands; Targets tab lets them enter the monthly target per product and shows progress; Rightsizing lists low-CPU VMs with the evidence window. Stubs remaining: Budgets & Alerts, Vendors.

### Phase 4 — Budgets, Alerts, Vendors

**Goal:** enable the Billing Budgets API and roles (documented setup step), view/create/edit Billing budgets and Monitoring alert policies with notification channels, a change-history log of every write, Groq usage via its API and Claude usage via a CLI runner into the two vendor columns, GCP budgets mirrored as targets.

**Capabilities:** `budgets`, `alert-policies`, `change-history`, `vendor-usage`.

**Slices:** (a) `internal/budgets/` (Budgets API read/write), `internal/alerting/` (policies + channels read/write); (b) `internal/vendors/` (Groq API read-only, Claude CLI runner) writing `vendor_usage` rows per week; (c) migration `0004` (`change_history`, `vendor_usage`) + endpoints `GET/POST/PUT /api/v1/budgets`, `GET/POST/PUT /api/v1/alerts/policies`, `GET /api/v1/alerts/channels`, `GET /api/v1/history`, `GET /api/v1/vendors/usage`; (d) Frontend — Budgets & Alerts tab, Vendors tab, vendor columns become real; (e) README setup section for enabling `billingbudgets.googleapis.com` and granting the SA budget roles; (f) smoke/e2e extension.

**Gate:** `make gate` with real-API tests: list budgets and alert policies succeed (≥ 4 policies, 1 channel in the known environment); creating then deleting a test budget round-trips and writes two `change_history` rows; Groq usage for the smoke week is numeric when `CLOUD0_GROQ_API_KEY` is set (skip with message otherwise, documented as a gate blocker if the key is expected); the Cost table's vendor columns are no longer stubs; the setup README commands have been run and `GET /api/v1/budgets` returns 200.

**How the user tests it:** follow the README setup to enable the Budgets API; Budgets & Alerts tab lists existing alert policies and channels; create a budget; see it in GCP console and in the change history; Vendors tab shows Groq/Claude weekly usage; the Cost table's last two columns are filled. No stubs remain.
