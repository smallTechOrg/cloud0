# cloud0 — UI

One page at `/`, served by the Go binary. Tabs are client-side (`#cost`, `#metrics`, …). Dense, spreadsheet-like, desktop-first (min width 1100 px); plain system font; numbers right-aligned, 2 decimals, amounts formatted by `formatMoney(value, config.currency)` (Intl.NumberFormat, en-IN grouping; the header says "<CURRENCY>, net of credits", e.g. "INR, net of credits"). Every surface that is not functional in the current phase is a **labelled stub** rendered with the stub pattern below — never an empty table, never a blank panel.

## Layout

```
┌───────────────────────────────────────────────────────────────────────────────────────┐
│ TopBar: [cloud0]  Week [2026-W43 · Oct 19–25 · settling ▾]  Scope [All billed projects ▾] │
│         [Refresh]  [Refresh all weeks]        status: "Last run 09:05Z ok · 41.9 MB billed" │
├────────────┬──────────────────────────────────────────────────────────────┬───────────┤
│ NavTabs    │ Active panel                                                 │ Cell      │
│ Cost       │  (Cost tab: toolbar · CostTable · AnnotationField)           │ Inspector │
│ Metrics ◦  │                                                              │ (opens on │
│ Errors ◦   │                                                              │  cell     │
│ Attribution◦                                                              │  click)   │
│ Month ◦    │                                                              │           │
│ Alerts/Inc/│                                                              │           │
│  Alerts ◦  │                                                              │           │
│ Vendors ◦  │                                                              │           │
│ Limits ◦   │                                                              │           │
│ Rightsizing◦                                                              │           │
│ Targets ◦  │                                                              │           │
├────────────┴──────────────────────────────────────────────────────────────┴───────────┤
│ RunLogDrawer (collapsed by default): "Runs (12) ▸"                                      │
└───────────────────────────────────────────────────────────────────────────────────────┘
```
`◦` = stub badge ("P2", "P3", "P4") next to the tab name.

## Top bar

- **Week picker** — `<select>` listing every ISO week from `config.first_week` to `config.current_week`, newest first; option label `2026-W43 · Oct 19–25 · settling` (status from the table response, or "not fetched" when no snapshot); default = `config.default_week`. Changing the week highlights that row in the table and scrolls it into view; the annotation field switches to that week.
- **Scope** — `<select>`: "All billed projects" (`all`) then each project from `config.projects`. Changing scope re-fetches the table.
- **Refresh** — `POST /refresh {mode:"week", week}` for the picked week. While running: button disabled, label "Refreshing 2026-W43…", a thin progress bar in the top bar (indeterminate; typical 5–30 s). On completion: status text "Run #8 ok · 2,345 rows · 41.9 MB billed · 8.1 s"; the table re-fetches. On source failure: an `ErrorBanner` under the top bar with `error_code`, `error_message` (collapsed to 2 lines, expandable) and the `fix_hint` in bold; a "View run" link opens the run log drawer at that run.
- **Refresh all weeks** — `POST /refresh {mode:"all"}`; confirm dialog "Fetch 2026-W18 → 2026-W44 in one BigQuery job (about 1 GB scanned, 10–30 s)?" Same running/complete/failure treatment.
- **409** (another refresh running) → banner "A refresh is already running — wait for it to finish."

## Nav tabs

