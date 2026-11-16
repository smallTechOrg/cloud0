# Capability: Month view (Phase 2)

## What It Does
Shows net cost per service for calendar months (UTC) with the same column order as the Cost table, plus month-to-date vs the monthly target, mirroring the sheet's Summary.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Month | `YYYY-MM` | month picker | default current month |
| Scope | `Scope` | picker | default `all` |
| Monthly target | number | `settings` key `target.monthly_total` (Phase 3 adds per-product) | no |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Month rows | `GET /api/v1/cost/month?from=&to=&scope=` | Month tab |
| `month_snapshots` (migration 0002) | store | provenance |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| BigQuery | the Phase 1 query shape with `usage_start_time` bounded by month start/end and grouped by `EXTRACT(MONTH …)`, partition-filtered with the same margin | source failed + hint, as in `refresh-snapshot.md` |

## Business Rules
- Month totals are fetched by their own query (day boundaries, not reassembled from ISO weeks) so they match the invoice month; `invoice.month` is also stored for comparison.
- Month refresh is part of "Refresh all weeks" (one additional query) and of a "Refresh month" button on the Month tab.
- Month-to-date for the current month is labelled `partial`; last month becomes `final` after the settle period (`CLOUD0_SETTLE_HOURS` after month end).
- Summary line: "Current month net X · target Y · projected Z" (linear projection).

## Success Criteria
- [ ] Real BigQuery: the smoke week's month total equals an independent single-aggregate query over the month window ± 0.005; bytes billed < 500 MB.
- [ ] Unit: projection and partial/final labelling with injected clock.
- [ ] e2e: Month tab shows the month rows in the Cost-table column order and the summary line.
