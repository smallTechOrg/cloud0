# Capability: Week annotations

## What It Does
Lets the user attach a free-text note to an ISO week that persists across refreshes, reloads and restarts, and is shown with the week's row.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Week | `WeekID` | picker | yes |
| Body | text ≤ 10,000 chars | user | yes (empty = delete) |

## Outputs
| Output | Type | Destination |
|---|---|---|
| `annotations` row | store | `GET/PUT /api/v1/annotations/{week}`; `annotation` field on table rows |
| Note indicator | icon + preview | CostTable row |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| SQLite | upsert / delete by (iso_year, iso_week) | 500; UI keeps the typed text and shows the banner |

## Business Rules
- One note per week, scope-agnostic (a note describes the week, not a project view).
- Notes are independent of snapshots: refreshing never touches them; a note may exist for a not-fetched week.
- `PUT` with a body that is empty after trimming deletes the note; > 10,000 chars → `400 annotation_too_long`.
- `updated_at` changes on every save; the UI shows "Saved HH:MMZ".

## Success Criteria
- [ ] API: `PUT` then `GET` returns the body; second `PUT` updates `updated_at` and body; `PUT ""` → `GET` 404; `PUT` 10,001 chars → 400.
- [ ] Table: `rows[].annotation` carries the body for the week, `null` otherwise.
- [ ] e2e (state survival): type a note, Save, reload the page → note present; restart the server (same DB) → still present (covered by the Go API test reopening the same temp DB file).
