# Capability: Free-tier / limits warnings (Phase 2)

## What It Does
Compares the week's (and month-to-date) usage against configurable free-tier allowances and quotas from the sheet's Limits tab and warns when usage approaches or exceeds them.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Allowances | per-limit numbers | `settings` keys `limit.<id>` seeded with the sheet's values (GCS 5 GB-month / 5,000 Class A / 50,000 Class B / 100 GB NA egress; Cloud Run 2M requests / 180k vCPU-s / 360k GiB-s / 1 GB egress; Cloud Logging 50 GB; disk throughput 50 GiB/month) editable in the UI | yes |
| Usage | metric values | `metrics-block.md` rows and billing usage | yes |
| Warn threshold | percent (default 80) | `settings` | yes |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Limit status rows | `GET /api/v1/limits?week=` | Limits tab; amber/red markers on Metrics cells |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| none beyond `metrics-block.md` | | |

## Business Rules
- Monthly allowances are compared with month-to-date usage (calendar month, UTC) projected linearly to month end; weekly figures are shown alongside.
- Status: `ok` < warn %, `near` ≥ warn %, `over` ≥ 100 %.
- Groq RPM/RPD/TPM/TPD per model are listed as stub rows until Phase 4 supplies vendor data.
- Allowances editable in the Limits tab; changes recorded with `updated_at`.

## Success Criteria
- [ ] Unit: usage 4.2 GB-month of 5 with warn 80 % → `near`; 5.1 → `over`; projection math for day 10 of a 31-day month.
- [ ] Real data: Limits tab for the smoke week shows a status for every allowance without errors.
- [ ] e2e: edit an allowance → status changes accordingly after save.
