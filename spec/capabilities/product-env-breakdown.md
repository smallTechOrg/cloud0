# Capability: Product & environment breakdown (Phase 3)

## What It Does
Shows weekly net cost by product and by environment (and product × environment), using `attribution-rules.md`, as alternative views of the Cost table with the same drill-down and provenance.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Attributed rows | `attribution_results` | store | yes |
| View | `service \| product \| environment \| product_env` | Cost tab toggle | default `service` |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Columns = products (sorted by net desc, `(unattributed)` last) or environments, then Total | `GET /api/v1/cost/by-product`, `GET /api/v1/cost/by-environment` (same row/cell shape as `/cost/table`) | Cost tab, CSV export |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| SQLite only | | |

## Business Rules
- Same weeks, statuses, anomaly flags and CSV rules as the service view; only the column set changes.
- Drill-down from a product cell: service → SKU → resource within that product.
- Provenance adds "matched rule: #id (kind, pattern)" per resource.

## Success Criteria
- [ ] Σ product columns = Σ service columns for the same snapshot and scope (exact float sums of the same rows).
- [ ] e2e: toggle to Product view → columns change, Total unchanged; click a product cell → services within it.