| Tab | Phase | Phase 1 content |
|---|---|---|
| Cost | 1 | real (below) |
| Metrics | 2 | `StubPanel` — "Coming in Phase 2 — per-week block from Cloud Monitoring: VM CPU/Memory/Disk % (with 'no agent' gaps), Cloud SQL CPU/Mem/Disk %, Networking LB hours and egress GB, Cloud Run requests / CPU-s / GiB-s / egress, Cloud Storage ops and GB-month, Cloud Logging GB." |
| Errors | 2 | `StubPanel` — "Coming in Phase 2 — 4xx/5xx per Cloud Run service from request counts, Error Reporting groups per service, load-balancer and nginx rows shown as gaps with fix hints until their logging is enabled." |
| Attribution | 3 | `StubPanel` — "Coming in Phase 3 — product and environment breakdown from GCP labels, project and resource-name rules; rules editor; unattributed list; label suggestions." |
| Month | 2 | `StubPanel` — "Coming in Phase 2 — calendar-month roll-up of net cost per service and total vs the monthly target." |
| Alerts | 1 (real) | Policies (managed/unmanaged, adopt & fix), templates (parameter forms), uptime checks, channels + severity routing, coverage, snoozes; every change is Preview → Apply (`spec/api.md → Alerts additions`). |
| Incidents | 1 (real) | Open alerts panel + alert history per ISO week. |
| Budgets | 1 (real) | Budgets list (managed/unmanaged), suggestions, create/edit/delete via Preview → Apply, change history. |
| Vendors | 4 | `StubPanel` — "Coming in Phase 4 — Groq usage via API and Claude usage via CLI, feeding the two vendor columns of the Cost table." |
| Limits | 2 | `StubPanel` — "Coming in Phase 2 — usage vs free-tier allowances (GCS, Cloud Run, Cloud Logging, disk throughput, Groq rate limits) with proximity warnings." |
| Rightsizing | 3 | `StubPanel` — "Coming in Phase 3 — VMs under ~15% CPU for weeks, idle disks and unused static IPs, with the evidence window." |
| Targets | 3 | `StubPanel` — "Coming in Phase 3 — per-product monthly targets and progress; GCP budgets mirrored as targets in Phase 4." |

**Stub pattern (`StubPanel`)** — a bordered panel with: badge "Not functional yet", heading "<Tab> — Coming in Phase N", one paragraph (above) describing exactly what it will show, and the line "Nothing on this tab is interactive in the current build." Stub columns in tables use the header suffix " (stub)", cells render "—" with `title="Arrives in Phase N"`.

## Cost tab

### Toolbar (above the table)
- Left: "Cost by service — INR, net of credits · weeks 2026-W18 → 2026-W44 · scope: All billed projects".
- Right: **Anomaly thresholds: ≥ 20 % and ≥ ₹200.00 [Edit]** → `ThresholdsEditor` popover with two numeric inputs (pct, currency amount), Save / Cancel; `PUT /settings`; after save the table re-fetches and the label updates. Validation errors shown inline from the error envelope.
- Right: **Export CSV** → downloads `cloud0-cost-<scope>-<from>-<to>.csv` built client-side from the rendered columns and rows (`spec/capabilities/cost-table.md → CSV`).

### CostTable
- Columns exactly as `GET /cost/table` returns them, in order; first column sticky; header row sticky. Service column headers show a tooltip listing the raw service names they aggregate (e.g. Gemini API → "Gemini API, Generative Language API"). Extra service columns have a subtle "+" marker (tooltip "service not in the reference sheet; appended automatically").
- One row per week ascending (oldest at top, like the sheet). Row header cell: `2026-W42` on the first line, `Oct 12–18` and a `StatusBadge` on the second line: `final` (green), `settling` (amber, tooltip "week ended; export lag not yet elapsed"), `partial` (grey, tooltip "week in progress"), `stale` (amber outline, text "Refresh for final", shown when `snapshot.stale`), `not fetched` (grey dashed).
- The picked week's row is highlighted.
- **Not-fetched rows:** the cells are replaced by one spanning cell reading "not fetched — Refresh" where "Refresh" is a link that triggers `POST /refresh` for that week. Never zeros.
- **Numeric cells:** 2 dp; zero rendered `0.00`; negative in parentheses. Hover shows gross and credits ("gross 596.92 · credits −193.51"). Click → opens the Cell Inspector for (snapshot, column).
- **Anomaly flag:** cells with `flagged: true` get a red left border and a "▲ +423.89 (+25.4 %)" / "▼" marker under the value; a flagged row shows a red dot beside the week id. Tooltip: "change vs 2026-W41 exceeds thresholds (≥ 20 % and ≥ ₹200.00)".
- **% change:** `+25.4 %` / `−3.1 %`; `—` when null, tooltip "previous week not fetched".
- **Stub columns:** header "AI Cost (Groq) (stub)", "claude (usage) (stub)"; cells "—".
- Empty state (no snapshot at all): a callout above the table — "No weeks fetched yet. Click **Refresh all weeks** to load 2026-W18 → 2026-W44 from BigQuery in one query (≈10–30 s)." The table still renders every row as "not fetched — Refresh".

