# Capability: Change history (Alerts feature, real)

## What It Does
Append-only audit log of every GCP write the Alerts feature performs (policies, uptime checks, channels, budgets, snoozes) and of local alert configuration (severity routing), with before/after JSON.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Change events | kind, action, target, before JSON, after JSON, actor (`local-user`), preview hash | every apply / routing PUT | yes |

## Outputs
| Output | Type | Destination |
|---|---|---|
| `change_history` rows (migration 0004) | store (`store.InsertChange`, `store.ListChanges`) | `GET /api/v1/history?kind=&limit=` and `ApplyResult.change_history_id` |
| History panel | UI (Budgets tab and Alerts tab) | |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| SQLite only | | the history row is written AFTER a successful GCP write with the real before/after; if the insert fails the apply returns `internal_error` with a hint naming the applied target (the GCP change stands and must be reconciled by hand) |

## Business Rules
- Append-only; no API edits or deletes entries. Newest first; default limit 50, max 500.
- Payloads are redacted of credential-like keys on insert (`store.RedactJSON`); callers also never pass secrets.
- A failed GCP write writes no row. A preview writes nothing.
- Phase 1–3 local settings (thresholds, limits, targets) are NOT back-filled in this feature.

## Success Criteria
- [ ] Every apply path test asserts one row with correct before/after and the preview hash.
- [ ] Secret-like keys never appear in stored rows (unit-tested).
- [ ] Rows survive restart (unit-tested).
- [ ] e2e: after applying a preview, the history shows the entry.
