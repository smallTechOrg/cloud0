# Capability: Change history (Phase 4)

## What It Does
Keeps an append-only log of every write cloud0 performs against GCP (budgets, alert policies) and every local configuration change (rules, thresholds, targets, allowances), with before/after payloads.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Change events | kind, target, before JSON, after JSON, actor (`local-user`), request id, result | every write path | yes |

## Outputs
| Output | Type | Destination |
|---|---|---|
| `change_history` rows (migration 0004) | store | `GET /api/v1/history?limit=` |
| History panel | UI (Budgets & Alerts tab and a global "History" drawer entry) | |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| SQLite only | | the originating write is rolled back if the history row cannot be written (same transaction for local changes; for GCP writes the history row is written first as `pending`, then updated) |

## Business Rules
- Append-only; no API to edit or delete entries.
- Payloads are redacted of any credential-like fields before storage.
- Thresholds/rules/targets changes from Phases 1–3 are back-filled into history from this phase on (not retroactively).

## Success Criteria
- [ ] Every write endpoint test asserts a history row with correct before/after.
- [ ] e2e: after creating a budget, the history shows the entry with the request id.
