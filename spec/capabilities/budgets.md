# Capability: Budgets (Alerts feature, real)

## What It Does
Lists Cloud Billing budgets of the configured billing account, suggests amounts from stored snapshots, and creates / edits / deletes budgets through Preview → Apply. Existing unmanaged budgets are shown as such and can be adopted.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Billing account | `CLOUD0_BILLING_ACCOUNT_ID` | env | yes |
| Alerts credentials | `CLOUD0_ALERTS_CREDENTIALS` (SA `cloud0-alerts`, `roles/billing.costsManager`) | env | for any budget call |
| Draft | `BudgetDraft` (display name `cloud0-…`, amount > 0 INR, projects, service ids, thresholds, severity/channel override) | user | create/edit |
| Snapshots, `target.monthly_total`, `alerts.routing.*` | store | local | suggestions / channels |

## Outputs
| Output | Type | Destination |
|---|---|---|
| `GET /api/v1/budgets`, `GET /api/v1/budgets/suggestions` | JSON | UI Budgets tab |
| create/update/delete | `POST /alerts/preview` + `POST /alerts/apply` (`kind: "budget"`) | GCP + `change_history` |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| Cloud Billing Budgets API (`billingbudgets.googleapis.com`, quota/user project = main project) | List/Get/Create/Update/Delete | `alerts_not_configured` 503, `permission_denied` 403 (hint `roles/billing.costsManager` on the billing account), `api_disabled` 503 |

## Business Rules
- Managed ⇔ display name starts with `cloud0-` (no labels in the API). Everything else is unmanaged: shown, never changed until the user previews an explicit edit/adopt/delete.
- Suggested amount = 1.2 × average weekly net cost of the last ≤ 4 complete weeks × 30/7 (INR, stored snapshots); account budget uses `target.monthly_total` when set. Prefilled and editable.
- Default thresholds 50 / 80 / 100 % current spend + 100 % forecast; notifications go to the severity-routed channels (default severity ERROR) plus default IAM recipients.
- Adopt (`budget_account`) = create the managed copy with channels (preview → apply); the old budget is never deleted automatically. Delete needs `confirm_delete`.
- Known case: an unmanaged "Ai-agent-boilerplate" ₹5,000/month budget on the main project, thresholds 50/90/100 %, no channels → shown with a warning and the adopt action.

## Success Criteria
- [ ] Real read: `GET /budgets` returns ≥ 1 budget (the unmanaged one) with `managed:false`.
- [ ] Preview → apply create of a `cloud0-test-…` budget → listed → update amount → delete with confirm → ≥ 3 history rows; cleanup guaranteed.
- [ ] Apply with a stale/unknown token → 409 `preview_stale`; delete without `confirm_delete` → 400 `confirm_required`.
- [ ] e2e: Budgets tab lists budgets, suggestion prefills the form, apply is disabled when `alerts_configured` is false.
