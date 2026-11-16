# Capability: Anomaly flags + thresholds

## What It Does
Flags cells and rows whose week-over-week change exceeds both an absolute and a percentage threshold, with the thresholds editable in the UI and stored server-side.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| This week's and previous week's cells | Money | `cost-table.md` assembly | yes |
| `anomaly_pct_threshold` | number ≥ 0 | `settings` (default 20) | yes |
| `anomaly_abs_threshold_usd` | number ≥ 0 | `settings` (default 200) | yes |
| New thresholds | two numbers | user (ThresholdsEditor) | on edit |

## Outputs
| Output | Type | Destination |
|---|---|---|
| `flagged`, `delta_abs`, `delta_pct` per cell; row `flagged` | table JSON | UI markers, tooltips |
| Current thresholds | `GET/PUT /api/v1/settings` | ThresholdsEditor, table toolbar |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| SQLite | read/write `settings` | 500; UI banner; previous values remain |

## Business Rules
- `flagged = |delta_abs| ≥ abs_usd AND |delta_pct| ≥ pct` (both; **Assumed:** defaults 20 % and 200 currency units (was 50 when amounts were assumed to be USD; the export is INR, so the absolute default was raised to 200 for new databases; thresholds already stored in a DB are not changed)).
- Comparison is against the previous ISO week's latest snapshot under the same scope; no previous snapshot → not flagged, `delta_* null`.
- Applies to every service column, extra columns and `total`; the row flag is the total's flag. Increases and decreases both flag (direction shown ▲/▼).
- Thresholds are global (one pair), edited via `PUT /settings` with both values required, finite, ≥ 0; saved atomically; table re-fetches after save.
- Stub columns are never flagged.

## Success Criteria
- [ ] Unit: prev 100 → now 160 with (20 %, 50) → flagged (Δ 60, 60 %); 100 → 140 → not flagged (Δ 40 < 50); 1000 → 1100 → not flagged (10 % < 20 %); 0 → 80 → `delta_pct null`, not flagged; thresholds (0, 0) → any non-zero change flagged.
- [ ] API: `PUT /settings {10, 25}` → `GET /settings` returns 10/25; `PUT` with a negative or missing value → 400 `invalid_settings`; values survive a server restart (same DB file).
- [ ] e2e: set thresholds to 1 % / 1, Save → at least one flag appears when two consecutive fetched weeks differ; reload → the editor shows 1 / 1; restore 20 / 50.
