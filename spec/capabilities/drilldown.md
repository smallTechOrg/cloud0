# Capability: Drill-down service → SKU → resource

## What It Does
From any service (or Total) cell, shows which SKUs and then which resources make up the number, with gross, credits and net, reconciling exactly to the clicked cell.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Snapshot id | int | clicked row | yes |
| Column services | `string[]` | `columns[].services` | yes |
| Scope | `Scope` | picker | default `all` |
| Level, service, sku | query params | inspector navigation | per level |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Breakdown rows + totals | `GET /api/v1/snapshots/{id}/breakdown` JSON | Cell Inspector → DrilldownTable |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| SQLite | `GROUP BY` over `snapshot_rows` of one snapshot | 500 `internal_error`; inspector shows the error with hint |

## Business Rules
- Levels: `service` (raw `service.description`, with the column it maps to) → `sku` (within one raw service) → `resource` (within one service + SKU, grouped by `project_id`, `resource_name`, `resource_global_name`).
- A column that aggregates several raw services (Gemini API) issues one `sku` request per raw service; the inspector shows them under subheadings.
- `resource_name` NULL → key `(unattributed)` (e.g. load-balancer forwarding-rule minimums, Artifact Registry storage); never dropped, never merged with named resources.
- Each row carries `gross`, `credits`, `net`, `line_items`; sorted by `net` desc, then key. `totals` sum the rows.
- Reconciliation invariant: `sku` totals = the service cell for the same snapshot + scope; `resource` totals = the SKU row (exact float sums of the same stored rows; UI tolerance 0.01 for display rounding).
- Read-only over stored rows; no BigQuery call.
- Scope filter identical to the table's.

## Success Criteria
- [ ] Unit (`store.Breakdown`): grouping at each level; NULL resource → `(unattributed)`; sorting; scope filter.
- [ ] Real data (API test, `CLOUD0_SMOKE_WEEK`): for `Compute Engine`, Σ `sku` rows' net = `cells.compute_engine.net` ± 0.005; for the top SKU, Σ `resource` rows' net = that SKU's net ± 0.005; `level=service` totals = `cells.total.net` ± 0.005; `Networking` at `sku` level shows at least one row with `credits < 0`.
- [ ] `level=resource` without `sku` → 400 `invalid_level`; unknown snapshot → 404.
- [ ] e2e: click Compute Engine cell → ≥ 1 SKU row; click first SKU → ≥ 1 resource row; breadcrumb back returns to SKUs; totals row equals the clicked cell.