### AnnotationField (below the table)
"Note for 2026-W43" · textarea (max 10,000 chars, counter) · **Save** · saved indicator "Saved 09:12Z". Loads from the row's `annotation`; `PUT /annotations/{week}`; empty → deletes and shows "No note". The table row shows a small note icon when a week has a note; hovering shows the first 120 chars.

## Cell Inspector (right panel, 420 px, closable)

Header: "2026-W42 · Compute Engine · 1,191.69" with the week StatusBadge. Sections:

1. **Provenance** (from `GET /snapshots/{id}` + the column definition + current scope):
   - Snapshot `#12`, fetched `2026-10-26 09:01:10Z`, status at fetch `final`, week status now `final` (or "stale — Refresh for final").
   - "Column = services: Compute Engine" (list from `columns[].services`); "Scope filter: all projects" or "Scope filter: project.id = project-a (applied to stored rows)".
   - Formula line from the response.
   - BigQuery job `job_abc123` · 41.2 MB processed · 41.9 MB billed · cache hit no · 7.9 s.
   - **SQL** in a `<pre>` (monospace, scrollable, Copy button) followed by **Params** as a 2-column table (`part_start`, `part_end`, `usage_start`, `usage_end`).
2. **Drill-down** (`DrilldownTable`):
   - For a service cell: `GET /snapshots/{id}/breakdown?level=sku&service=<each raw service in the column>` — one request per raw service, results merged under subheadings when the column aggregates > 1 service. Columns: SKU · gross · credits · net · lines. Click a SKU → replaces with the resource table (`level=resource`), breadcrumb "Compute Engine › N1 Predefined Instance Core … ‹ back". Resource columns: Resource (resource_name, `(unattributed)` in italics when null) · project · global name (truncated, full on hover) · gross · credits · net.
   - For the Total cell: `level=service` — raw service · column · gross · credits · net.
   - For the % change cell: provenance only, plus the previous week's snapshot id and total.
   - Sorted by net desc; totals row at the bottom must equal the clicked cell (the UI shows a red "mismatch" warning if it does not within 0.01 — this should never fire).

## RunLogDrawer (bottom, collapsible)

- Header "Runs (N) ▸" with the latest run summary. Expanded: table of runs newest first — `#id · trigger · weeks · started · duration · status · rows · bytes billed · snapshots`. Status chips `ok` green / `failed` red / `partial` amber / `running` grey.
- Expanding a run shows each source: status, job id, bytes processed/billed, cache hit, duration, and on failure `error_code`, `error_message` and the **fix hint** in a highlighted box; a "Show SQL" toggle (loads `GET /runs/{id}`).
- Auto-refreshes after every refresh action.

## Error and loading states

- `ErrorBanner` (top of content) for any API error: "`code` — message" + hint; dismissible; `internal_error` shows the request id for log correlation.
- Table/inspector loading: skeleton rows, never a blank panel; network failure → banner "cloud0 server unreachable at this origin" with the URL.
- Placeholder build: if the Go server is serving `placeholder.html` (no frontend build), the page itself says so and tells the user to run `make web`; the real UI never shows this.

## Keyboard / accessibility (minimum)

- All controls reachable by Tab; selects and buttons are native elements; numeric cells are `<button>`s inside `<td>` with `aria-label="2026-W42 Compute Engine 1191.69, open inspector"`; status badges carry `title` text; colour is never the only signal (flags also show ▲/▼ and text).

## Phase 1 real vs stub summary

Real: TopBar (all controls), Cost tab (table, flags, thresholds, CSV, annotation), Cell Inspector (provenance + drill-down), RunLogDrawer, error states. Stub: nine tabs listed above and the two vendor columns.
