# Capability: Per-product targets (Phase 3)

## What It Does
Stores a monthly net-cost target per product (and an overall target) and shows month-to-date and projected cost against it, replacing the sheet's Summary line.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Targets | `targets` (migration 0003): product (or `*` for overall), month_from, amount_usd (legacy name; currency units) | user (Targets tab) | yes |
| Month cost by product | `month-view.md` × `product-env-breakdown.md` | store | yes |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Target progress rows | `GET /api/v1/targets?month=`; `PUT /api/v1/targets` | Targets tab; Month tab summary |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| SQLite only | | |

## Business Rules
- A target applies from `month_from` until superseded; history retained.
- Status `on_track` (projection ≤ target), `at_risk` (projection ≤ 110 %), `over`.
- Phase 4 can mirror GCP budgets as targets (`budgets.md`).

## Success Criteria
- [ ] Unit: projection and status thresholds with injected date.
- [ ] e2e: set a target, see progress and status on the Targets tab and the Month summary; persists across reload.
