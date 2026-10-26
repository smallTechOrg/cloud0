# Capability: Budgets (Phase 4)

## What It Does
Lists, creates and edits Cloud Billing budgets for the configured billing account, and can mirror them as targets.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Billing account | `CLOUD0_BILLING_ACCOUNT_ID` | env | yes |
| Budget definitions | name, amount, period, project filter, threshold rules, notification channels | user (Budgets & Alerts tab) | for create/edit |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Budget list / detail | `GET /api/v1/budgets`, `POST /api/v1/budgets`, `PUT /api/v1/budgets/{id}` | UI |
| Change history entries | `change-history.md` | store |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| Cloud Billing Budget API (`cloud.google.com/go/billing/budgets`) | `ListBudgets`, `CreateBudget`, `UpdateBudget` | error envelope with hint: API not enabled → "Enable billingbudgets.googleapis.com and grant the service account roles/billing.costsManager (or budget viewer/editor roles) on the billing account — see README → Phase 4 setup"; permission → same roles |

## Business Rules
- Setup is a documented, user-run step (README) performed before the phase is tested: enable the API, grant roles. cloud0 never grants itself roles.
- Writes are confirmed in the UI (dialog shows the exact request) and recorded in `change_history` before and after.
- "Mirror as target" creates/updates a `targets` row (`product-targets.md`) from the budget amount.

## Success Criteria
- [ ] Real API: `GET /budgets` returns 200 (list may be empty) after the setup step; before setup it returns the `api_disabled` envelope with the hint.
- [ ] Create a test budget → appears in the list and in GCP → update amount → delete (via `PUT` with `state: deleted` or a `DELETE` endpoint added at that time) → two+ history rows.
- [ ] e2e: Budgets & Alerts tab lists budgets; create dialog validates amount > 0.
