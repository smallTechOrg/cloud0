# Capability: Week & scope selection

## What It Does
Lets the user choose the ISO week (Mon–Sun, UTC) and the project scope that every other Cost-tab surface (table highlight, refresh target, annotation, inspector) refers to, with the last complete week and "all billed projects" preselected.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| First week | `WeekID` | `CLOUD0_FIRST_WEEK` via `GET /config` | yes |
| Current week | `WeekID` | server clock (UTC) via `GET /config` | yes |
| Project list | `string[]` | `CLOUD0_GCP_PROJECTS` via `GET /config` | yes |
| Picked week | `WeekID` | user (week picker) | defaults to `default_week` |
| Picked scope | `Scope` | user (scope select) | defaults to `all` |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Selected week + scope | UI state | table highlight, Refresh button, annotation field, inspector scope filter, `GET /cost/table?scope=` |
| Week status label | `partial \| settling \| final \| not fetched` | picker option label and row badge |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| cloud0 API | `GET /api/v1/config` | banner "server unreachable"; picker disabled |

## Business Rules
- Week bounds: Monday 00:00:00 UTC inclusive → next Monday 00:00:00 UTC exclusive. Labels show inclusive dates ("Oct 19–25").
- `current_week` = ISO week containing server `now` (UTC). `default_week` = `current_week − 1`, clamped to ≥ `first_week`.
- Week status: `partial` while `now < week_end`; `settling` while `week_end ≤ now < week_end + CLOUD0_SETTLE_HOURS`; `final` after. Computed on the server, never stored except as `status_at_fetch` on snapshots.
- The picker lists only `first_week … current_week`; weeks outside are rejected by the API (`week_out_of_range`).
- ISO edge cases handled: a year with 53 weeks (2026), week 1 starting in the previous calendar year, December dates belonging to week 1 of the next year.
- Scope only filters stored rows; it never changes what Refresh fetches (`refresh-snapshot.md`).

## Success Criteria
- [ ] Unit: `isoweek.Bounds("2026-W18")` = 2026-04-27T00:00Z → 2026-05-04T00:00Z; `isoweek.Bounds("2026-W53")` valid; `"2027-W53"` invalid; `Current(2026-10-26T00:00Z)` = `2026-W44`; `Status` returns partial/settling/final at `week_end − 1s`, `week_end + 47h`, `week_end + 48h` with `settle_hours = 48`.
- [ ] `GET /config` returns `default_week` one less than `current_week` and `projects` in `.env` order.
- [ ] e2e: on load the picker shows `default_week` selected and the scope "All billed projects"; choosing another week highlights its row; choosing a project re-fetches the table with `scope=<project>`.
